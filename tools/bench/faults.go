package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/catalog"
	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/jobs"
	"github.com/mmm1h/cwa-book-mcp/internal/server"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (b *bench) prepare(ctx context.Context, size int64) (jobs.UploadResult, error) {
	var result jobs.UploadResult
	err := b.call(ctx, "upload_book", map[string]any{"action": "prepare", "request_id": domain.ID(), "title": "合成测试书", "language": "zh", "filename": "synthetic.pdf", "size_bytes": size}, &result)
	return result, err
}

func (b *bench) cancelTask(ctx context.Context, id string) error {
	var result any
	return b.call(ctx, "acquire_book", map[string]any{"action": "cancel", "request_id": domain.ID(), "task_id": id}, &result)
}

func (b *bench) waitTask(ctx context.Context, id string, condition func(domain.Task) bool) (domain.Task, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		var task domain.Task
		if err := b.call(ctx, "get_task", map[string]any{"task_id": id}, &task); err != nil {
			return task, err
		}
		if condition(task) {
			return task, nil
		}
		if err := waitContext(ctx, 20*time.Millisecond); err != nil {
			return task, fmt.Errorf("task %s state=%s: %w", id, task.State, err)
		}
	}
}

// These scenarios use the same separate service and 128 MiB container as the
// file-size load. Abrupt restarts preserve its private state and staging volume.
func (b *bench) faults(ctx context.Context) error {
	if err := b.configureFile(1024); err != nil {
		return err
	}
	for _, crash := range []bool{false, true} {
		upload, err := b.prepare(ctx, 256<<10)
		if err != nil {
			return err
		}
		transfer, cancel := context.WithTimeout(ctx, 15*time.Second)
		reader, writer := io.Pipe()
		req, _ := http.NewRequestWithContext(transfer, "PUT", upload.URL, reader)
		req.ContentLength = upload.Size
		req.Header.Set("Content-Type", "application/octet-stream")
		done := make(chan error, 1)
		go func() {
			resp, err := http.DefaultClient.Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			done <- err
		}()
		_, err = writer.Write(make([]byte, 4096))
		if err == nil {
			_, err = b.waitTask(ctx, upload.ID, func(t domain.Task) bool { return t.Received > 0 })
		}
		if err == nil && crash {
			err = b.restart(ctx)
			// The deliberately stalled client reader must also be stopped when
			// its old server disappears; it cannot produce the remaining bytes.
			cancel()
			writer.CloseWithError(context.Canceled)
		} else if err == nil {
			err = b.cancelTask(ctx, upload.ID)
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			if err == nil {
				err = fmt.Errorf("stalled upload did not stop after cancellation/restart (crash=%v)", crash)
			}
		}
		writer.Close()
		cancel()
		if err != nil {
			return err
		}
		state := "cancelled"
		key := "stalled_upload_cancelled"
		if crash {
			state, key = "awaiting_upload", "partial_upload_crash_recovered"
		}
		task, err := b.waitTask(ctx, upload.ID, func(t domain.Task) bool { return t.State == state })
		if err != nil || task.InputComplete {
			return fmt.Errorf("partial upload was accepted: %v", err)
		}
		if _, err := os.Stat(filepath.Join(b.work, "staging", upload.ID, "input.part")); !os.IsNotExist(err) {
			return fmt.Errorf("partial upload file leaked: %v", err)
		}
		if crash {
			if err = b.cancelTask(ctx, upload.ID); err != nil {
				return err
			}
		}
		b.report.FaultChecks[key] = true
	}
	if err := b.fileBackpressure(ctx); err != nil {
		return err
	}

	ids := []string{}
	for i := 0; i < 32; i++ {
		upload, err := b.prepare(ctx, 1024)
		if err != nil {
			return err
		}
		ids = append(ids, upload.ID)
	}
	result, err := b.sess.CallTool(ctx, &sdk.CallToolParams{Name: "upload_book", Arguments: map[string]any{"action": "prepare", "request_id": domain.ID(), "title": "合成测试书", "filename": "synthetic.pdf", "size_bytes": 1024}})
	if err != nil {
		return err
	}
	var failure struct {
		Error domain.Error `json:"error"`
	}
	data, _ := json.Marshal(result.StructuredContent)
	if json.Unmarshal(data, &failure) != nil || !result.IsError || failure.Error.Code != "BUSY" {
		return fmt.Errorf("33rd active task was not rejected: isError=%v structured=%s", result.IsError, data)
	}
	b.capture()
	for _, id := range ids {
		if err = b.cancelTask(ctx, id); err != nil {
			return err
		}
	}
	b.report.FaultChecks["32_task_backlog_bounded"] = true

	b.fixture.Mu.Lock()
	b.fixture.Behavior = "unknown"
	b.fixture.Mu.Unlock()
	defer func() { b.fixture.Mu.Lock(); b.fixture.Behavior = ""; b.fixture.Mu.Unlock() }()
	var candidates catalog.Result
	if err = b.call(ctx, "search_books", map[string]any{"query": "合成测试书"}, &candidates); err != nil {
		return err
	}
	if len(candidates.Candidates) == 0 {
		return fmt.Errorf("source candidate missing")
	}
	args := map[string]any{"action": "start", "request_id": domain.ID(), "candidate_id": candidates.Candidates[0].CandidateID}
	var task jobs.TaskResult
	before := b.fixture.Uploads.Load()
	if err = b.call(ctx, "acquire_book", args, &task); err != nil {
		return err
	}
	if _, err = b.waitTask(ctx, task.ID, func(t domain.Task) bool { return t.State == "uncertain" }); err != nil {
		return err
	}
	if err = b.restart(ctx); err != nil {
		return err
	}
	if _, err = b.waitTask(ctx, task.ID, func(t domain.Task) bool { return t.State == "ready" }); err != nil {
		return err
	}
	var replay jobs.TaskResult
	if err = b.call(ctx, "acquire_book", args, &replay); err != nil {
		return err
	}
	if replay.ID != task.ID || b.fixture.Uploads.Load() != before+1 {
		return fmt.Errorf("uncertain submission was repeated after restart")
	}
	b.report.FaultChecks["unknown_submission_restart_no_reupload"] = true
	return nil
}

