package zlib

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/testfixture"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func session(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "session.json")
	if e := os.WriteFile(p, []byte(`{"cookies":{"remix_userid":"synthetic","remix_userkey":"synthetic"}}`), 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestResolvedCDNReceivesNoCredentials(t *testing.T) {
	var data bytes.Buffer
	testfixture.PDF(&data, 1024)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("remix-userkey") != "" || r.Header.Get("Authorization") != "" {
			t.Error("credentials sent to initial CDN URL")
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Write(data.Bytes())
	}))
	defer cdn.Close()
	base := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/file") {
			json.NewEncoder(w).Encode(map[string]any{"success": 1, "file": map[string]any{"downloadLink": cdn.URL}})
		} else {
			io.WriteString(w, `{"success":1,"book":{"id":"7","hash":"synthetic","title":"合成测试书","language":"zh","extension":"pdf"}}`)
		}
	}))
	defer base.Close()
	c := New(base.URL, session(t), "eapi", []string{base.URL, cdn.URL})
	defer c.Close()
	resp, e := c.Open(context.Background(), domain.Book{ID: "7", Ref: "7:synthetic", Title: "合成测试书", Language: "zh", Formats: []string{"pdf"}})
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	if _, e = io.Copy(io.Discard, resp.Body); e != nil {
		t.Fatal(e)
	}
}
func TestEAPIPageArrayIsBounded(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"success":1,"books":[`+strings.Repeat(`{},`, 50)+`{}],"pagination":{"total_pages":1}}`)
	}))
	defer s.Close()
	c := New(s.URL, session(t), "eapi", []string{s.URL})
	defer c.Close()
	if _, e := c.Search(context.Background(), domain.Query{Query: "book"}, ""); e == nil {
		t.Fatal("oversized books array accepted")
	}
}
