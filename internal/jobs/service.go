package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/catalog"
	"github.com/mmm1h/cwa-book-mcp/internal/config"
	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/files"
	"github.com/mmm1h/cwa-book-mcp/internal/store"
)

type Library interface {
	Search(context.Context, domain.Query, string) (domain.Page, error)
	Book(context.Context, domain.Book) (domain.Book, error)
	Open(context.Context, domain.Book, string) (*http.Response, error)
	PrepareUpload(context.Context) (string, error)
	Upload(context.Context, string, string, string) (bool, error)
}
type Source interface {
	catalog.Searcher
	Open(context.Context, domain.Book) (*http.Response, error)
}
type Service struct {
	Config    config.Config
	Store     *store.Store
	Files     *files.Manager
	CWA       Library
	Source    Source
	Catalog   *catalog.Catalog
	mu        sync.Mutex
	transfers map[string]context.CancelFunc
	uploads   map[string]bool
	wake      chan struct{}
}

func New(c config.Config, s *store.Store, f *files.Manager, lib Library, source Source) *Service {
	return &Service{Config: c, Store: s, Files: f, CWA: lib, Source: source, Catalog: &catalog.Catalog{CWA: lib, Source: source, Store: s}, transfers: map[string]context.CancelFunc{}, uploads: map[string]bool{}, wake: make(chan struct{}, 1)}
}
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

type AcquireInput struct {
	Action         string `json:"action"`
	RequestID      string `json:"request_id"`
	CandidateID    string `json:"candidate_id,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	CWABookID      string `json:"cwa_book_id,omitempty"`
	RequiredFormat string `json:"required_format,omitempty"`
}
type UploadInput struct {
	Action         string `json:"action"`
	RequestID      string `json:"request_id"`
	TaskID         string `json:"task_id,omitempty"`
	Title          string `json:"title,omitempty"`
	Filename       string `json:"filename,omitempty"`
	Size           int64  `json:"size_bytes,omitempty"`
	Author         string `json:"author,omitempty"`
	Language       string `json:"language,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	RequiredFormat string `json:"required_format,omitempty"`
}
type TaskResult struct {
	ID           string `json:"task_id"`
	State        string `json:"state"`
	Reused       bool   `json:"reused_existing"`
	MaySubmitted bool   `json:"cwa_submission_may_have_succeeded,omitempty"`
}

func taskResult(t domain.Task) any {
	return TaskResult{t.ID, t.State, t.VerificationBasis == "reused_existing", t.State == "cancelled" && t.SubmissionAttempted}
}

type UploadResult struct {
	TaskResult
	Ticket  string            `json:"upload_ticket"`
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers"`
	Expires time.Time         `json:"expires_at"`
	Size    int64             `json:"size_bytes"`
}

