package cwa

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/httpx"
	"golang.org/x/net/html"
)

type Client struct {
	base, user, password string
	meta, file           *http.Client
	login                sync.Mutex
	logged               bool
}

func New(base, user, password string) *Client {
	m := httpx.NewClient([]string{base}, true)
	f := httpx.NewClient([]string{base}, false)
	f.Jar = m.Jar
	return &Client{base: strings.TrimRight(base, "/"), user: user, password: password, meta: m, file: f}
}
func (c *Client) Close() { httpx.Close(c.meta); httpx.Close(c.file) }
func (c *Client) request(ctx context.Context, method, path string, body io.Reader, basic bool) (*http.Response, error) {
	target, e := httpx.Resolve(c.base+"/", path)
	if e != nil {
		return nil, e
	}
	r, e := http.NewRequestWithContext(ctx, method, target, body)
	if e != nil {
		return nil, e
	}
	if basic {
		r.SetBasicAuth(c.user, c.password)
	}
	return c.meta.Do(r)
}

type link struct {
	Rel    string `xml:"rel,attr"`
	Href   string `xml:"href,attr"`
	Type   string `xml:"type,attr"`
	Length string `xml:"length,attr"`
}
type entry struct {
	Title   string `xml:"title"`
	ID      string `xml:"id"`
	Authors []struct {
		Name string `xml:"name"`
	} `xml:"author"`
	Languages []string `xml:"language"`
	Links     []link   `xml:"link"`
}
type position struct {
	URL  string `json:"url"`
	Skip int    `json:"skip"`
}

