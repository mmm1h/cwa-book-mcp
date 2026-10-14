package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/mmm1h/cwa-book-mcp/internal/catalog"
	"io"
	"os"
	"testing"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/config"
	"github.com/mmm1h/cwa-book-mcp/internal/cwa"
	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/files"
	zlib "github.com/mmm1h/cwa-book-mcp/internal/source/zlib"
	"github.com/mmm1h/cwa-book-mcp/internal/store"
	"github.com/mmm1h/cwa-book-mcp/internal/testfixture"
)

func setup(t *testing.T) (*Service, *testfixture.CWA, *testfixture.Zlib, []byte) {
	t.Helper()
	var input, output bytes.Buffer
	testfixture.EPUB(&input)
	testfixture.PDF(&output, 1024)
	lib := testfixture.NewCWA(output.Bytes())
	src := testfixture.NewZlib(domain.Book{Title: "合成测试书", Language: "zh", Formats: []string{"epub"}}, input.Bytes())
	t.Cleanup(lib.Server.Close)
	t.Cleanup(src.Server.Close)
	state, stage := t.TempDir(), t.TempDir()
	session := state + "/session.json"
	os.WriteFile(session, []byte(`{"cookies":{"remix_userid":"synthetic","remix_userkey":"synthetic"}}`), 0600)
	db, e := store.Open(state)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	fm, e := files.New(stage, 16<<20)
	if e != nil {
		t.Fatal(e)
	}
	c := config.Config{StateDir: state, StagingDir: stage, PublicURL: "http://localhost", CWABase: lib.Server.URL, CWAUsername: "synthetic", CWAPassword: "synthetic", CWAExpected: "pdf", CWAWrite: true, CWAInput: []string{"epub", "pdf"}, CWAOutput: []string{"epub", "pdf"}, MaxFile: 4 << 20, MaxStaging: 16 << 20, TransferTimeout: time.Second * 3, ConfirmWindow: time.Second * 10, TicketKey: []byte("synthetic-ticket-key-32-bytes-012345"), MCPToken: []byte("synthetic-mcp-token-32-bytes-012345")}
	lc := cwa.New(lib.Server.URL, "synthetic", "synthetic")
	zc := zlib.New(src.Server.URL, session, "eapi", []string{src.Server.URL})
	t.Cleanup(lc.Close)
	t.Cleanup(zc.Close)
	return New(c, db, fm, lc, zc), lib, src, input.Bytes()
}
func runSteps(t *testing.T, s *Service, id string, max int) domain.Task {
	t.Helper()
	for i := 0; i < max; i++ {
		v, e := s.Store.Get(id)
		if e != nil {
			t.Fatal(e)
		}
		if domain.Terminal(v.State) || v.State == "needs_review" || v.State == "blocked" {
			return v
		}
		if e = s.step(context.Background(), v); e != nil {
			s.failure(v, e)
		}
	}
	v, e := s.Store.Get(id)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func prepare(t *testing.T, s *Service, data []byte) UploadResult {
	t.Helper()
	raw, e := s.Upload(UploadInput{Action: "prepare", RequestID: domain.ID(), Title: "合成测试书", Language: "zh", Filename: "synthetic.epub", Size: int64(len(data))})
	if e != nil {
		t.Fatal(e)
	}
	var r UploadResult
	json.Unmarshal(raw, &r)
	return r
}
func TestLocalUploadVerifiesCWAOutputAndCleanup(t *testing.T) {
	s, lib, _, data := setup(t)
	r := prepare(t, s, data)
	task, e := s.Receive(context.Background(), r.ID, bytes.NewReader(data), int64(len(data)))
	if e != nil || task.State != "queued" {
		t.Fatal(task, e)
	}
	task = runSteps(t, s, r.ID, 8)
	if task.State != "ready" || task.CWA == nil || task.CWA.ID != "42" || task.RequiredFormat != "pdf" || task.VerificationBasis != "matched_after_submission" || lib.Uploads.Load() != 1 {
		t.Fatal(task, lib.Uploads.Load())
	}
	if _, e = os.Stat(s.Files.Path(task.ID, "input")); !os.IsNotExist(e) {
		t.Fatal("staged book survived ready", e)
	}
	if lib.Downloads.Load() != 1 {
		t.Fatal("repeated file validation", lib.Downloads.Load())
	}
}
func TestSourceAcquireAndExactDedup(t *testing.T) {
	s, lib, src, _ := setup(t)
	r, e := s.Catalog.Search(context.Background(), catalog.Input{Query: domain.Query{Query: "合成测试书"}})
	if e != nil || len(r.Candidates) != 1 {
		t.Fatal(r, e)
	}
	a := AcquireInput{Action: "start", RequestID: "source-1", CandidateID: r.Candidates[0].CandidateID}
	raw, e := s.Acquire(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	var first TaskResult
	json.Unmarshal(raw, &first)
	a.RequestID = "source-2"
	raw, e = s.Acquire(context.Background(), a)
	var second TaskResult
	json.Unmarshal(raw, &second)
	if e != nil || second.ID != first.ID {
		t.Fatal(first, second, e)
	}
	task := runSteps(t, s, first.ID, 10)
	if task.State != "ready" || lib.Uploads.Load() != 1 || src.Downloads.Load() != 1 {
		t.Fatal(task, lib.Uploads.Load(), src.Downloads.Load())
	}
}
func TestUnknownSubmissionRecoveryNeverReuploads(t *testing.T) {
	s, lib, _, data := setup(t)
	lib.Behavior = "unknown"
	r := prepare(t, s, data)
	s.Receive(context.Background(), r.ID, bytes.NewReader(data), int64(len(data)))
	v, _ := s.Store.Get(r.ID)
	if e := s.step(context.Background(), v); e != nil {
		t.Fatal(e)
	}
	v, _ = s.Store.Get(r.ID)
	if v.State != "uncertain" || !v.SubmissionAttempted {
		t.Fatal(v)
	}
	deadline := v.ReconcileDeadline
	s.Store.Change(v.ID, func(t *domain.Task) error { t.State = "submitting"; return nil })
	if e := s.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	v, _ = s.Store.Get(r.ID)
	if v.State != "reconciling" || !v.ReconcileDeadline.Equal(deadline) {
		t.Fatal(v)
	}
	v = runSteps(t, s, r.ID, 6)
	if v.State != "ready" || lib.Uploads.Load() != 1 {
		t.Fatal(v, lib.Uploads.Load())
	}
}
func TestHTTP200WithoutImportAndAmbiguousMatches(t *testing.T) {
	s, lib, _, data := setup(t)
	lib.Behavior = "refuse200"
	r := prepare(t, s, data)
	s.Receive(context.Background(), r.ID, bytes.NewReader(data), int64(len(data)))
	v, _ := s.Store.Get(r.ID)
	if e := s.step(context.Background(), v); e != nil {
		t.Fatal(e)
	}
	s.Store.Change(r.ID, func(t *domain.Task) error { t.ReconcileDeadline = time.Now().Add(-time.Second); return nil })
	v = runSteps(t, s, r.ID, 4)
	if v.State != "needs_review" || v.Error.Code != "IMPORT_UNCONFIRMED" {
		t.Fatal(v)
	}
	for _, id := range []string{"10", "11"} {
		lib.Add(domain.Book{ID: id, Title: "合成测试书", Language: "zh", Formats: []string{"pdf"}})
	}
	raw, e := s.Acquire(context.Background(), AcquireInput{Action: "retry", RequestID: "retry", TaskID: r.ID})
	if e != nil || len(raw) == 0 {
		t.Fatal(e)
	}
	v = runSteps(t, s, r.ID, 4)
	if v.State != "needs_review" || v.Error.Code != "AMBIGUOUS_CWA_MATCH" || lib.Uploads.Load() != 1 {
		t.Fatal(v)
	}
	_, e = s.Acquire(context.Background(), AcquireInput{Action: "resolve", RequestID: "resolve", TaskID: r.ID, CWABookID: "10"})
	if e != nil {
		t.Fatal(e)
	}
	v = runSteps(t, s, r.ID, 4)
	if v.State != "ready" || v.VerificationBasis != "user_resolved" {
		t.Fatal(v)
	}
}
func TestAtomicFileAndInterruptedUploadRecovery(t *testing.T) {
	s, _, _, data := setup(t)
	r := prepare(t, s, data)
	if _, e := s.Files.Receive(context.Background(), r.ID, bytes.NewReader(data), int64(len(data)), s.Config.MaxFile, "epub", "", nil); e != nil {
		t.Fatal(e)
	}
	if e := s.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	v, _ := s.Store.Get(r.ID)
	if v.State != "queued" || !v.InputComplete {
		t.Fatal(v)
	}
	r2 := prepare(t, s, data)
	s.Files.MakeDir(r2.ID)
	os.WriteFile(s.Files.Path(r2.ID, "input.part"), data[:32], 0600)
	if e := s.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	v, _ = s.Store.Get(r2.ID)
	if v.State != "awaiting_upload" {
		t.Fatal(v)
	}
	if _, e := os.Stat(s.Files.Path(r2.ID, "input.part")); !os.IsNotExist(e) {
		t.Fatal(e)
	}
}
func TestConcurrentPUTAndCancellation(t *testing.T) {
	s, _, _, data := setup(t)
	r := prepare(t, s, data)
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { _, e := s.Receive(context.Background(), r.ID, pr, int64(len(data))); done <- e }()
	for i := 0; i < 100; i++ {
		s.mu.Lock()
		busy := s.uploads[r.ID]
		s.mu.Unlock()
		if busy {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, e := s.Receive(context.Background(), r.ID, bytes.NewReader(data), int64(len(data))); domain.AsError(e).Code != "UPLOAD_IN_PROGRESS" {
		t.Fatal(e)
	}
	_, e := s.Acquire(context.Background(), AcquireInput{Action: "cancel", RequestID: "cancel", TaskID: r.ID})
	if e != nil {
		t.Fatal(e)
	}
	pw.CloseWithError(context.Canceled)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("upload leaked")
	}
	v, _ := s.Store.Get(r.ID)
	if v.State != "cancelled" {
		t.Fatal(v)
	}
}
