package cwa

import (
	"bytes"
	"context"
	"fmt"
	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/testfixture"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestProfilePasswordFieldIsNotLogin(t *testing.T) {
	_, login := csrf([]byte(`<form><input name="csrf_token" value="synthetic"><input name="password" autocomplete="off"></form>`))
	if login {
		t.Fatal("profile confused with login")
	}
	_, login = csrf([]byte(`<form class="cwa-login-form"><input name="csrf_token" value="synthetic"><input name="username"><input name="password" autocomplete="current-password"></form>`))
	if !login {
		t.Fatal("login not detected")
	}
}

func TestOriginalFeedShapeAndVirtualPagination(t *testing.T) {
	var b bytes.Buffer
	testfixture.PDF(&b, 1024)
	f := testfixture.NewCWA(b.Bytes())
	defer f.Server.Close()
	for i := 1; i <= 75; i++ {
		f.Add(domain.Book{ID: fmt.Sprint(i), Title: "合成测试书", Language: "chi", Formats: []string{"pdf"}})
	}
	c := New(f.Server.URL, "synthetic", "synthetic")
	defer c.Close()
	p, e := c.Search(context.Background(), domain.Query{Query: "合成测试书"}, "")
	if e != nil || len(p.Books) != 50 || p.Next == "" || p.Books[0].ID != "1" || p.Books[0].Language != "zh" {
		t.Fatal(p, e)
	}
	next, e := c.Search(context.Background(), domain.Query{Query: "合成测试书"}, p.Next)
	if e != nil || len(next.Books) != 25 || next.Books[0].ID != "51" || next.Next != "" {
		t.Fatal(next, e)
	}
	detail, e := c.Book(context.Background(), p.Books[0])
	if e != nil || detail.ID != "1" {
		t.Fatal(detail, e)
	}
	token, e := c.PrepareUpload(context.Background())
	if e != nil || token == "" {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "input")
	os.WriteFile(path, b.Bytes(), 0600)
	may, e := c.Upload(context.Background(), path, "synthetic.pdf", token)
	if e != nil || !may || f.Uploads.Load() != 1 {
		t.Fatal(may, e, f.Uploads.Load())
	}
}
func TestHTMLAndAuthAreErrors(t *testing.T) {
	for _, status := range []int{200, 401} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(status)
				w.Write([]byte("<html>login</html>"))
			}))
			defer s.Close()
			c := New(s.URL, "synthetic", "synthetic")
			defer c.Close()
			if _, e := c.Search(context.Background(), domain.Query{Query: "book"}, ""); domain.AsError(e).Code != "AUTH_EXPIRED" {
				t.Fatal(e)
			}
		})
	}
}
