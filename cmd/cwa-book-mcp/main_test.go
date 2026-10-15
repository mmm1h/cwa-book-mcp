package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/jobs"
	"github.com/mmm1h/cwa-book-mcp/internal/server"
	"github.com/mmm1h/cwa-book-mcp/internal/store"
	"github.com/mmm1h/cwa-book-mcp/internal/testfixture"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The child executes the production entry point over real stdin/stdout pipes.
func TestServiceProcess(t *testing.T) {
	if os.Getenv("CWA_TEST_STDIO_PROCESS") != "1" {
		return
	}
	if e := run([]string{"--transport=stdio"}); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	os.Exit(0)
}

func childCommand(t *testing.T, libURL, base, dir string) *exec.Cmd {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(exe, "-test.run=^TestServiceProcess$")
	settings := map[string]string{
		"CWA_TEST_STDIO_PROCESS": "1", "MCP_TRANSPORT": "http",
		"LISTEN_ADDR": strings.TrimPrefix(base, "http://"), "PUBLIC_BASE_URL": base,
		"STATE_DIR": filepath.Join(dir, "state"), "STAGING_DIR": filepath.Join(dir, "staging"),
		"CWA_BASE_URL": libURL, "CWA_USERNAME": "synthetic", "CWA_PASSWORD": "synthetic", "CWA_PASSWORD_FILE": "",
		"MCP_TOKEN": "", "MCP_TOKEN_FILE": "", "TICKET_KEY": "", "TICKET_KEY_FILE": "",
		"CWA_WRITE_ENABLED": "true", "CWA_EXPECTED_FORMAT": "pdf", "CWA_INPUT_FORMATS": "epub,pdf", "CWA_OUTPUT_FORMATS": "epub,pdf",
		"CWA_CONFIRM_WINDOW": "10s", "MAX_FILE_BYTES": "1048576", "MAX_STAGING_BYTES": "4194304", "CWA_MAX_UPLOAD_BYTES": "1114112",
		"ZLIB_DOMAIN": "", "ZLIB_SESSION_FILE": "", "ZLIB_MODE": "eapi", "ZLIB_FILE_ORIGINS": "", "CWA_DEBUG_METRICS_FILE": "",
	}
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if _, ok := settings[strings.ToUpper(key)]; !ok {
			cmd.Env = append(cmd.Env, v)
		}
	}
	for key, value := range settings {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd
}

func freeBase(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	base := "http://" + l.Addr().String()
	l.Close()
	return base
}

func stdioClient(t *testing.T, cmd *exec.Cmd) (*sdk.ClientSession, func()) {
	t.Helper()
	var logs bytes.Buffer
	cmd.Stderr = &logs
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := sdk.NewClient(&sdk.Implementation{Name: "synthetic-mcphub", Version: "test"}, nil)
	sess, e := c.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if e != nil {
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		t.Fatalf("stdio initialize: %v; stderr: %s", e, &logs)
	}
	closed := false
	close := func() {
		if closed {
			return
		}
		closed = true
		if e := sess.Close(); e != nil {
			t.Errorf("closing stdio: %v; stderr: %s", e, &logs)
		}
		if cmd.ProcessState == nil || !cmd.ProcessState.Success() {
			t.Errorf("child did not exit cleanly after stdin EOF: %s", &logs)
		}
	}
	t.Cleanup(close)
	return sess, close
}

func tool(t *testing.T, sess *sdk.ClientSession, name string, args, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, e := sess.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if e != nil || r.IsError {
		t.Fatalf("%s: result=%+v error=%v", name, r, e)
	}
	b, e := json.Marshal(r.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, out); e != nil {
		t.Fatal(e)
	}
}

