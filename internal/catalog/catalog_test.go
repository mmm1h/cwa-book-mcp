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

func TestEmptyFilteredPagesPreserveDiscoveryPositions(t *testing.T) {
	pages := func(language string) []domain.Page {
		out := []domain.Page{}
		for i := 0; i < 4; i++ {
			b := book(fmt.Sprint(i+1), language)
			b.Authors = []string{"其他作者"}
			if i == 3 {
				b.Authors = []string{"目标作者"}
			}
			p := domain.Page{Books: []domain.Book{b}}
			if i < 3 {
				p.Next = fmt.Sprint(i + 1)
			}
			out = append(out, p)
		}
		return out
	}
	for _, tc := range []struct {
		name, language, tier string
		cwa, source          []domain.Page
		cwaCalls, srcCalls   int
	}{
		{"explicit language library", "en", "cwa", pages("en"), nil, 4, 1},
		{"preferred source Chinese", "", "zlibrary_zh", nil, pages("zh"), 1, 4},
		{"both incomplete source Chinese beats library English", "", "zlibrary_zh", pages("en"), pages("zh"), 4, 4},
		{"preferred library English after complete source", "", "cwa_other", pages("en"), nil, 4, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			cwa, src := &fake{pages: tc.cwa}, &fake{pages: tc.source}
			c := &Catalog{cwa, src, s}
			in := Input{Query: domain.Query{Query: "书名", Author: "目标作者", Language: tc.language}}
			first, err := c.Search(context.Background(), in)
			if err != nil || len(first.Candidates) != 0 || !first.Partial || first.Next == "" {
				t.Fatalf("filtered scan lost continuation: %+v, %v", first, err)
			}
			in.Cursor = first.Next
			last, err := c.Search(context.Background(), in)
			if err != nil || len(last.Candidates) != 1 || last.Tier == nil || *last.Tier != tc.tier || last.Partial || last.Next != "" {
				t.Fatalf("continuation did not find fourth page: %+v, %v", last, err)
			}
			if cwa.calls != tc.cwaCalls || src.calls != tc.srcCalls {
				t.Fatalf("completed channels rescanned: cwa=%d source=%d", cwa.calls, src.calls)
			}
		})
	}
}

func TestPaginationWarningsRemainBounded(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pages := []domain.Page{}
	for i := 0; i < 20; i++ {
		pages = append(pages, domain.Page{Books: []domain.Book{book(fmt.Sprint(i), "zh")}, Next: fmt.Sprint(i + 1), Incomplete: true})
	}
	c := &Catalog{&fake{pages: pages}, &fake{}, s}
	in := Input{Query: domain.Query{Query: "书名"}, Limit: 1}
	for i := 0; i < 20; i++ {
		out, err := c.Search(context.Background(), in)
		if err != nil || len(out.Candidates) != 1 || len(out.Warnings) != 1 || !out.Partial || out.Next == "" {
			t.Fatalf("page %d: warnings or continuation grew: %+v, %v", i, out, err)
		}
		in.Cursor = out.Next
	}
}

func TestDiscoveryDoesNotTreatFailedSourceAsCompleted(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	local := book("local", "en")
	local.Authors = []string{"目标作者"}
	cwa := &fake{pages: []domain.Page{{Next: "1"}, {Next: "2"}, {Next: "3"}, {Books: []domain.Book{local}}}}
	chinese := book("source", "zh")
	chinese.Authors = []string{"目标作者"}
	src := &fake{pages: []domain.Page{{Books: []domain.Book{chinese}}}, err: domain.E("AUTH_EXPIRED", "synthetic")}
	c := &Catalog{cwa, src, s}
	in := Input{Query: domain.Query{Query: "书名", Author: "目标作者"}}
	first, err := c.Search(context.Background(), in)
	if err != nil || !first.Partial || first.Next == "" || len(first.Candidates) != 0 {
		t.Fatal(first, err)
	}
	src.err = nil
	in.Cursor = first.Next
	last, err := c.Search(context.Background(), in)
	if err != nil || len(last.Candidates) != 1 || last.Candidates[0].ID != "source" || last.Tier == nil || *last.Tier != "zlibrary_zh" || last.Partial {
		t.Fatalf("failed source was skipped or treated as empty: %+v, %v", last, err)
	}
	if cwa.calls != 4 || src.calls != 2 {
		t.Fatal(cwa.calls, src.calls)
	}
}
