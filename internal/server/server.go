package server

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/catalog"
	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/files"
	"github.com/mmm1h/cwa-book-mcp/internal/jobs"
	"github.com/mmm1h/cwa-book-mcp/internal/mcp"
)

type App struct {
	Jobs     *jobs.Service
	controls chan struct{}
}

func New(s *jobs.Service) *App { return &App{s, make(chan struct{}, 8)} }
func (a *App) Search(ctx context.Context, in catalog.Input) (any, error) {
	return a.Jobs.Catalog.Search(ctx, in)
}
func (a *App) Acquire(ctx context.Context, in jobs.AcquireInput) (json.RawMessage, error) {
	return a.Jobs.Acquire(ctx, in)
}
func (a *App) Upload(in jobs.UploadInput) (json.RawMessage, error) { return a.Jobs.Upload(in) }
func (a *App) Task(ctx context.Context, id string) (any, error) {
	t, e := a.Jobs.Store.Get(id)
	if e != nil {
		return nil, e
	}
	t.ExactKey = ""
	return struct {
		domain.Task
		Next      int64    `json:"next_poll_after_seconds"`
		CWABookID string   `json:"cwa_book_id,omitempty"`
		Formats   []string `json:"available_formats,omitempty"`
	}{t, max(0, int64(time.Until(t.NextPoll).Seconds())), bookID(t.CWA), bookFormats(t.CWA)}, nil
}
func bookID(b *domain.Book) string {
	if b == nil {
		return ""
	}
	return b.ID
}
func bookFormats(b *domain.Book) []string {
	if b == nil {
		return nil
	}
	return b.Formats
}

type DownloadResult struct {
	URL      string    `json:"url"`
	Expires  time.Time `json:"expires_at"`
	Filename string    `json:"filename"`
	Size     *int64    `json:"size_bytes"`
}