func TestStdioCommandLifecycleAndFiles(t *testing.T) {
	var input, output bytes.Buffer
	testfixture.EPUB(&input)
	testfixture.PDF(&output, 1024)
	lib := testfixture.NewCWA(output.Bytes())
	defer lib.Server.Close()
	lib.Add(domain.Book{ID: "7", Title: "已有合成书", Language: "zh", Formats: []string{"pdf"}})
	dir := t.TempDir()
	base := freeBase(t)
	first, closeFirst := stdioClient(t, childCommand(t, lib.Server.URL, base, dir))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	list, e := first.ListTools(ctx, nil)
	if e != nil || len(list.Tools) != 5 {
		t.Fatal(list, e)
	}
	var found struct {
		Candidates []domain.Book `json:"candidates"`
	}
	tool(t, first, "search_books", map[string]any{"query": "已有合成书"}, &found)
	if len(found.Candidates) != 1 {
		t.Fatal(found)
	}
	var link server.DownloadResult
	tool(t, first, "get_download_url", map[string]any{"cwa_book_id": found.Candidates[0].ID, "format": "pdf"}, &link)
	var upload jobs.UploadResult
	tool(t, first, "upload_book", map[string]any{"action": "prepare", "request_id": "stdio-upload", "title": "合成测试书", "language": "zh", "filename": "synthetic.epub", "size_bytes": input.Len()}, &upload)
	closeFirst()

	// MCPHub restarts the single shared process. Both task and issued tickets
	// remain valid, while the old process releases its DB and file listener.
	second, closeSecond := stdioClient(t, childCommand(t, lib.Server.URL, base, dir))
	var task domain.Task
	tool(t, second, "get_task", map[string]any{"task_id": upload.ID}, &task)
	if task.State != "awaiting_upload" {
		t.Fatal(task.State)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, e := client.Get(link.URL)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || resp.StatusCode != 200 || sha256.Sum256(data) != sha256.Sum256(output.Bytes()) {
		t.Fatal("pre-restart download ticket failed", resp.StatusCode, e)
	}
	resp, e = client.Get(base + "/mcp")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("stdio unexpectedly exposes HTTP MCP", resp.StatusCode)
	}
	req, e := http.NewRequest("PUT", upload.URL, bytes.NewReader(input.Bytes()))
	if e != nil {
		t.Fatal(e)
	}
	req.Header.Set("Content-Type", upload.Headers["Content-Type"])
	resp, e = client.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatal("pre-restart upload ticket failed", resp.StatusCode)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		tool(t, second, "get_task", map[string]any{"task_id": upload.ID}, &task)
		if task.State == "ready" {
			break
		}
		if time.Now().After(deadline) || task.State == "failed" || task.State == "blocked" {
			t.Fatalf("upload failed to become ready: %+v", task)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lib.Uploads.Load() != 1 || task.CWA == nil || task.RequiredFormat != "pdf" {
		t.Fatal("CWA submission or verification failed", lib.Uploads.Load(), task)
	}
	tool(t, second, "get_download_url", map[string]any{"cwa_book_id": task.CWA.ID, "task_id": task.ID, "format": "pdf"}, &link)
	var waiting jobs.UploadResult
	tool(t, second, "upload_book", map[string]any{"action": "prepare", "request_id": "stdio-cancel-upload", "title": "待取消合成书", "filename": "synthetic.epub", "size_bytes": input.Len()}, &waiting)
	var cancelled jobs.TaskResult
	tool(t, second, "acquire_book", map[string]any{"action": "cancel", "request_id": "stdio-cancel", "task_id": waiting.ID}, &cancelled)
	if cancelled.State != "cancelled" {
		t.Fatal(cancelled)
	}
	closeSecond()
	db, e := store.Open(filepath.Join(dir, "state"))
	if e != nil {
		t.Fatal("stdio EOF left database locked", e)
	}
	db.Close()
	if conn, e := net.DialTimeout("tcp", strings.TrimPrefix(base, "http://"), 100*time.Millisecond); e == nil {
		conn.Close()
		t.Fatal("stdio EOF left file listener running")
	}
	t.Log("command/args/env: initialize, five tools, file PUT/GET, persistent tasks/tickets and stdin EOF passed")
}

func TestStdioRejectsOversizedFrameAndBusyPort(t *testing.T) {
	for _, test := range []string{"oversized", "busy-port"} {
		t.Run(test, func(t *testing.T) {
			base := freeBase(t)
			if test == "busy-port" {
				l, e := net.Listen("tcp", strings.TrimPrefix(base, "http://"))
				if e != nil {
					t.Fatal(e)
				}
				defer l.Close()
			}
			cmd := childCommand(t, "http://localhost:8083", base, t.TempDir())
			cmd.Stdin = strings.NewReader(strings.Repeat("x", (256<<10)+1) + "\n")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			cmd.WaitDelay = 3 * time.Second
			done := make(chan error, 1)
			go func() { done <- cmd.Run() }()
			select {
			case e := <-done:
				if e == nil || stdout.Len() != 0 {
					t.Fatalf("invalid startup/input accepted or stdout polluted: %v %s", e, &stdout)
				}
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Fatal("invalid startup/input did not terminate")
			}
		})
	}
}