func (b *bench) fileBackpressure(ctx context.Context) error {
	var catalogResult catalog.Result
	if err := b.call(ctx, "search_books", map[string]any{"query": "客户端下载"}, &catalogResult); err != nil {
		return err
	}
	var link server.DownloadResult
	if err := b.call(ctx, "get_download_url", map[string]any{"cwa_book_id": "1", "format": "pdf"}, &link); err != nil {
		return err
	}
	type pending struct {
		id     string
		cancel context.CancelFunc
		writer *io.PipeWriter
		done   chan error
	}
	transfers := []pending{}
	defer func() {
		for _, transfer := range transfers {
			transfer.cancel()
			transfer.writer.Close()
			<-transfer.done
		}
	}()
	for i := 0; i < 2; i++ {
		upload, err := b.prepare(ctx, 256<<10)
		if err != nil {
			return err
		}
		transfer, cancel := context.WithTimeout(ctx, 20*time.Second)
		reader, writer := io.Pipe()
		req, _ := http.NewRequestWithContext(transfer, "PUT", upload.URL, reader)
		req.ContentLength = upload.Size
		req.Header.Set("Content-Type", "application/octet-stream")
		done := make(chan error, 1)
		go func() {
			resp, err := http.DefaultClient.Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			done <- err
		}()
		transfers = append(transfers, pending{upload.ID, cancel, writer, done})
		if _, err = writer.Write(make([]byte, 4096)); err != nil {
			return err
		}
		if _, err = b.waitTask(ctx, upload.ID, func(t domain.Task) bool { return t.Received > 0 }); err != nil {
			return err
		}
	}
	for i := 0; i < 20; i++ {
		bounded, cancel := context.WithTimeout(ctx, time.Second)
		req, _ := http.NewRequestWithContext(bounded, "GET", link.URL, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			return fmt.Errorf("file request queued without backpressure: %w", err)
		}
		var body struct {
			Error domain.Error `json:"error"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		cancel()
		if err != nil || resp.StatusCode != 503 || body.Error.Code != "BUSY" {
			return fmt.Errorf("third file permit was not rejected: status=%d %v", resp.StatusCode, err)
		}
	}
	b.capture()
	for _, transfer := range transfers {
		if err := b.cancelTask(ctx, transfer.id); err != nil {
			return err
		}
	}
	b.report.FaultChecks["file_backpressure_bounded"] = true
	return nil
}

func (b *bench) restart(ctx context.Context) error {
	b.capture()
	b.sess.Close()
	b.sess = nil
	if b.container != "" {
		if err := exec.Command("docker", "kill", b.container).Run(); err != nil {
			return err
		}
		data, err := exec.Command("docker", "inspect", "--format", "{{.State.OOMKilled}}", b.container).Output()
		if err != nil {
			return err
		}
		b.report.OOMKilled = b.report.OOMKilled || strings.TrimSpace(string(data)) == "true"
	} else {
		if err := b.app.Process.Kill(); err != nil {
			return err
		}
	}
	select {
	case <-b.wait:
	case <-time.After(5 * time.Second):
		return fmt.Errorf("old process did not exit")
	}
	env := b.app.Env
	if b.container != "" {
		b.app = exec.Command("docker", "start", "-a", b.container)
	} else {
		b.app = exec.Command(b.binary)
		b.app.Env = env
	}
	b.app.Stdout, b.app.Stderr = b.log, b.log
	b.wait = make(chan error, 1)
	if err := b.app.Start(); err != nil {
		return err
	}
	go func() { b.wait <- b.app.Wait() }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get(b.base + "/readyz")
		if resp != nil {
			resp.Body.Close()
		}
		if err == nil && resp.StatusCode == 200 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("restarted service not ready")
		}
		if err := waitContext(ctx, 100*time.Millisecond); err != nil {
			return err
		}
	}
	pid := b.app.Process.Pid
	if b.container != "" {
		data, err := exec.Command("docker", "inspect", "--format", "{{.State.Pid}}", b.container).Output()
		if err != nil {
			return err
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return err
		}
	}
	b.mu.Lock()
	b.pid = pid
	b.mu.Unlock()
	return b.connect(ctx)
}
