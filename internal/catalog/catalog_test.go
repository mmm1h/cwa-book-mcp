package catalog

import (
	"context"
	"fmt"
	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/store"
	"testing"
)

type fake struct {
	pages []domain.Page
	err   error
	calls int
}

func (f *fake) Search(ctx context.Context, q domain.Query, cursor string) (domain.Page, error) {
	f.calls++
	if f.err != nil {
		return domain.Page{}, f.err
	}
	i := 0
	if cursor != "" {
		fmt.Sscan(cursor, &i)
	}
	if i >= len(f.pages) {
		return domain.Page{}, nil
	}
	return f.pages[i], nil
}
func book(id, lang string) domain.Book {
	return domain.Book{ID: id, Title: "书名", Language: domain.Language(lang), Formats: []string{"epub"}}
}
func TestPriorityMatrix(t *testing.T) {
	for _, test := range []struct {
		name, language string
		cwa, src       []domain.Book
		sourceErr      error
		want           string
		partial        bool
		sourceCalls    int
	}{
		{"cwa chinese", "", []domain.Book{book("1", "Chinese")}, []domain.Book{book("2", "zh")}, nil, "cwa_zh", false, 0},
		{"source chinese beats local english", "", []domain.Book{book("1", "en")}, []domain.Book{book("2", "zh-Hant")}, nil, "zlibrary_zh", false, 1},
		{"local english after confirmed no chinese", "", []domain.Book{book("1", "en")}, []domain.Book{book("2", "en")}, nil, "cwa_other", false, 1},
		{"external english", "", nil, []domain.Book{book("2", "en")}, nil, "zlibrary_other", false, 1},
		{"unknown title is not chinese", "", []domain.Book{book("1", "")}, nil, nil, "cwa_other", false, 1},
		{"explicit en", "en", []domain.Book{book("1", "zh"), book("2", "eng")}, nil, nil, "cwa", false, 0},
		{"explicit any", "any", []domain.Book{book("1", "en")}, []domain.Book{book("2", "zh")}, nil, "cwa", false, 0},
		{"source failure is partial", "", []domain.Book{book("1", "en")}, nil, domain.E("AUTH_EXPIRED", "test"), "cwa_other", true, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, e := store.Open(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			cwa := &fake{pages: []domain.Page{{Books: test.cwa}}}
			src := &fake{pages: []domain.Page{{Books: test.src}}, err: test.sourceErr}
			c := &Catalog{cwa, src, s}
			r, e := c.Search(context.Background(), Input{Query: domain.Query{Query: "书名", Language: test.language}})
			if e != nil || r.Tier == nil || *r.Tier != test.want || r.Partial != test.partial || src.calls != test.sourceCalls {
				t.Fatalf("result=%+v error=%v source calls=%d", r, e, src.calls)
			}
		})
	}
}
func TestCWAErrorNeverFallsBack(t *testing.T) {
	s, _ := store.Open(t.TempDir())
	defer s.Close()
	src := &fake{}
	c := &Catalog{&fake{err: domain.E("CWA_UNAVAILABLE", "test")}, src, s}
	if _, e := c.Search(context.Background(), Input{Query: domain.Query{Query: "book"}}); e == nil || src.calls != 0 {
		t.Fatal(e, src.calls)
	}
}
func TestCursorAdvancesAndBindsFilters(t *testing.T) {
	s, _ := store.Open(t.TempDir())
	defer s.Close()
	c := &Catalog{&fake{pages: []domain.Page{{Books: []domain.Book{book("1", "zh"), book("2", "zh")}, Next: "1"}, {Books: []domain.Book{book("3", "zh")}}}}, &fake{}, s}
	a := Input{Query: domain.Query{Query: "book"}, Limit: 1}
	r, e := c.Search(context.Background(), a)
	if e != nil || r.Next == "" || r.Candidates[0].ID != "1" {
		t.Fatal(r, e)
	}
	a.Cursor = r.Next
	r, e = c.Search(context.Background(), a)
	if e != nil || r.Candidates[0].ID != "2" {
		t.Fatal(r, e)
	}
	a.Cursor = r.Next
	a.Query.Author = "different"
	if _, e = c.Search(context.Background(), a); domain.AsError(e).Code != "CURSOR_INVALID" {
		t.Fatal(e)
	}
}
