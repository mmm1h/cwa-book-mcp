package zlib

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	upstream "github.com/heartleo/zlib"
	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/httpx"
)

type Client struct {
	domain, session, mode string
	meta, file            *http.Client
	gate                  chan struct{}
}

func New(base, session, mode string, fileOrigins []string) *Client {
	return &Client{base, session, mode, httpx.NewClient([]string{base}, true), httpx.NewClient(fileOrigins, false), make(chan struct{}, 1)}
}
func (c *Client) Close() { httpx.Close(c.meta); httpx.Close(c.file) }
func (c *Client) core(ctx context.Context) (*upstream.Client, error) {
	if c.mode != "eapi" {
		return nil, domain.E("UNSUPPORTED_SOURCE_MODE", "首版只验证 EAPI 书源通路")
	}
	if c.domain == "" || c.session == "" {
		return nil, domain.E("AUTH_REQUIRED", "服务端尚未配置书源入口和会话文件")
	}
	f, e := os.Open(c.session)
	if e != nil {
		return nil, domain.E("AUTH_REQUIRED", "书源会话文件不可用")
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, 65537))
	if e != nil || len(data) > 65536 {
		return nil, domain.E("AUTH_REQUIRED", "书源会话文件无效")
	}
	var session struct {
		Cookies map[string]string `json:"cookies"`
	}
	if json.Unmarshal(data, &session) != nil || session.Cookies["remix_userid"] == "" || session.Cookies["remix_userkey"] == "" {
		return nil, domain.E("AUTH_REQUIRED", "会话文件需包含 cookies.remix_userid/remix_userkey")
	}
	z := upstream.NewClient(upstream.WithDomain(c.domain), upstream.WithHTTPClient(c.meta), upstream.WithRequestContext(ctx))
	z.SetCookies(session.Cookies)
	if c.mode == "eapi" {
		z.SetMode(upstream.ClientModeEAPI)
	}
	return z, nil
}
func (c *Client) lock(ctx context.Context) error {
	select {
	case c.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *Client) Search(ctx context.Context, q domain.Query, cursor string) (domain.Page, error) {
	if e := c.lock(ctx); e != nil {
		return domain.Page{}, e
	}
	defer func() { <-c.gate }()
	z, e := c.core(ctx)
	if e != nil {
		return domain.Page{}, e
	}
	page := 1
	if cursor != "" {
		page, e = strconv.Atoi(cursor)
		if e != nil || page < 1 || page > 100000 {
			return domain.Page{}, domain.E("CURSOR_INVALID", "书源游标无效")
		}
	}
	result, e := z.Search(q.Query, page, 50, nil)
	if e != nil {
		return domain.Page{}, classify(e)
	}
	out := domain.Page{Books: []domain.Book{}}
	for _, raw := range result.Books {
		b := domain.Book{Origin: "zlibrary", ID: raw.ID, Ref: raw.ID, Title: raw.Name, Authors: raw.Authors, Language: raw.Language, Formats: []string{raw.Extension}, ISBN: raw.ISBN}
		if raw.Hash != "" {
			b.Ref = raw.ID + ":" + raw.Hash
		}
		if n, e := strconv.ParseInt(raw.Size, 10, 64); e == nil && n > 0 {
			b.Size = &n
		}
		b, e = domain.CleanBook(b)
		if e != nil {
			return out, e
		}
		out.Books = append(out.Books, b)
	}
	if result.TotalPages > page {
		out.Next = strconv.Itoa(page + 1)
	} else if result.TotalPages <= 0 && len(result.Books) == 50 {
		out.Next = strconv.Itoa(page + 1)
		out.Incomplete = true
	}
	return out, nil
}
func (c *Client) Open(ctx context.Context, b domain.Book) (*http.Response, error) {
	if e := c.lock(ctx); e != nil {
		return nil, e
	}
	defer func() { <-c.gate }()
	z, e := c.core(ctx)
	if e != nil {
		return nil, e
	}
	detail, e := z.FetchBook(b.Ref)
	if e != nil {
		return nil, classify(e)
	}
	got := domain.Book{Title: detail.Name, Authors: detail.Authors, Language: detail.Language}
	if !domain.Match(b, got, false) {
		return nil, domain.E("SOURCE_CHANGED", "书源详情与已选候选不符，请重新搜索")
	}
	if detail.DownloadURL == "" {
		return nil, domain.E("QUOTA_EXCEEDED", "书源未提供可用下载入口，检查账号额度")
	}
	resp, e := z.OpenDownloadStream(ctx, c.file, detail.DownloadURL)
	if e != nil {
		return nil, classify(e)
	}
	if e = httpx.CheckStatus(resp, "SOURCE"); e != nil {
		resp.Body.Close()
		return nil, e
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/json") {
		resp.Body.Close()
		return nil, domain.E("AUTH_EXPIRED", "书源返回认证页或错误页")
	}
	return resp, nil
}
func classify(e error) error {
	var de *domain.Error
	if errors.As(e, &de) {
		return de
	}
	s := strings.ToLower(e.Error())
	switch {
	case errors.Is(e, upstream.ErrNotLoggedIn), errors.Is(e, upstream.ErrSessionExpired), strings.Contains(s, "not authorized"), strings.Contains(s, "authorization"), strings.Contains(s, "login"):
		return domain.E("AUTH_EXPIRED", "书源会话失效，请在服务端恢复")
	case errors.Is(e, upstream.ErrChallengeFailed), strings.Contains(s, "bot protection"), strings.Contains(s, "captcha"):
		return domain.E("CAPTCHA_REQUIRED", "书源要求人工验证")
	case strings.Contains(s, "limit"), strings.Contains(s, "quota"):
		return domain.E("QUOTA_EXCEEDED", "书源账号额度不足")
	}
	return domain.Temporary("SOURCE_UNAVAILABLE", "书源请求失败或响应不兼容")
}