func (s *Service) uploadResult(t domain.Task) any {
	exp := minTime(time.Now().UTC().Add(15*time.Minute), t.Expires).Truncate(time.Second)
	ticket := files.Sign(s.Config.TicketKey, files.Ticket{Purpose: "upload", ID: t.ID, Expires: exp.Unix()})
	return UploadResult{TaskResult: taskResult(t).(TaskResult), Ticket: ticket, URL: s.Config.PublicURL + "/files/uploads/" + t.ID + "?ticket=" + ticket, Method: "PUT", Headers: map[string]string{"Content-Type": "application/octet-stream", "Content-Length": fmt.Sprint(t.Size)}, Expires: exp, Size: t.Size}
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func newTask(kind string, b domain.Book, format string) domain.Task {
	now := time.Now().UTC()
	return domain.Task{ID: domain.ID(), Kind: kind, State: "queued", Book: b, RequiredFormat: format, Created: now, Updated: now, Expires: now.Add(24 * time.Hour), NextPoll: now}
}
func requestKey(tool, action, id string) string { return tool + "\x00" + action + "\x00" + id }
func (s *Service) Acquire(ctx context.Context, a AcquireInput) (json.RawMessage, error) {
	if e := domain.RequireID(a.RequestID); e != nil {
		return nil, e
	}
	key := requestKey("acquire_book", a.Action, a.RequestID)
	hash := store.Hash(a)
	if out, ok, e := s.Store.Replay(key, hash); ok || e != nil {
		return out, e
	}
	if a.Action == "start" {
		if a.TaskID != "" || a.CWABookID != "" || a.CandidateID == "" {
			return nil, domain.E("INVALID_ARGUMENT", "start 需要 candidate_id")
		}
		b, e := s.Catalog.Candidate(a.CandidateID)
		if e != nil {
			return nil, e
		}
		f := domain.Format(a.RequiredFormat)
		if f == "" {
			f = s.Config.CWAExpected
		}
		if !domain.Supported(f) {
			return nil, domain.E("UNSUPPORTED_FORMAT", "首版仅支持 EPUB/PDF")
		}
		t := newTask("acquire", b, f)
		if b.Origin == "cwa" {
			if !domain.HasFormat(b, f) {
				return nil, domain.E("FORMAT_UNAVAILABLE", "选定 CWA 候选没有所需格式")
			}
		} else {
			if !s.Config.CWAWrite {
				return nil, domain.E("WRITES_DISABLED", "需由操作者核对 CWA 标准设置并启用 CWA_WRITE_ENABLED")
			}
			if len(b.Formats) != 1 || s.Config.CheckFormat(b.Formats[0], f) != nil {
				return nil, domain.E("UNSUPPORTED_FORMAT", "所选书源文件不在 CWA 声明格式中")
			}
			t.Filename = "book." + b.Formats[0]
			t.ExactKey = store.Hash([]string{b.Origin, b.Ref, b.Language, f})
		}
		out, e := s.Store.Create(key, hash, t, taskResult)
		if e == nil {
			s.Wake()
		}
		return out, e
	}
	if a.TaskID == "" || a.CandidateID != "" || a.RequiredFormat != "" {
		return nil, domain.E("INVALID_ARGUMENT", "该 action 需要 task_id，不能改变任务输入")
	}
	var selected domain.Book
	if a.Action == "resolve" {
		t, e := s.Store.Get(a.TaskID)
		if e != nil {
			return nil, e
		}
		if t.State != "needs_review" {
			return nil, domain.E("INVALID_ARGUMENT", "resolve 仅用于 needs_review")
		}
		var b domain.Book
		for _, candidate := range t.Candidates {
			if candidate.ID == a.CWABookID {
				b = candidate
				break
			}
		}
		if b.ID == "" {
			b, e = s.Store.RecentCWA(a.CWABookID)
			if e != nil {
				return nil, e
			}
		}
		selected, e = s.CWA.Book(ctx, b)
		if e != nil {
			return nil, e
		}
		if !domain.Match(t.Book, selected, true) || !domain.HasFormat(selected, t.RequiredFormat) {
			return nil, domain.E("INVALID_ARGUMENT", "明确语言或所需格式不满足任务条件")
		}
	}
	out, e := s.Store.Act(key, hash, a.TaskID, func(t *domain.Task) error {
		if a.Action == "cancel" {
			if !domain.Terminal(t.State) {
				t.State = "cancelled"
				t.Error = domain.E("CANCELLED", "任务已取消")
			}
			return nil
		}
		if time.Now().After(t.Expires) {
			return domain.E("TASK_EXPIRED", "任务已到期")
		}
		switch a.Action {
		case "retry":
			if t.State != "blocked" && t.State != "retry_wait" && t.State != "needs_review" && t.State != "uncertain" {
				return domain.E("INVALID_ARGUMENT", "当前状态不能 retry")
			}
			if t.SubmissionAttempted {
				t.State = "reconciling"
				t.ResumeStep = "reconciling"
				t.ReconcileDeadline = time.Now().UTC().Add(s.Config.ConfirmWindow)
			} else if t.InputComplete {
				t.State = "staged"
				t.ResumeStep = "staged"
			} else if t.Kind == "upload" {
				t.State = "awaiting_upload"
			} else {
				t.State = "queued"
			}
			t.Attempts = 0
			t.Polls = 0
			t.Error = nil
			t.NextPoll = time.Now().UTC()
		case "resolve":
			if t.State != "needs_review" {
				return domain.E("INVALID_ARGUMENT", "当前状态不能 resolve")
			}
			t.CWA = &selected
			t.State = "verifying"
			t.VerificationBasis = "user_resolved"
			t.Error = nil
			t.NextPoll = time.Now().UTC()
		default:
			return domain.E("INVALID_ARGUMENT", "action 必须为 start/retry/resolve/cancel")
		}
		return nil
	}, taskResult)
	if e == nil {
		if a.Action == "cancel" {
			s.stopTransfer(a.TaskID)
			s.cleanupIfIdle(a.TaskID)
		}
		s.Wake()
	}
	return out, e
}
func (s *Service) Upload(a UploadInput) (json.RawMessage, error) {
	if e := domain.RequireID(a.RequestID); e != nil {
		return nil, e
	}
	key := requestKey("upload_book", a.Action, a.RequestID)
	hash := store.Hash(a)
	if out, ok, e := s.Store.Replay(key, hash); ok || e != nil {
		return out, e
	}
	if a.Action == "refresh_ticket" {
		if a.Title != "" || a.Filename != "" || a.Size != 0 || a.Author != "" || a.Language != "" || a.SHA256 != "" || a.RequiredFormat != "" {
			return nil, domain.E("INVALID_ARGUMENT", "refresh_ticket 不能修改任务输入")
		}
		return s.Store.Act(key, hash, a.TaskID, func(t *domain.Task) error {
			if t.State != "awaiting_upload" || time.Now().After(t.Expires) {
				return domain.E("INVALID_ARGUMENT", "仅可刷新未到期的 awaiting_upload 票据")
			}
			return nil
		}, s.uploadResult)
	}
	if a.Action != "prepare" || a.TaskID != "" {
		return nil, domain.E("INVALID_ARGUMENT", "action 必须为 prepare/refresh_ticket")
	}
	if !s.Config.CWAWrite {
		return nil, domain.E("WRITES_DISABLED", "需由操作者核对 CWA 标准设置并启用 CWA_WRITE_ENABLED")
	}
	if strings.TrimSpace(a.Title) == "" || len(a.Title) > 1024 || len(a.Author) > 256 || len(a.Language) > 64 {
		return nil, domain.E("INVALID_ARGUMENT", "请补充有效书名；首版不从文件名猜测书名")
	}
	if e := domain.Filename(a.Filename); e != nil {
		return nil, e
	}
	if a.Size <= 0 {
		return nil, domain.E("INVALID_ARGUMENT", "size_bytes 必须为正数")
	}
	if a.Size > s.Config.MaxFile {
		return nil, domain.E("FILE_TOO_LARGE", "文件超过应用或已声明上传通路上限")
	}
	if a.SHA256 != "" {
		if b, e := hex.DecodeString(a.SHA256); e != nil || len(b) != 32 {
			return nil, domain.E("INVALID_ARGUMENT", "sha256 需为 64 位十六进制")
		}
	}
	f := domain.Format(a.RequiredFormat)
	if f == "" {
		f = s.Config.CWAExpected
	}
	if e := s.Config.CheckFormat(domain.Format(filepath.Ext(a.Filename)), f); e != nil {
		return nil, e
	}
	b := domain.Book{Title: strings.TrimSpace(a.Title), Language: domain.Language(a.Language), Formats: []string{domain.Format(filepath.Ext(a.Filename))}}
	if a.Author != "" {
		b.Authors = []string{a.Author}
	}
	t := newTask("upload", b, f)
	t.State = "awaiting_upload"
	t.Filename = a.Filename
	t.Size = a.Size
	t.SHA256 = a.SHA256
	if e := s.Files.Reserve(t.ID, a.Size); e != nil {
		return nil, e
	}
	out, e := s.Store.Create(key, hash, t, s.uploadResult)
	if e != nil {
		s.Files.Release(t.ID)
		return nil, e
	}
	var result TaskResult
	_ = json.Unmarshal(out, &result)
	if result.ID != t.ID {
		s.Files.Release(t.ID)
	}
	s.Wake()
	return out, nil
}
func (s *Service) stopTransfer(id string) {
	s.mu.Lock()
	if cancel := s.transfers[id]; cancel != nil {
		cancel()
	}
	s.mu.Unlock()
}
func (s *Service) cleanupIfIdle(id string) {
	s.mu.Lock()
	busy := s.transfers[id] != nil || s.uploads[id]
	s.mu.Unlock()
	if !busy {
		_ = s.Files.Cleanup(id)
	}
}
func (s *Service) beginTransfer(ctx context.Context, id string) (context.Context, func(), error) {
	// Cancellation commits the terminal state before taking mu. Keep the local
	// state read and registration under this lock so cancellation cannot miss a
	// transfer registered after stopTransfer has already returned.
	s.mu.Lock()
	defer s.mu.Unlock()
	t, e := s.Store.Get(id)
	if e != nil {
		return nil, nil, e
	}
	if domain.Terminal(t.State) {
		return nil, nil, errStopped
	}
	remaining := time.Until(t.Expires)
	if remaining <= 0 {
		return nil, nil, domain.E("TASK_EXPIRED", "任务已到期")
	}
	if s.transfers[id] != nil {
		return nil, nil, domain.E("UPLOAD_IN_PROGRESS", "同一任务已有文件传输")
	}
	ctx, cancel := context.WithTimeout(ctx, min(s.Config.TransferTimeout, remaining))
	s.transfers[id] = cancel
	return ctx, func() {
		cancel()
		s.mu.Lock()
		delete(s.transfers, id)
		s.mu.Unlock()
		t, e := s.Store.Get(id)
		if e == nil && domain.Terminal(t.State) {
			s.cleanupIfIdle(id)
		}
	}, nil
}
func (s *Service) Receive(ctx context.Context, id string, r io.Reader, length int64) (domain.Task, error) {
	s.mu.Lock()
	if s.uploads[id] {
		s.mu.Unlock()
		return domain.Task{}, domain.E("UPLOAD_IN_PROGRESS", "同一任务已有 PUT")
	}
	s.uploads[id] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.uploads, id)
		s.mu.Unlock()
		t, e := s.Store.Get(id)
		if e == nil && domain.Terminal(t.State) {
			s.cleanupIfIdle(id)
		}
	}()
	t, e := s.Store.Get(id)
	if e != nil {
		return t, e
	}
	if t.InputComplete && t.State != "awaiting_upload" {
		return t, nil
	}
	if t.State != "awaiting_upload" || time.Now().After(t.Expires) {
		return t, domain.E("TASK_EXPIRED", "任务已到期或不能再接收文件")
	}
	if length != t.Size {
		return t, domain.E("LENGTH_MISMATCH", "Content-Length 与 prepare 声明不符")
	}
	ctx, done, e := s.beginTransfer(ctx, id)
	if e != nil {
		return t, e
	}
	defer done()
	interrupt := context.AfterFunc(ctx, func() {
		if closer, ok := r.(io.Closer); ok {
			_ = closer.Close()
		}
	})
	defer interrupt()
	if e = s.Files.Acquire(ctx); e != nil {
		return t, e
	}
	defer s.Files.Done()
	if e = s.Files.Reserve(id, t.Size); e != nil {
		return t, e
	}
	n, e := s.Files.Receive(ctx, id, r, t.Size, s.Config.MaxFile, domain.Format(filepath.Ext(t.Filename)), t.SHA256, s.progress(id))
	if e != nil {
		_, _ = s.Store.Change(id, func(t *domain.Task) error {
			if t.State == "awaiting_upload" {
				t.Received = 0
				t.Error = domain.AsError(e)
			}
			return nil
		})
		return t, e
	}
	t, e = s.Store.Change(id, func(t *domain.Task) error {
		if t.State != "awaiting_upload" {
			return errStopped
		}
		t.State = "queued"
		t.InputComplete = true
		t.Received = n
		t.Error = nil
		t.NextPoll = time.Now().UTC()
		return nil
	})
	if e == nil {
		s.Wake()
	}
	return t, e
}
func (s *Service) progress(id string) func(int64) {
	var last int64
	var lastAt time.Time
	return func(n int64) {
		if n-last < 1<<20 && time.Since(lastAt) < time.Second {
			return
		}
		last = n
		lastAt = time.Now()
		_, _ = s.Store.Change(id, func(t *domain.Task) error {
			if domain.Terminal(t.State) {
				return errStopped
			}
			t.Received = n
			return nil
		})
	}
}

