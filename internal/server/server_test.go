package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/catalog"
	"github.com/mmm1h/cwa-book-mcp/internal/config"
	"github.com/mmm1h/cwa-book-mcp/internal/cwa"
	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/files"
	"github.com/mmm1h/cwa-book-mcp/internal/jobs"
	zlib "github.com/mmm1h/cwa-book-mcp/internal/source/zlib"
	"github.com/mmm1h/cwa-book-mcp/internal/store"
	"github.com/mmm1h/cwa-book-mcp/internal/testfixture"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type authTransport struct{ token string }

func (t authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}
func setup(t *testing.T) (*App, *testfixture.CWA, *testfixture.Zlib, []byte) {
	t.Helper()
	var in, out bytes.Buffer
	testfixture.EPUB(&in)
	testfixture.PDF(&out, 1024)
	lib := testfixture.NewCWA(out.Bytes())
	src := testfixture.NewZlib(domain.Book{Title: "合成测试书", Language: "zh", Formats: []string{"epub"}}, in.Bytes())
	t.Cleanup(lib.Server.Close)
	t.Cleanup(src.Server.Close)
	dir := t.TempDir()
	session := dir + "/session.json"
	os.WriteFile(session, []byte(`{"cookies":{"remix_userid":"synthetic","remix_userkey":"synthetic"}}`), 0600)
	db, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	fm, e := files.New(t.TempDir(), 16<<20)
	if e != nil {
		t.Fatal(e)
	}
	c := config.Config{PublicURL: "http://localhost", CWAExpected: "pdf", CWAWrite: true, CWAInput: []string{"epub", "pdf"}, CWAOutput: []string{"epub", "pdf"}, MaxFile: 4 << 20, MaxStaging: 16 << 20, TransferTimeout: 3 * time.Second, ConfirmWindow: 10 * time.Second, TicketKey: []byte("synthetic-ticket-key-32-bytes-012345"), MCPToken: []byte("synthetic-mcp-token-32-bytes-012345")}
	lc := cwa.New(lib.Server.URL, "synthetic", "synthetic")
	zc := zlib.New(src.Server.URL, session, "eapi", []string{src.Server.URL})
	t.Cleanup(lc.Close)
	t.Cleanup(zc.Close)
	s := jobs.New(c, db, fm, lc, zc)
	return New(s), lib, src, in.Bytes()
}
func call(t *testing.T, s *sdk.ClientSession, name string, args any, out any) {
	t.Helper()
	r, e := s.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if e != nil || r.IsError {
		t.Fatalf("%s: result=%+v error=%v", name, r, e)
	}
	b, e := json.Marshal(r.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, out); e != nil {
		t.Fatalf("%s %s %v", name, b, e)
	}
}
func connect(t *testing.T, url, token string) *sdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := sdk.NewClient(&sdk.Implementation{Name: "synthetic-agent", Version: "test"}, nil)
	s, e := c.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: url + "/mcp", HTTPClient: &http.Client{Transport: authTransport{token}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestFiveToolsAndOfflineBackgroundDelivery(t *testing.T) {
	a, lib, src, _ := setup(t)
	ts := httptest.NewServer(a.Handler("test"))
	defer ts.Close()
	a.Jobs.Config.PublicURL = ts.URL
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := make(chan error, 1)
	go func() { worker <- a.Jobs.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case e := <-worker:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(3 * time.Second):
			t.Error("worker leaked")
		}
	}()
	first := connect(t, ts.URL, string(a.Jobs.Config.MCPToken))
	tools, e := first.ListTools(context.Background(), nil)
	if e != nil || len(tools.Tools) != 5 {
		t.Fatal(tools, e)
	}
	var search struct {
		Candidates []domain.Book `json:"candidates"`
	}
	call(t, first, "search_books", map[string]any{"query": "合成测试书"}, &search)
	if len(search.Candidates) != 1 {
		t.Fatal(search)
	}
	var acquired jobs.TaskResult
	args := map[string]any{"action": "start", "request_id": "synthetic-acquire", "candidate_id": search.Candidates[0].CandidateID}
	call(t, first, "acquire_book", args, &acquired)
	first.Close()
	second := connect(t, ts.URL, string(a.Jobs.Config.MCPToken))
	deadline := time.Now().Add(5 * time.Second)
	var task domain.Task
	for {
		call(t, second, "get_task", map[string]any{"task_id": acquired.ID}, &task)
		if task.State == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(task)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lib.Uploads.Load() != 1 || src.Downloads.Load() != 1 {
		t.Fatal("external side effect count", lib.Uploads.Load(), src.Downloads.Load())
	}
	var replay jobs.TaskResult
	call(t, second, "acquire_book", args, &replay)
	if replay.ID != acquired.ID {
		t.Fatal(replay)
	}
	var download DownloadResult
	call(t, second, "get_download_url", map[string]any{"cwa_book_id": task.CWA.ID, "format": "pdf", "task_id": task.ID}, &download)
	resp, e := http.Get(download.URL)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || resp.StatusCode != 200 || !bytes.Equal(data, lib.Output) || resp.Header.Get("Set-Cookie") != "" {
		t.Fatal(resp.StatusCode, e)
	}
	r, _ := http.NewRequest("GET", download.URL, nil)
	r.Header.Set("Range", "bytes=0-9")
	resp, e = http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 416 {
		t.Fatal(resp.StatusCode)
	}
}
func TestUploadHTTPAuthReplayAndErrors(t *testing.T) {
	a, lib, _, data := setup(t)
	ts := httptest.NewServer(a.Handler("test"))
	defer ts.Close()
	a.Jobs.Config.PublicURL = ts.URL
	resp, e := http.Post(ts.URL+"/mcp", "application/json", strings.NewReader(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	s := connect(t, ts.URL, string(a.Jobs.Config.MCPToken))
	args := map[string]any{"action": "prepare", "request_id": "synthetic-prepare", "title": "合成测试书", "filename": "synthetic.epub", "size_bytes": len(data)}
	var ticket jobs.UploadResult
	call(t, s, "upload_book", args, &ticket)
	send := func(url string, data []byte) int {
		r, _ := http.NewRequest("PUT", url, bytes.NewReader(data))
		r.Header.Set("Content-Type", "application/octet-stream")
		resp, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	if status := send(ticket.URL, data[:32]); status != 400 {
		t.Fatal(status)
	}
	if status := send(ticket.URL, data); status != 202 {
		t.Fatal(status)
	}
	if status := send(ticket.URL, data); status != 202 {
		t.Fatal("completed PUT not idempotent", status)
	}
	if lib.Uploads.Load() != 0 {
		t.Fatal("upload HTTP directly submitted")
	}
	var replay jobs.UploadResult
	call(t, s, "upload_book", args, &replay)
	if replay.Ticket != ticket.Ticket {
		t.Fatal("prepare replay refreshed ticket")
	}
	if status := send(ticket.URL+"invalid", data); status != 401 {
		t.Fatal(status)
	}
	bad, e := s.CallTool(context.Background(), &sdk.CallToolParams{Name: "upload_book", Arguments: map[string]any{"action": "prepare", "request_id": "missing-title", "filename": "book.pdf", "size_bytes": 1024}})
	if e != nil || !bad.IsError {
		t.Fatal(bad, e)
	}
	var business struct {
		Error domain.Error `json:"error"`
	}
	resultJSON, e := json.Marshal(bad.StructuredContent)
	if e != nil || json.Unmarshal(resultJSON, &business) != nil || business.Error.Code != "INVALID_ARGUMENT" {
		t.Fatalf("business error was lost from structuredContent: %s %v", resultJSON, e)
	}
}

func TestStalledHTTPUploadCancellation(t *testing.T) {
	a, lib, _, _ := setup(t)
	ts := httptest.NewServer(a.Handler("test"))
	defer ts.Close()
	a.Jobs.Config.PublicURL = ts.URL
	client := connect(t, ts.URL, string(a.Jobs.Config.MCPToken))
	var upload jobs.UploadResult
	call(t, client, "upload_book", map[string]any{"action": "prepare", "request_id": "stalled", "title": "合成测试书", "filename": "synthetic.pdf", "size_bytes": 32768}, &upload)
	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "PUT %s HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/octet-stream\r\nContent-Length: 32768\r\n\r\n", strings.TrimPrefix(upload.URL, ts.URL))
	if _, err = conn.Write(make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		task, err := a.Jobs.Store.Get(upload.ID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Received > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("HTTP upload did not start")
		}
		time.Sleep(time.Millisecond)
	}
	var cancelled jobs.TaskResult
	call(t, client, "acquire_book", map[string]any{"action": "cancel", "request_id": "stop", "task_id": upload.ID}, &cancelled)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "PUT"})
	if err != nil {
		t.Fatal("stalled HTTP reader was not interrupted", err)
	}
	resp.Body.Close()
	if cancelled.State != "cancelled" || resp.StatusCode == 202 || lib.Uploads.Load() != 0 {
		t.Fatal(cancelled, resp.StatusCode)
	}
}
func TestDownloadRejectsHTMLAndExpiredTickets(t *testing.T) {
	a, lib, _, _ := setup(t)
	lib.Add(domain.Book{ID: "1", Title: "合成测试书", Language: "zh", Formats: []string{"pdf"}})
	if _, e := a.Search(context.Background(), catalog.Input{Query: domain.Query{Query: "合成测试书"}}); e != nil {
		t.Fatal(e)
	}
	lib.FileHTML = true
	if _, e := a.DownloadURL(context.Background(), "1", "pdf", ""); domain.AsError(e).Code != "CWA_FILE_INVALID" {
		t.Fatal(e)
	}
	ts := httptest.NewServer(a.Handler("test"))
	defer ts.Close()
	ticket := files.Sign(a.Jobs.Config.TicketKey, files.Ticket{Purpose: "download", ID: "1", Format: "pdf", Expires: time.Now().Add(-time.Second).Unix()})
	resp, e := http.Get(ts.URL + "/files/downloads/" + ticket)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
}

func TestDownloadTicketSurvivesCandidateCacheEviction(t *testing.T) {
	a, lib, _, _ := setup(t)
	lib.Add(domain.Book{ID: "1", Title: "合成测试书", Language: "zh", Formats: []string{"pdf"}})
	ts := httptest.NewServer(a.Handler("test"))
	defer ts.Close()
	a.Jobs.Config.PublicURL = ts.URL
	if _, err := a.Search(context.Background(), catalog.Input{Query: domain.Query{Query: "合成测试书"}}); err != nil {
		t.Fatal(err)
	}
	result, err := a.DownloadURL(context.Background(), "1", "pdf", "")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		book := domain.Book{Origin: "cwa", ID: fmt.Sprint(10000 + i), Title: "合成缓存记录"}
		if err := a.Jobs.Store.PutCache("candidates", domain.ID(), book, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Jobs.Store.KnownCWA("1"); err == nil {
		t.Fatal("eviction did not happen")
	}
	resp, err := http.Get(result.(DownloadResult).URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || !bytes.Equal(data, lib.Output) {
		t.Fatal("issued ticket depended on evicted cache", resp.StatusCode, err)
	}
}
