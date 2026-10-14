package httpx

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/domain"
)

const MetadataLimit int64 = 4 << 20

type limitedBody struct {
	body      io.ReadCloser
	left      int64
	exhausted bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left == 0 {
		var one [1]byte
		n, err := b.body.Read(one[:])
		if n > 0 {
			return 0, domain.E("METADATA_TOO_LARGE", "上游响应超过上限")
		}
		return 0, err
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, e := b.body.Read(p)
	b.left -= int64(n)
	return n, e
}
func (b *limitedBody) Close() error { return b.body.Close() }

type gzipBody struct {
	*gzip.Reader
	original io.ReadCloser
}

func (b *gzipBody) Close() error { return errors.Join(b.Reader.Close(), b.original.Close()) }

type cancelledBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelledBody) Close() error { e := b.ReadCloser.Close(); b.cancel(); return e }

type idleConn struct {
	net.Conn
	idle time.Duration
}

func (c *idleConn) Read(p []byte) (int, error) {
	_ = c.SetReadDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(p)
}
func (c *idleConn) Write(p []byte) (int, error) {
	_ = c.SetWriteDeadline(time.Now().Add(c.idle))
	return c.Conn.Write(p)
}

type transport struct {
	base     *http.Transport
	origins  map[string]bool
	metadata bool
}

func origin(u *url.URL) string { return strings.ToLower(u.Scheme + "://" + u.Host) }
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.origins[origin(req.URL)] || req.URL.User != nil {
		return nil, domain.E("UPSTREAM_URL_REJECTED", "上游地址不在允许列表")
	}
	ctx := req.Context()
	cancel := func() {}
	if t.metadata {
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
	}
	r := req.Clone(ctx)
	r.Header.Set("Accept-Encoding", "gzip")
	if !t.metadata {
		r.Header.Set("Accept-Encoding", "identity")
	}
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		cancel()
		return nil, err
	}
	if t.metadata {
		resp.Body = &limitedBody{body: resp.Body, left: MetadataLimit}
		if resp.Header.Get("Content-Encoding") == "gzip" {
			gz, e := gzip.NewReader(resp.Body)
			if e != nil {
				resp.Body.Close()
				cancel()
				return nil, e
			}
			resp.Body = &gzipBody{gz, resp.Body}
			resp.Header.Del("Content-Encoding")
			resp.ContentLength = -1
		}
		resp.Body = &limitedBody{body: resp.Body, left: MetadataLimit}
	}
	resp.Body = &cancelledBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}
func NewClient(origins []string, metadata bool) *http.Client {
	dial := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	b := &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 8, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 60 * time.Second, DisableCompression: true, ForceAttemptHTTP2: false, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, e := dial.DialContext(ctx, network, addr)
		if e != nil {
			return nil, e
		}
		return &idleConn{c, 60 * time.Second}, nil
	}}
	a := map[string]bool{}
	for _, v := range origins {
		u, e := url.Parse(v)
		if e == nil {
			a[origin(u)] = true
		}
	}
	jar, _ := cookiejar.New(nil)
	return &http.Client{Transport: &transport{b, a, metadata}, Jar: jar, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !a[origin(r.URL)] {
			return domain.E("UPSTREAM_URL_REJECTED", "上游重定向被拒绝")
		}
		if len(via) > 0 && origin(r.URL) != origin(via[0].URL) {
			r.Header.Del("Authorization")
			r.Header.Del("Cookie")
			r.Header.Del("remix-userid")
			r.Header.Del("remix-userkey")
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			return http.ErrUseLastResponse
		}
		return nil
	}}
}
func Close(client *http.Client) {
	if t, ok := client.Transport.(*transport); ok {
		t.base.CloseIdleConnections()
	}
}
func Resolve(base, ref string) (string, error) {
	u, e := url.Parse(base)
	if e != nil {
		return "", e
	}
	v, e := url.Parse(ref)
	if e != nil {
		return "", e
	}
	target := u.ResolveReference(v)
	if origin(u) != origin(target) || target.User != nil {
		return "", domain.E("UPSTREAM_URL_REJECTED", "上游链接越过配置边界")
	}
	return target.String(), nil
}
func Read(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(&limitedBody{body: resp.Body, left: MetadataLimit})
}
func CheckStatus(resp *http.Response, channel string) error {
	switch resp.StatusCode {
	case 401, 403:
		return domain.E("AUTH_EXPIRED", "上游认证失效或权限不足")
	case 429:
		return domain.Temporary("RATE_LIMITED", "上游请求频率受限")
	case 513, 517:
		return domain.E("CAPTCHA_REQUIRED", "书源要求人工验证")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domain.Temporary(channel+"_UNAVAILABLE", "上游请求未成功")
	}
	return nil
}