var errStopped = errors.New("task stopped")

func (s *Service) change(id string, f store.Mutation) (domain.Task, error) {
	return s.Store.Change(id, func(t *domain.Task) error {
		if domain.Terminal(t.State) {
			return errStopped
		}
		return f(t)
	})
}

// Recover examines only <=32 active records and streams orphan directory scans.
func (s *Service) Recover(ctx context.Context) error {
	tasks, e := s.Store.Active()
	if e != nil {
		return e
	}
	keep := map[string]bool{}
	for _, t := range tasks {
		keep[t.ID] = true
		s.Files.RemoveParts(t.ID)
		if time.Now().After(t.Expires) {
			_, e = s.change(t.ID, func(t *domain.Task) error {
				t.State = "cancelled"
				t.Error = domain.E("TASK_EXPIRED", "任务已到期")
				return nil
			})
			if e != nil {
				return e
			}
			_ = s.Files.Cleanup(t.ID)
			continue
		}
		path := s.Files.Path(t.ID, "input")
		st, statErr := os.Stat(path)
		complete := statErr == nil && st.Size() > 0
		if complete {
			if t.Size > 0 && t.Kind == "upload" && st.Size() != t.Size {
				complete = false
			}
			if complete {
				e = files.Validate(ctx, path, domain.Format(filepath.Ext(t.Filename)), s.Config.MaxFile)
				complete = e == nil
				if complete && t.SHA256 != "" {
					f, err := os.Open(path)
					if err != nil {
						complete = false
					} else {
						h := sha256.New()
						_, err = io.CopyBuffer(h, f, make([]byte, 64<<10))
						f.Close()
						complete = err == nil && strings.EqualFold(hex.EncodeToString(h.Sum(nil)), t.SHA256)
					}
				}
			}
			if complete {
				if e = s.Files.Reserve(t.ID, st.Size()); e != nil {
					return e
				}
			} else {
				_ = os.Remove(path)
			}
		} else if t.Kind == "upload" && !t.InputComplete {
			if e = s.Files.Reserve(t.ID, t.Size); e != nil {
				return e
			}
		}
		_, e = s.change(t.ID, func(t *domain.Task) error {
			t.InputComplete = complete
			if complete && t.State == "awaiting_upload" {
				t.State = "queued"
				t.Received = st.Size()
			}
			if t.State == "submitting" || t.State == "uncertain" || t.State == "reconciling" {
				t.SubmissionAttempted = true
				t.State = "reconciling"
				if t.ReconcileDeadline.IsZero() {
					t.ReconcileDeadline = time.Now().UTC().Add(s.Config.ConfirmWindow)
				}
			} else if t.State == "downloading" {
				t.State = "queued"
				t.Attempts++
				if t.Attempts >= 3 {
					t.State = "blocked"
					t.Error = domain.E("SOURCE_UNAVAILABLE", "中断下载已达尝试上限，请显式 retry")
				}
			} else if (t.State == "staged" || t.State == "queued") && !complete && t.Kind == "upload" && !t.SubmissionAttempted {
				t.State = "awaiting_upload"
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	return s.Files.Orphans(keep)
}
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	maintenance := time.Now()
	for {
		if ctx.Err() != nil {
			return nil
		}
		tasks, e := s.Store.Active()
		if e != nil {
			return e
		}
		now := time.Now()
		var next *domain.Task
		for i := range tasks {
			t := &tasks[i]
			if now.After(t.Expires) {
				_, _ = s.change(t.ID, func(t *domain.Task) error {
					t.State = "cancelled"
					t.Error = domain.E("TASK_EXPIRED", "任务已到期")
					return nil
				})
				s.stopTransfer(t.ID)
				s.cleanupIfIdle(t.ID)
				continue
			}
			s.mu.Lock()
			uploading := s.uploads[t.ID]
			s.mu.Unlock()
			if uploading {
				continue
			}
			switch t.State {
			case "queued", "downloading", "staged", "submitting", "uncertain", "reconciling", "verifying", "retry_wait":
				if !t.NextPoll.After(now) && (next == nil || t.NextPoll.Before(next.NextPoll)) {
					next = t
				}
			}
		}
		if now.Sub(maintenance) > 10*time.Second {
			ids, e := s.Store.Sweep(now)
			if e != nil {
				return e
			}
			for _, id := range ids {
				_ = s.Files.Cleanup(id)
			}
			maintenance = now
		}
		if next != nil {
			e = s.step(ctx, *next)
			if e != nil && ctx.Err() == nil && !errors.Is(e, errStopped) {
				s.failure(*next, e)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-s.wake:
		}
	}
}
func (s *Service) failure(t domain.Task, e error) {
	d := domain.AsError(e)
	_, _ = s.change(t.ID, func(current *domain.Task) error {
		current.Error = d
		current.ResumeStep = current.State
		if current.SubmissionAttempted {
			current.ResumeStep = "reconciling"
		}
		current.Attempts++
		if d.Retryable && current.Attempts < 3 {
			current.State = "retry_wait"
			current.NextPoll = time.Now().UTC().Add(time.Duration(1<<current.Attempts) * time.Second)
		} else if d.Code == "INPUT_MISSING" || d.Code == "CWA_FILE_INVALID" || d.Code == "UNSUPPORTED_FORMAT" || d.Code == "SOURCE_CHANGED" {
			current.State = "failed"
		} else {
			current.State = "blocked"
		}
		return nil
	})
	current, err := s.Store.Get(t.ID)
	if err == nil && domain.Terminal(current.State) {
		s.cleanupIfIdle(t.ID)
	}
}
func (s *Service) step(ctx context.Context, t domain.Task) error {
	switch t.State {
	case "retry_wait":
		_, e := s.change(t.ID, func(t *domain.Task) error {
			if t.SubmissionAttempted {
				t.State = "reconciling"
			} else {
				t.State = t.ResumeStep
				if t.State == "" {
					t.State = "queued"
				}
			}
			t.NextPoll = time.Now().UTC()
			return nil
		})
		return e
	case "submitting", "uncertain":
		return s.reconciling(t.ID)
	case "reconciling":
		return s.reconcile(ctx, t)
	case "verifying":
		return s.verify(ctx, t)
	case "downloading":
		return s.download(ctx, t)
	case "queued", "staged":
		if t.SubmissionAttempted {
			return s.reconciling(t.ID)
		}
		if t.Book.Origin == "cwa" {
			b, e := s.CWA.Book(ctx, t.Book)
			if e != nil {
				return e
			}
			if !domain.Match(t.Book, b, false) || !domain.HasFormat(b, t.RequiredFormat) {
				return domain.E("FORMAT_UNAVAILABLE", "选定 CWA 记录已变化或所需格式不可用")
			}
			_, e = s.change(t.ID, func(t *domain.Task) error {
				t.CWA = &b
				t.State = "verifying"
				t.VerificationBasis = "reused_existing"
				return nil
			})
			return e
		}
		candidates, complete, e := s.matches(ctx, t)
		if e != nil {
			return e
		}
		good := []domain.Book{}
		for _, b := range candidates {
			if domain.Match(t.Book, b, false) {
				good = append(good, b)
			}
		}
		if !complete || len(good) > 1 {
			return s.review(t.ID, candidates, "AMBIGUOUS_CWA_MATCH", "搜索未完整或存在多个基本匹配，需用户确认")
		}
		if len(good) == 1 && domain.HasFormat(good[0], t.RequiredFormat) {
			_, e = s.change(t.ID, func(t *domain.Task) error {
				t.CWA = &good[0]
				t.State = "verifying"
				t.VerificationBasis = "reused_existing"
				t.Candidates = candidates
				return nil
			})
			return e
		}
		if !t.InputComplete {
			if t.Kind == "upload" {
				return domain.E("INPUT_MISSING", "设备完整文件已丢失，请重新准备上传")
			}
			_, e = s.change(t.ID, func(t *domain.Task) error {
				t.State = "downloading"
				t.Candidates = candidates
				return nil
			})
			return e
		}
		return s.submit(ctx, t, candidates)
	}
	return domain.E("INTERNAL_ERROR", "任务状态不可执行")
}
func (s *Service) matches(ctx context.Context, t domain.Task) ([]domain.Book, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	q := domain.Query{Query: t.Book.Title, Language: "any"}
	out := []domain.Book{}
	next := ""
	for i := 0; i < 3; i++ {
		p, e := s.CWA.Search(ctx, q, next)
		if e != nil {
			return out, false, e
		}
		for _, b := range p.Books {
			if len(out) < 50 {
				out = append(out, b)
			} else {
				return out, false, nil
			}
		}
		if p.Incomplete {
			return out, false, nil
		}
		if p.Next == "" {
			return out, true, nil
		}
		if p.Next == next {
			return out, false, nil
		}
		next = p.Next
	}
	return out, false, nil
}
func (s *Service) download(ctx context.Context, t domain.Task) error {
	ctx, done, e := s.beginTransfer(ctx, t.ID)
	if e != nil {
		return e
	}
	defer done()
	if e = s.Files.Acquire(ctx); e != nil {
		return e
	}
	defer s.Files.Done()
	size := s.Config.MaxFile
	if t.Book.Size != nil {
		if *t.Book.Size <= 0 || *t.Book.Size > s.Config.MaxFile {
			return domain.E("FILE_TOO_LARGE", "书源声明大小超过上限")
		}
	}
	if e = s.Files.Reserve(t.ID, size); e != nil {
		return e
	}
	resp, e := s.Source.Open(ctx, t.Book)
	if e != nil {
		s.Files.Release(t.ID)
		return e
	}
	defer resp.Body.Close()
	if resp.ContentLength > s.Config.MaxFile {
		s.Files.Release(t.ID)
		return domain.E("FILE_TOO_LARGE", "书源文件超过上限")
	}
	// Preserve the validated source filename when one is supplied; it is never
	// used as the local path or as book identity.
	if v, params, e := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); e == nil && v != "" && params["filename"] != "" {
		if e = domain.Filename(params["filename"]); e != nil {
			return e
		}
		if domain.Format(filepath.Ext(params["filename"])) != t.Book.Formats[0] {
			return domain.E("SOURCE_CHANGED", "下载文件格式与候选不符")
		}
		t.Filename = params["filename"]
	}
	n, e := s.Files.Receive(ctx, t.ID, resp.Body, resp.ContentLength, s.Config.MaxFile, t.Book.Formats[0], "", s.progress(t.ID))
	if e != nil {
		s.Files.Release(t.ID)
		return e
	}
	_, e = s.change(t.ID, func(current *domain.Task) error {
		current.InputComplete = true
		current.Size = n
		current.Received = n
		current.Filename = t.Filename
		current.State = "staged"
		current.ResumeStep = "staged"
		current.Attempts = 0
		return nil
	})
	return e
}
func (s *Service) submit(ctx context.Context, t domain.Task, candidates []domain.Book) error {
	parentContext := ctx
	if !s.Config.CWAWrite {
		return domain.E("WRITES_DISABLED", "当前配置未启用 CWA 写入")
	}
	token, e := s.CWA.PrepareUpload(ctx)
	if e != nil {
		return e
	}
	ctx, done, e := s.beginTransfer(ctx, t.ID)
	if e != nil {
		return e
	}
	defer done()
	if e = s.Files.Acquire(ctx); e != nil {
		return e
	}
	defer s.Files.Done()
	_, e = s.change(t.ID, func(t *domain.Task) error {
		t.State = "submitting"
		t.ResumeStep = "reconciling"
		t.SubmissionAttempted = true
		t.Candidates = candidates
		t.Attempts = 0
		return nil
	})
	if e != nil {
		return e
	}
	may, uploadErr := s.CWA.Upload(ctx, s.Files.Path(t.ID, "input"), t.Filename, token)
	if parentContext.Err() != nil {
		return uploadErr
	}
	if uploadErr != nil && !may {
		_, e = s.change(t.ID, func(t *domain.Task) error {
			t.SubmissionAttempted = false
			t.ResumeStep = "staged"
			t.State = "staged"
			return nil
		})
		if e != nil {
			return e
		}
		return uploadErr
	}
	_, e = s.change(t.ID, func(t *domain.Task) error {
		t.State = "reconciling"
		if uploadErr != nil {
			t.State = "uncertain"
			t.Error = domain.AsError(uploadErr)
		}
		if t.ReconcileDeadline.IsZero() {
			t.ReconcileDeadline = time.Now().UTC().Add(s.Config.ConfirmWindow)
		}
		t.NextPoll = time.Now().UTC().Add(2 * time.Second)
		t.Polls = 0
		return nil
	})
	return e
}
func (s *Service) reconciling(id string) error {
	_, e := s.change(id, func(t *domain.Task) error {
		t.State = "reconciling"
		if t.ReconcileDeadline.IsZero() {
			t.ReconcileDeadline = time.Now().UTC().Add(s.Config.ConfirmWindow)
		}
		return nil
	})
	return e
}
func (s *Service) review(id string, books []domain.Book, code, msg string) error {
	_, e := s.change(id, func(t *domain.Task) error {
		t.State = "needs_review"
		t.Candidates = books
		t.Error = domain.E(code, msg)
		return nil
	})
	return e
}
func (s *Service) reconcile(ctx context.Context, t domain.Task) error {
	if time.Now().After(t.ReconcileDeadline) {
		return s.review(t.ID, t.Candidates, "IMPORT_UNCONFIRMED", "确认窗口内未验证到 CWA 所需格式；不会自动再次上传")
	}
	books, complete, e := s.matches(ctx, t)
	if e != nil {
		return e
	}
	good := []domain.Book{}
	for _, b := range books {
		if domain.Match(t.Book, b, false) {
			good = append(good, b)
		}
	}
	if !complete || len(good) > 1 {
		return s.review(t.ID, books, "AMBIGUOUS_CWA_MATCH", "匹配不唯一或查询未完整，需用户确认")
	}
	if len(good) == 1 && domain.HasFormat(good[0], t.RequiredFormat) {
		_, e = s.change(t.ID, func(t *domain.Task) error {
			t.CWA = &good[0]
			t.Candidates = books
			t.State = "verifying"
			t.VerificationBasis = "matched_after_submission"
			t.Error = nil
			t.Attempts = 0
			return nil
		})
		return e
	}
	_, e = s.change(t.ID, func(t *domain.Task) error {
		t.Candidates = books
		t.Polls++
		seconds := []int{2, 4, 8, 15, 30}[min(t.Polls, 4)]
		jitter := time.Duration(time.Now().UnixNano()%500) * time.Millisecond
		t.NextPoll = time.Now().UTC().Add(time.Duration(seconds)*time.Second + jitter)
		t.Error = domain.E("FORMAT_NOT_READY", "CWA 所需格式尚未核验到")
		return nil
	})
	return e
}
func (s *Service) verify(ctx context.Context, t domain.Task) error {
	if t.CWA == nil {
		return domain.E("INTERNAL_ERROR", "缺少 CWA 记录")
	}
	b, e := s.CWA.Book(ctx, *t.CWA)
	if e != nil {
		return e
	}
	if !domain.Match(t.Book, b, t.VerificationBasis == "user_resolved") {
		return s.review(t.ID, []domain.Book{b}, "AMBIGUOUS_CWA_MATCH", "核验时元数据已变化，需要用户确认")
	}
	if !domain.HasFormat(b, t.RequiredFormat) {
		if t.SubmissionAttempted {
			return s.reconciling(t.ID)
		}
		return domain.E("FORMAT_UNAVAILABLE", "CWA 所需格式已失效")
	}
	ctx, done, e := s.beginTransfer(ctx, t.ID)
	if e != nil {
		return e
	}
	defer done()
	if e = s.Files.Acquire(ctx); e != nil {
		return e
	}
	defer s.Files.Done()
	if e = s.Files.Reserve(t.ID+":verify", s.Config.MaxFile); e != nil {
		return e
	}
	defer s.Files.Release(t.ID + ":verify")
	if e = s.Files.MakeDir(t.ID); e != nil {
		return e
	}
	resp, e := s.CWA.Open(ctx, b, t.RequiredFormat)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.ContentLength > s.Config.MaxFile {
		return domain.E("FILE_TOO_LARGE", "CWA 输出文件超过验证上限")
	}
	path := s.Files.Path(t.ID, "verify.part")
	f, e := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer os.Remove(path)
	n, _, e := s.Files.Copy(ctx, f, resp.Body, s.Config.MaxFile, s.progress(t.ID))
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return domain.E("CWA_FILE_INVALID", "CWA 文件未完整返回")
	}
	if e = files.Validate(ctx, path, t.RequiredFormat, s.Config.MaxFile); e != nil {
		return e
	}
	_, e = s.change(t.ID, func(t *domain.Task) error {
		t.CWA = &b
		t.State = "ready"
		t.VerifiedAt = time.Now().UTC()
		t.Error = nil
		return nil
	})
	return e
}
