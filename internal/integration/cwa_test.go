//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/config"
	"github.com/mmm1h/cwa-book-mcp/internal/cwa"
	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/files"
	"github.com/mmm1h/cwa-book-mcp/internal/jobs"
	zlib "github.com/mmm1h/cwa-book-mcp/internal/source/zlib"
	"github.com/mmm1h/cwa-book-mcp/internal/store"
	"github.com/mmm1h/cwa-book-mcp/internal/testfixture"
)

// The operator must supply an isolated original CWA with standard settings.
// This test never changes CWA settings, files, scripts or its database.
func TestOriginalCWAUploadConvertRead(t *testing.T) {
	if os.Getenv("CWA_INTEGRATION") != "1" {
		t.Skip("explicit isolated CWA opt-in required")
	}
	base := os.Getenv("CWA_BASE_URL")
	user := os.Getenv("CWA_USERNAME")
	if base == "" || user == "" {
		t.Fatal("CWA_BASE_URL/CWA_USERNAME required")
	}
	pw, e := os.ReadFile(os.Getenv("CWA_PASSWORD_FILE"))
	if e != nil {
		t.Fatal("CWA_PASSWORD_FILE required")
	}
	lib := cwa.New(base, user, strings.TrimSpace(string(pw)))
	defer lib.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	defer cancel()
	ready := time.Now().Add(2 * time.Minute)
	for {
		_, e = lib.Search(ctx, domain.Query{Query: "CWA compatibility readiness"}, "")
		if e == nil {
			break
		}
		if time.Now().After(ready) {
			t.Fatal(e)
		}
		time.Sleep(time.Second)
	}
	db, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	fm, e := files.New(t.TempDir(), 256<<20)
	if e != nil {
		t.Fatal(e)
	}
	source := zlib.New("", "", "eapi", nil)
	defer source.Close()
	c := config.Config{PublicURL: "http://localhost", CWAExpected: "epub", CWAWrite: true, CWAInput: []string{"epub", "pdf"}, CWAOutput: []string{"epub"}, MaxFile: 64 << 20, MaxStaging: 256 << 20, TransferTimeout: 2 * time.Minute, ConfirmWindow: 6 * time.Minute, TicketKey: []byte("synthetic-test-ticket-key-32-bytes")}
	s := jobs.New(c, db, fm, lib, source)
	if e = s.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	worker := make(chan error, 1)
	go func() { worker <- s.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case e := <-worker:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(5 * time.Second):
			t.Error("worker shutdown timed out")
		}
	}()
	for _, format := range []string{"epub", "pdf"} {
		t.Run(format, func(t *testing.T) {
			var data bytes.Buffer
			title := "合成测试书"
			if format == "epub" {
				e = testfixture.EPUB(&data)
			} else {
				title = "CWA Synthetic PDF Compatibility"
				e = testfixture.PDFTitle(&data, 2048, title)
			}
			if e != nil {
				t.Fatal(e)
			}
			raw, e := s.Upload(jobs.UploadInput{Action: "prepare", RequestID: domain.ID(), Title: title, Filename: "synthetic." + format, Size: int64(data.Len()), RequiredFormat: "epub"})
			if e != nil {
				t.Fatal(e)
			}
			var prepared jobs.UploadResult
			json.Unmarshal(raw, &prepared)
			if _, e = s.Receive(ctx, prepared.ID, bytes.NewReader(data.Bytes()), int64(data.Len())); e != nil {
				t.Fatal(e)
			}
			deadline := time.Now().Add(6 * time.Minute)
			var task domain.Task
			for {
				task, e = db.Get(prepared.ID)
				if e != nil {
					t.Fatal(e)
				}
				if task.State == "ready" {
					break
				}
				if task.State == "blocked" || task.State == "failed" || task.State == "needs_review" || time.Now().After(deadline) {
					t.Fatalf("original CWA did not verify: state=%s error=%v candidates=%+v", task.State, task.Error, task.Candidates)
				}
				time.Sleep(200 * time.Millisecond)
			}
			if task.VerificationBasis != "matched_after_submission" {
				t.Fatalf("test expected actual new upload, got %s", task.VerificationBasis)
			}
			resp, e := lib.Open(ctx, *task.CWA, "epub")
			if e != nil {
				t.Fatal(e)
			}
			path := filepath.Join(t.TempDir(), "original-output.epub")
			f, e := os.Create(path)
			if e != nil {
				t.Fatal(e)
			}
			_, e = io.CopyBuffer(f, resp.Body, make([]byte, 64<<10))
			f.Close()
			resp.Body.Close()
			if e != nil {
				t.Fatal(e)
			}
			if e = files.Validate(ctx, path, "epub", c.MaxFile); e != nil {
				t.Fatal(e)
			}
			os.Remove(path)
			t.Logf("input=%s output=epub CWA_id=%s basis=%s", format, task.CWA.ID, task.VerificationBasis)
		})
	}
}