func (a *App) DownloadURL(ctx context.Context, id, format, taskID string) (any, error) {
	format = domain.Format(format)
	if !domain.Supported(format) {
		return nil, domain.E("UNSUPPORTED_FORMAT", "首版仅支持 EPUB/PDF")
	}
	var b domain.Book
	var e error
	if taskID != "" {
		t, e := a.Jobs.Store.Get(taskID)
		if e != nil {
			return nil, e
		}
		if t.State != "ready" || t.CWA == nil || t.CWA.ID != id || t.RequiredFormat != format {
			return nil, domain.E("INVALID_ARGUMENT", "task_id 必须对应 ready 的 CWA ID 和格式")
		}
		b = *t.CWA
	} else {
		b, e = a.Jobs.Store.KnownCWA(id)
		if e != nil {
			return nil, e
		}
	}
	b, e = a.Jobs.CWA.Book(ctx, b)
	if e != nil {
		return nil, e
	}
	if e = a.Jobs.Files.Acquire(ctx); e != nil {
		return nil, e
	}
	defer a.Jobs.Files.Done()
	resp, e := a.Jobs.CWA.Open(ctx, b, format)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.ContentLength > a.Jobs.Config.MaxFile {
		return nil, domain.E("FILE_TOO_LARGE", "CWA 文件超过服务文件上限")
	}
	if _, e = checkedReader(resp.Body, format); e != nil {
		return nil, e
	}
	var size *int64
	if resp.ContentLength >= 0 {
		n := resp.ContentLength
		size = &n
	}
	expires := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Second)
	ticket := files.Sign(a.Jobs.Config.TicketKey, files.Ticket{Purpose: "download", ID: id, Format: format, Expires: expires.Unix()})
	return DownloadResult{a.Jobs.Config.PublicURL + "/files/downloads/" + ticket, expires, "book-" + id + "." + format, size}, nil
}
func checkedReader(body io.Reader, format string) (*bufio.Reader, error) {
	r := bufio.NewReaderSize(body, 4096)
	prefix, e := r.Peek(8)
	if e != nil {
		return nil, domain.E("CWA_FILE_INVALID", "CWA 文件头不完整")
	}
	valid := false
	if format == "pdf" {
		valid = strings.HasPrefix(string(prefix), "%PDF-1.") || strings.HasPrefix(string(prefix), "%PDF-2.")
	} else if format == "epub" {
		valid = string(prefix[:4]) == "PK\x03\x04"
	}
	if !valid {
		return nil, domain.E("CWA_FILE_INVALID", "CWA 返回错误页或不匹配格式")
	}
	return r, nil
}
func writeError(w http.ResponseWriter, e error) {
	d := domain.AsError(e)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	if d.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(d.RetryAfter))
	}
	w.WriteHeader(domain.Status(d))
	_ = json.NewEncoder(w).Encode(map[string]any{"error": d})
}
func (a *App) Handler(version string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", a.authorize(mcp.Handler(a, version)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"alive"}`)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if _, e := a.Jobs.Store.Active(); e != nil {
			writeError(w, e)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ready"}`)
	})
	mux.HandleFunc("PUT /files/uploads/{id}", a.uploadHTTP)
	mux.HandleFunc("GET /files/downloads/{ticket}", a.downloadHTTP)
	return mux
}
func (a *App) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "Bearer "
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, prefix) || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, prefix)), a.Jobs.Config.MCPToken) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="cwa-book-mcp"`)
			writeError(w, domain.E("AUTH_REQUIRED", "MCP Bearer 凭据无效"))
			return
		}
		select {
		case a.controls <- struct{}{}:
			defer func() { <-a.controls }()
		default:
			writeError(w, domain.Temporary("BUSY", "控制请求并发已达上限"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type idleReader struct {
	body       io.ReadCloser
	controller *http.ResponseController
}

func (r idleReader) Read(p []byte) (int, error) {
	_ = r.controller.SetReadDeadline(time.Now().Add(60 * time.Second))
	return r.body.Read(p)
}
func (a *App) uploadHTTP(w http.ResponseWriter, r *http.Request) {
	ticket, e := files.Verify(a.Jobs.Config.TicketKey, r.URL.Query().Get("ticket"), "upload")
	if e != nil {
		writeError(w, e)
		return
	}
	id := r.PathValue("id")
	if ticket.ID != id {
		writeError(w, domain.E("TICKET_INVALID", "票据任务不匹配"))
		return
	}
	if r.ContentLength <= 0 {
		writeError(w, domain.E("LENGTH_MISMATCH", "必须提供与 prepare 一致的 Content-Length"))
		return
	}
	if r.ContentLength > a.Jobs.Config.MaxFile {
		writeError(w, domain.E("FILE_TOO_LARGE", "文件超过允许大小"))
		return
	}
	if r.Header.Get("Content-Range") != "" {
		writeError(w, domain.E("INVALID_ARGUMENT", "首版不支持分块上传"))
		return
	}
	ct, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if e != nil || ct != "application/octet-stream" {
		writeError(w, domain.E("INVALID_ARGUMENT", "Content-Type 必须为 application/octet-stream"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.Jobs.Config.TransferTimeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { r.Body.Close() })
	defer stop()
	defer r.Body.Close()
	controller := http.NewResponseController(w)
	defer controller.SetReadDeadline(time.Time{})
	t, e := a.Jobs.Receive(ctx, id, idleReader{r.Body, controller}, r.ContentLength)
	if e != nil {
		writeError(w, e)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(jobs.TaskResult{ID: t.ID, State: t.State, Reused: t.VerificationBasis == "reused_existing"})
}

type idleWriter struct {
	w http.ResponseWriter
	c *http.ResponseController
}

func (w idleWriter) Write(p []byte) (int, error) {
	_ = w.c.SetWriteDeadline(time.Now().Add(60 * time.Second))
	return w.w.Write(p)
}
func (a *App) downloadHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ticket, e := files.Verify(a.Jobs.Config.TicketKey, r.PathValue("ticket"), "download")
	if e != nil {
		writeError(w, e)
		return
	}
	if r.Header.Get("Range") != "" {
		w.Header().Set("Content-Range", "bytes */*")
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.Jobs.Config.TransferTimeout)
	defer cancel()
	if e = a.Jobs.Files.Acquire(ctx); e != nil {
		writeError(w, e)
		return
	}
	defer a.Jobs.Files.Done()
	b, e := a.Jobs.Store.KnownCWA(ticket.ID)
	if e != nil {
		writeError(w, e)
		return
	}
	b, e = a.Jobs.CWA.Book(ctx, b)
	if e != nil {
		writeError(w, e)
		return
	}
	resp, e := a.Jobs.CWA.Open(ctx, b, ticket.Format)
	if e != nil {
		writeError(w, e)
		return
	}
	defer resp.Body.Close()
	if resp.ContentLength > a.Jobs.Config.MaxFile {
		writeError(w, domain.E("FILE_TOO_LARGE", "CWA 文件超过服务上限"))
		return
	}
	reader, e := checkedReader(resp.Body, ticket.Format)
	if e != nil {
		writeError(w, e)
		return
	}
	w.Header().Set("Content-Type", domain.ContentType(ticket.Format))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "book-" + ticket.ID + "." + ticket.Format}))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if resp.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	c := http.NewResponseController(w)
	defer c.SetWriteDeadline(time.Time{})
	n, _, e := a.Jobs.Files.Copy(ctx, idleWriter{w, c}, reader, a.Jobs.Config.MaxFile, nil)
	if e != nil || resp.ContentLength >= 0 && n != resp.ContentLength {
		panic(http.ErrAbortHandler)
	}
}