func (c *Client) Search(ctx context.Context, q domain.Query, cursor string) (domain.Page, error) {
	p := position{URL: c.base + "/opds/search?query=" + url.QueryEscape(q.Query)}
	if cursor != "" {
		if json.Unmarshal([]byte(cursor), &p) != nil || p.Skip < 0 || p.Skip > 100000 {
			return domain.Page{}, domain.E("CURSOR_INVALID", "CWA 游标失效")
		}
	}
	resp, e := c.request(ctx, "GET", p.URL, nil, true)
	if e != nil {
		return domain.Page{}, classify(e)
	}
	defer resp.Body.Close()
	if e = httpx.CheckStatus(resp, "CWA"); e != nil {
		return domain.Page{}, e
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		return domain.Page{}, domain.E("AUTH_EXPIRED", "CWA 返回登录页或错误页")
	}
	d := xml.NewDecoder(resp.Body)
	page := domain.Page{Books: []domain.Book{}}
	count := 0
	nextHref := ""
	root := false
	for {
		tok, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return page, classify(e)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !root {
			if start.Name.Local != "feed" {
				return page, domain.E("CWA_UNAVAILABLE", "CWA 未返回 OPDS feed")
			}
			root = true
			continue
		}
		if start.Name.Local == "entry" {
			var ent entry
			if e = d.DecodeElement(&ent, &start); e != nil {
				return page, classify(e)
			}
			count++
			if count <= p.Skip {
				continue
			}
			if len(page.Books) >= 50 {
				next := position{p.URL, p.Skip + 50}
				b, _ := json.Marshal(next)
				page.Next = string(b)
				return page, nil
			}
			b, e := c.entryBook(ent)
			if e != nil {
				return page, e
			}
			if b.ID != "" {
				page.Books = append(page.Books, b)
			}
		} else if start.Name.Local == "link" {
			var l link
			if e = d.DecodeElement(&l, &start); e != nil {
				return page, classify(e)
			}
			if l.Rel == "next" {
				nextHref = l.Href
			}
		}
	}
	if !root {
		return page, domain.E("CWA_UNAVAILABLE", "OPDS 响应为空")
	}
	if nextHref != "" {
		next, e := httpx.Resolve(p.URL, nextHref)
		if e != nil {
			return page, e
		}
		if next == p.URL {
			return page, domain.E("CWA_UNAVAILABLE", "CWA 分页未前进")
		}
		b, _ := json.Marshal(position{URL: next})
		page.Next = string(b)
	}
	return page, nil
}
func (c *Client) entryBook(e entry) (domain.Book, error) {
	b := domain.Book{Origin: "cwa", Title: e.Title, AvailableInCWA: true, UUID: strings.TrimPrefix(e.ID, "urn:uuid:"), Language: "unknown"}
	for _, a := range e.Authors {
		b.Authors = append(b.Authors, a.Name)
	}
	for _, l := range e.Languages {
		if b.Language == "unknown" || domain.Language(l) == "zh" {
			b.Language = domain.Language(l)
		}
	}
	for _, l := range e.Links {
		if l.Href == "" {
			continue
		}
		u, err := httpx.Resolve(c.base+"/", l.Href)
		if err != nil {
			return b, err
		}
		parsed, _ := url.Parse(u)
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if strings.Contains(l.Rel, "acquisition") {
			for i, v := range parts {
				if v == "download" && i+2 < len(parts) {
					if _, err := strconv.ParseUint(parts[i+1], 10, 64); err != nil {
						return b, domain.E("CWA_UNAVAILABLE", "CWA ID 无效")
					}
					b.ID = parts[i+1]
					f := domain.Format(parts[i+2])
					if len(f) > 32 {
						return b, domain.E("METADATA_TOO_LARGE", "格式字段过长")
					}
					b.Formats = append(b.Formats, f)
					if n, err := strconv.ParseInt(l.Length, 10, 64); err == nil && n > 0 {
						b.Size = &n
					}
					break
				}
			}
		} else if b.ID == "" {
			for i, v := range parts {
				if (v == "cover" || strings.HasPrefix(v, "cover_")) && i+1 < len(parts) {
					b.ID = parts[i+1]
				}
			}
		}
	}
	return domain.CleanBook(b)
}
func (c *Client) Book(ctx context.Context, known domain.Book) (domain.Book, error) {
	if known.UUID == "" || domain.CheckID(known.UUID) != nil {
		return known, domain.E("CWA_UNAVAILABLE", "CWA 候选缺少可核验 UUID")
	}
	resp, e := c.request(ctx, "GET", c.base+"/ajax/book/"+url.PathEscape(known.UUID), nil, true)
	if e != nil {
		return known, classify(e)
	}
	if e = httpx.CheckStatus(resp, "CWA"); e != nil {
		resp.Body.Close()
		return known, e
	}
	data, e := httpx.Read(resp)
	if e != nil {
		return known, classify(e)
	}
	var r struct {
		ID        int64    `json:"application_id"`
		UUID      string   `json:"uuid"`
		Title     string   `json:"title"`
		Authors   []string `json:"authors"`
		Languages []string `json:"languages"`
		Formats   []string `json:"formats"`
		Metadata  map[string]struct {
			Size int64 `json:"size"`
		} `json:"format_metadata"`
	}
	if json.Unmarshal(data, &r) != nil || strconv.FormatInt(r.ID, 10) != known.ID || r.UUID != known.UUID {
		return known, domain.E("CWA_UNAVAILABLE", "CWA 记录已失效或详情不兼容")
	}
	b := domain.Book{Origin: "cwa", ID: known.ID, UUID: r.UUID, Title: r.Title, Authors: r.Authors, Formats: r.Formats, AvailableInCWA: true, Language: "unknown"}
	for _, l := range r.Languages {
		if b.Language == "unknown" || domain.Language(l) == "zh" {
			b.Language = domain.Language(l)
		}
	}
	for _, m := range r.Metadata {
		if m.Size > 0 {
			n := m.Size
			b.Size = &n
			break
		}
	}
	return domain.CleanBook(b)
}
func (c *Client) Open(ctx context.Context, b domain.Book, format string) (*http.Response, error) {
	if !domain.HasFormat(b, format) {
		return nil, domain.E("FORMAT_UNAVAILABLE", "CWA 记录没有所需格式")
	}
	if _, e := strconv.ParseUint(b.ID, 10, 64); e != nil {
		return nil, domain.E("INVALID_ARGUMENT", "CWA ID 无效")
	}
	r, e := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/opds/download/%s/%s/", c.base, b.ID, format), nil)
	if e != nil {
		return nil, e
	}
	r.SetBasicAuth(c.user, c.password)
	resp, e := c.file.Do(r)
	if e != nil {
		return nil, classify(e)
	}
	if e = CheckFileResponse(resp, format); e != nil {
		resp.Body.Close()
		return nil, e
	}
	return resp, nil
}
func CheckFileResponse(resp *http.Response, format string) error {
	if resp.StatusCode != 200 {
		if e := httpx.CheckStatus(resp, "CWA"); e != nil {
			return e
		}
		return domain.E("CWA_FILE_INVALID", "CWA 未返回完整 HTTP 200 文件")
	}
	ct := strings.ToLower(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if ct != domain.ContentType(format) && ct != "application/octet-stream" && ct != "application/zip" {
		return domain.E("CWA_FILE_INVALID", "上游返回的不是所需电子书文件")
	}
	if resp.Header.Get("Content-Encoding") != "" && resp.Header.Get("Content-Encoding") != "identity" {
		return domain.E("CWA_FILE_INVALID", "不接受压缩编码的文件响应")
	}
	return nil
}
func csrf(data []byte) (string, bool) {
	z := html.NewTokenizer(strings.NewReader(string(data)))
	login := false
	token := ""
	for {
		kind := z.Next()
		if kind == html.ErrorToken {
			return token, login
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		t := z.Token()
		if t.Data != "input" {
			continue
		}
		name, value := "", ""
		for _, a := range t.Attr {
			if a.Key == "name" {
				name = a.Val
			}
			if a.Key == "value" {
				value = a.Val
			}
		}
		if name == "csrf_token" {
			token = value
		}
		if name == "password" {
			login = true
		}
	}
}
func (c *Client) session(ctx context.Context) (string, error) {
	resp, e := c.request(ctx, "GET", c.base+"/me", nil, false)
	if e != nil {
		return "", classify(e)
	}
	data, e := httpx.Read(resp)
	if e != nil {
		return "", classify(e)
	}
	token, isLogin := csrf(data)
	if resp.StatusCode == 200 && !isLogin && token != "" {
		c.logged = true
		return token, nil
	}
	resp, e = c.request(ctx, "GET", c.base+"/login", nil, false)
	if e != nil {
		return "", classify(e)
	}
	data, e = httpx.Read(resp)
	if e != nil {
		return "", classify(e)
	}
	token, _ = csrf(data)
	if token == "" {
		return "", domain.E("AUTH_REQUIRED", "CWA 登录页缺少 CSRF，检查版本和登录方式")
	}
	form := url.Values{"username": {c.user}, "password": {c.password}, "csrf_token": {token}}
	r, e := http.NewRequestWithContext(ctx, "POST", c.base+"/login", strings.NewReader(form.Encode()))
	if e != nil {
		return "", e
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, e = c.meta.Do(r)
	if e != nil {
		return "", classify(e)
	}
	_, e = httpx.Read(resp)
	if e != nil {
		return "", classify(e)
	}
	resp, e = c.request(ctx, "GET", c.base+"/me", nil, false)
	if e != nil {
		return "", classify(e)
	}
	data, e = httpx.Read(resp)
	if e != nil {
		return "", classify(e)
	}
	token, isLogin = csrf(data)
	if resp.StatusCode != 200 || isLogin || token == "" {
		return "", domain.E("AUTH_REQUIRED", "CWA 登录未通过受保护页面验证")
	}
	c.logged = true
	return token, nil
}

// PrepareUpload is a read/login operation. The caller persists submission
// intent only after it succeeds, immediately before Upload sends the file.
func (c *Client) PrepareUpload(ctx context.Context) (string, error) {
	c.login.Lock()
	defer c.login.Unlock()
	return c.session(ctx)
}
func (c *Client) Upload(ctx context.Context, path, filename, token string) (bool, error) {
	f, e := os.Open(path)
	if e != nil {
		return false, domain.E("INPUT_MISSING", "完整暂存文件已丢失")
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return false, e
	}
	reader, writer := io.Pipe()
	mw := multipart.NewWriter(writer)
	boundary := mw.Boundary()
	// Compute the exact overhead with the same fields and boundary. The book
	// remains a file stream; only the tiny multipart headers are counted.
	count := &counter{}
	cm := multipart.NewWriter(count)
	_ = cm.SetBoundary(boundary)
	_ = cm.WriteField("csrf_token", token)
	_, e = cm.CreateFormFile("btn-upload", filename)
	if e != nil {
		return false, e
	}
	_ = cm.Close()
	length := st.Size() + count.n
	done := make(chan error, 1)
	go func() {
		defer writer.Close()
		if e := mw.WriteField("csrf_token", token); e != nil {
			writer.CloseWithError(e)
			done <- e
			return
		}
		part, e := mw.CreateFormFile("btn-upload", filename)
		if e == nil {
			_, e = io.CopyBuffer(part, f, make([]byte, 64<<10))
		}
		if e == nil {
			e = mw.Close()
		}
		if e != nil {
			writer.CloseWithError(e)
		}
		done <- e
	}()
	r, e := http.NewRequestWithContext(ctx, "POST", c.base+"/upload", reader)
	if e != nil {
		reader.CloseWithError(e)
		<-done
		return false, e
	}
	r.ContentLength = length
	r.Header.Set("Content-Type", mw.FormDataContentType())
	resp, e := c.file.Do(r)
	reader.Close()
	copyErr := <-done
	if e != nil {
		return true, domain.E("UPLOAD_OUTCOME_UNKNOWN", "CWA 上传连接中断，必须先核对书库")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 413 || resp.StatusCode == 400 {
		if resp.StatusCode == 413 {
			return false, domain.E("FILE_TOO_LARGE", "CWA 上传通路拒绝完整请求")
		}
		return false, domain.E("AUTH_EXPIRED", "CWA 明确拒绝上传，请恢复权限后重试")
	}
	if copyErr != nil {
		return true, domain.E("UPLOAD_OUTCOME_UNKNOWN", "CWA 上传未确认，必须先核对书库")
	}
	data, e := httpx.Read(resp)
	if e != nil || resp.StatusCode != 200 {
		return true, domain.E("UPLOAD_OUTCOME_UNKNOWN", "CWA 上传响应不可确认")
	}
	_, isLogin := csrf(data)
	if isLogin {
		return true, domain.E("UPLOAD_OUTCOME_UNKNOWN", "CWA 返回登录页，接收结果未知")
	}
	var result struct {
		Location string `json:"location"`
	}
	if json.Unmarshal(data, &result) != nil || result.Location == "" {
		return true, domain.E("UPLOAD_OUTCOME_UNKNOWN", "CWA 上传响应不兼容，先核对书库")
	}
	return true, nil
}

type counter struct{ n int64 }

func (c *counter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
func classify(e error) error {
	if _, ok := e.(*domain.Error); ok {
		return e
	}
	return domain.Temporary("CWA_UNAVAILABLE", "CWA 请求失败或响应不兼容")
}
