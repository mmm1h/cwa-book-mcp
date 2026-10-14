package testfixture

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/mmm1h/cwa-book-mcp/internal/domain"
)

// CWA is a faithful HTTP substitute, not an original CWA instance.
type CWA struct {
	Server                                 *httptest.Server
	Mu                                     sync.Mutex
	Books                                  []domain.Book
	Output                                 []byte
	OutputPath                             string
	OutputBytes                            int64
	InputTitle, InputLanguage, InputFormat string
	Behavior                               string
	Uploads                                atomic.Int64
	Downloads                              atomic.Int64
	FileHTML                               bool
}

func NewCWA(output []byte) *CWA {
	f := &CWA{Output: output, InputTitle: "合成测试书", InputLanguage: "zh", InputFormat: "pdf"}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}
func XML(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func (f *CWA) Add(b domain.Book) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if b.UUID == "" {
		n, _ := strconv.Atoi(b.ID)
		b.UUID = fmt.Sprintf("00000000-0000-0000-0000-%012d", n)
	}
	b.Origin = "cwa"
	b.AvailableInCWA = true
	f.Books = append(f.Books, b)
}
func (f *CWA) snapshot() []domain.Book {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return append([]domain.Book(nil), f.Books...)
}
func (f *CWA) serve(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/login" {
		if r.Method == "POST" {
			_ = r.ParseForm()
			if r.FormValue("csrf_token") != "synthetic-csrf" || r.FormValue("username") != "synthetic" || r.FormValue("password") != "synthetic" {
				w.WriteHeader(403)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "synthetic-cookie", Path: "/", HttpOnly: true})
		}
		io.WriteString(w, `<form><input name="csrf_token" value="synthetic-csrf"><input name="password"></form>`)
		return
	}
	if path == "/me" {
		cookie, e := r.Cookie("session")
		if e != nil || cookie.Value != "synthetic-cookie" {
			http.Redirect(w, r, "/login", 302)
			return
		}
		io.WriteString(w, `<input name="csrf_token" value="synthetic-csrf"><p>synthetic protected page</p>`)
		return
	}
	if path == "/upload" {
		cookie, e := r.Cookie("session")
		if e != nil || cookie.Value != "synthetic-cookie" {
			w.WriteHeader(401)
			return
		}
		reader, e := r.MultipartReader()
		if e != nil {
			w.WriteHeader(400)
			return
		}
		csrf := ""
		files := 0
		for {
			part, e := reader.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil {
				w.WriteHeader(400)
				return
			}
			if part.FormName() == "csrf_token" {
				b, _ := io.ReadAll(io.LimitReader(part, 128))
				csrf = string(b)
			} else if part.FormName() == "btn-upload" {
				files++
				_, e = io.CopyBuffer(io.Discard, part, make([]byte, 64<<10))
				if e != nil {
					w.WriteHeader(400)
					return
				}
			} else {
				w.WriteHeader(400)
				return
			}
			part.Close()
		}
		if csrf != "synthetic-csrf" || files != 1 {
			w.WriteHeader(403)
			return
		}
		f.Uploads.Add(1)
		f.Mu.Lock()
		behavior := f.Behavior
		f.Mu.Unlock()
		if behavior == "reject" {
			w.WriteHeader(403)
			return
		}
		if behavior != "refuse200" {
			f.Add(domain.Book{ID: "42", Title: f.InputTitle, Language: f.InputLanguage, Formats: []string{f.InputFormat}})
		}
		if behavior == "unknown" {
			conn, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				conn.Close()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"location":"/tasks"}`)
		return
	}
	user, pass, ok := r.BasicAuth()
	if !ok || user != "synthetic" || pass != "synthetic" {
		w.WriteHeader(401)
		return
	}
	if path == "/opds/search" {
		w.Header().Set("Content-Type", "application/atom+xml")
		io.WriteString(w, `<feed xmlns="http://www.w3.org/2005/Atom" xmlns:dcterms="http://purl.org/dc/terms/">`)
		for _, b := range f.snapshot() {
			if r.URL.Query().Get("query") != "" && !strings.Contains(domain.Normalize(b.Title), domain.Normalize(r.URL.Query().Get("query"))) {
				continue
			}
			fmt.Fprintf(w, "<entry><title>%s</title><id>urn:uuid:%s</id>", XML(b.Title), b.UUID)
			for _, a := range b.Authors {
				fmt.Fprintf(w, "<author><name>%s</name></author>", XML(a))
			}
			fmt.Fprintf(w, "<dcterms:language>%s</dcterms:language>", XML(b.Language))
			for _, format := range b.Formats {
				size := int64(len(f.Output))
				if f.OutputPath != "" {
					size = f.OutputBytes
				}
				fmt.Fprintf(w, `<link rel="http://opds-spec.org/acquisition" href="/opds/download/%s/%s/" type="%s" length="%d"/>`, b.ID, format, domain.ContentType(format), size)
			}
			io.WriteString(w, "</entry>")
		}
		io.WriteString(w, "</feed>")
		return
	}
	if strings.HasPrefix(path, "/ajax/book/") {
		uuid := strings.TrimPrefix(path, "/ajax/book/")
		for _, b := range f.snapshot() {
			if b.UUID == uuid {
				n, _ := strconv.Atoi(b.ID)
				_ = json.NewEncoder(w).Encode(map[string]any{"application_id": n, "uuid": b.UUID, "title": b.Title, "authors": b.Authors, "languages": []string{b.Language}, "formats": b.Formats})
				return
			}
		}
		w.WriteHeader(404)
		return
	}
	if strings.HasPrefix(path, "/opds/download/") {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) != 4 {
			w.WriteHeader(404)
			return
		}
		for _, b := range f.snapshot() {
			if b.ID == parts[2] && domain.HasFormat(b, parts[3]) {
				f.Downloads.Add(1)
				if f.FileHTML {
					w.Header().Set("Content-Type", "application/octet-stream")
					io.WriteString(w, "<html>synthetic login page</html>")
					return
				}
				w.Header().Set("Content-Type", domain.ContentType(parts[3]))
				if f.OutputPath != "" {
					w.Header().Set("Content-Length", strconv.FormatInt(f.OutputBytes, 10))
					file, e := os.Open(f.OutputPath)
					if e != nil {
						w.WriteHeader(500)
						return
					}
					defer file.Close()
					_, _ = io.CopyBuffer(w, file, make([]byte, 64<<10))
					return
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(f.Output)))
				w.Write(f.Output)
				return
			}
		}
		w.WriteHeader(404)
		return
	}
	w.WriteHeader(404)
}

type Zlib struct {
	Server    *httptest.Server
	Book      domain.Book
	Data      []byte
	DataPath  string
	DataBytes int64
	Searches  atomic.Int64
	Downloads atomic.Int64
}

func NewZlib(b domain.Book, data []byte) *Zlib {
	f := &Zlib{Book: b, Data: data}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		size := int64(len(data))
		if f.DataPath != "" {
			size = f.DataBytes
		}
		raw := map[string]any{"id": "7", "hash": "synthetic-hash", "title": b.Title, "language": b.Language, "extension": b.Formats[0], "filesize": size}
		switch r.URL.Path {
		case "/eapi/book/search":
			f.Searches.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": 1, "books": []any{raw}, "pagination": map[string]any{"total_pages": 1}})
		case "/eapi/book/7/synthetic-hash":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": 1, "book": raw})
		case "/eapi/book/7/synthetic-hash/file":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": 1, "file": map[string]any{"downloadLink": f.Server.URL + "/file"}})
		case "/file":
			f.Downloads.Add(1)
			w.Header().Set("Content-Type", domain.ContentType(b.Formats[0]))
			w.Header().Set("Content-Disposition", "attachment; filename=synthetic."+b.Formats[0])
			if f.DataPath != "" {
				w.Header().Set("Content-Length", strconv.FormatInt(f.DataBytes, 10))
				file, e := os.Open(f.DataPath)
				if e != nil {
					w.WriteHeader(500)
					return
				}
				defer file.Close()
				_, _ = io.CopyBuffer(w, file, make([]byte, 64<<10))
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Write(data)
		default:
			w.WriteHeader(404)
		}
	}))
	return f
}
