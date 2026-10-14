package httpx

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	z "github.com/heartleo/zlib"
)

func TestMetadataCancellationAndBounds(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wait" {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		_, _ = io.Copy(gz, io.LimitReader(strings.NewReader(strings.Repeat("x", int(MetadataLimit+1))), MetadataLimit+1))
		_ = gz.Close()
	}))
	defer s.Close()
	h := NewClient([]string{s.URL}, true)
	defer Close(h)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.URL+"/wait", nil)
	start := time.Now()
	_, e := h.Do(req)
	if e == nil || time.Since(start) > time.Second {
		t.Fatal("request did not cancel", e)
	}
	resp, e := h.Get(s.URL)
	if e != nil {
		t.Fatal(e)
	}
	_, e = Read(resp)
	if e == nil {
		t.Fatal("compressed response escaped limit")
	}
}
func TestUpstreamCoreHonorsContext(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer s.Close()
	h := NewClient([]string{s.URL}, true)
	defer Close(h)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	c := z.NewClient(z.WithDomain(s.URL), z.WithHTTPClient(h), z.WithRequestContext(ctx))
	c.SetCookies(map[string]string{"remix_userid": "synthetic", "remix_userkey": "synthetic"})
	c.SetMode(z.ClientModeEAPI)
	_, e := c.Search("合成书", 1, 20, nil)
	if e == nil {
		t.Fatal("uncancellable core search")
	}
}
func TestRedirectRejectsDifferentOrigin(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted server reached") }))
	defer other.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer s.Close()
	h := NewClient([]string{s.URL}, true)
	defer Close(h)
	if _, e := h.Get(s.URL); e == nil {
		t.Fatal("redirect accepted")
	}
}
