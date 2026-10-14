package catalog

import (
	"context"
	"strings"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/store"
)

type Searcher interface {
	Search(context.Context, domain.Query, string) (domain.Page, error)
}
type Catalog struct {
	CWA, Source Searcher
	Store       *store.Store
}
type Input struct {
	domain.Query
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type Result struct {
	Candidates []domain.Book `json:"candidates"`
	Tier       *string       `json:"selected_tier"`
	Searched   []string      `json:"searched"`
	Partial    bool          `json:"partial"`
	Warnings   []string      `json:"warnings"`
	Next       string        `json:"next_cursor,omitempty"`
}
type cursor struct {
	Hash         string       `json:"hash"`
	Query        domain.Query `json:"query"`
	Tier         string       `json:"tier"`
	ProviderNext string       `json:"provider_next"`
	Pending      []string     `json:"pending"`
	Partial      bool         `json:"partial"`
	Warnings     []string     `json:"warnings"`
	Searched     []string     `json:"searched"`
}
type scanned struct {
	books      []domain.Book
	next       string
	incomplete bool
}

func (c *Catalog) remember(b domain.Book) (domain.Book, error) {
	b.CandidateID = domain.ID()
	e := c.Store.PutCache("candidates", b.CandidateID, b, 30*time.Minute)
	return b, e
}
func (c *Catalog) scan(ctx context.Context, p Searcher, q domain.Query, stop func(domain.Book) bool) (scanned, error) {
	out := scanned{}
	for i := 0; i < 3; i++ {
		page, e := p.Search(ctx, q, out.next)
		if e != nil {
			return out, e
		}
		found := false
		for _, b := range page.Books {
			if !domain.Filter(b, q) {
				continue
			}
			b, e = c.remember(b)
			if e != nil {
				return out, e
			}
			out.books = append(out.books, b)
			if stop(b) {
				found = true
			}
		}
		if page.Next == out.next && page.Next != "" {
			return out, domain.E("SEARCH_INCOMPLETE", "上游分页未前进")
		}
		out.next = page.Next
		out.incomplete = out.incomplete || page.Incomplete
		if out.next == "" || found {
			break
		}
	}
	return out, nil
}
func tierMatch(b domain.Book, tier string) bool {
	switch {
	case strings.HasSuffix(tier, "_zh"):
		return b.Language == "zh"
	case strings.HasSuffix(tier, "_other"):
		return b.Language != "zh"
	default:
		return true
	}
}
func (c *Catalog) Search(ctx context.Context, in Input) (Result, error) {
	q, e := in.Query.Validate()
	if e != nil {
		return Result{}, e
	}
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return Result{}, domain.E("INVALID_ARGUMENT", "limit 必须在 1..50 之间")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	state := cursor{Hash: store.Hash(q), Query: q, Searched: []string{}, Warnings: []string{}}
	if in.Cursor != "" {
		if e = c.Store.GetCache("cursors", in.Cursor, &state); e != nil {
			return Result{}, e
		}
		if state.Hash != store.Hash(q) {
			return Result{}, domain.E("CURSOR_INVALID", "游标与查询条件不符")
		}
		return c.continueSearch(ctx, state, limit)
	}
	preferred := q.Language == "zh_preferred"
	stop := func(b domain.Book) bool { return !preferred || b.Language == "zh" }
	cwa, e := c.scan(ctx, c.CWA, q, stop)
	if e != nil {
		return Result{}, e
	}
	state.Searched = append(state.Searched, "cwa")
	cwaZH := false
	for _, b := range cwa.books {
		if b.Language == "zh" {
			cwaZH = true
		}
	}
	selected := cwa
	state.Tier = "cwa"
	if preferred && cwaZH {
		state.Tier = "cwa_zh"
	} else if !preferred && len(cwa.books) > 0 {
		state.Tier = "cwa"
	} else {
		if cwa.next != "" || cwa.incomplete {
			state.Partial = true
			state.Warnings = append(state.Warnings, "cwa_search_incomplete")
		}
		source, se := c.scan(ctx, c.Source, q, stop)
		state.Searched = append(state.Searched, "zlibrary")
		if se != nil {
			state.Partial = true
			state.Warnings = append(state.Warnings, domain.AsError(se).Code+": zlibrary_search_incomplete")
		}
		if source.next != "" || source.incomplete {
			state.Partial = true
			state.Warnings = append(state.Warnings, "zlibrary_search_incomplete")
		}
		sourceZH := false
		for _, b := range source.books {
			if b.Language == "zh" {
				sourceZH = true
			}
		}
		switch {
		case preferred && sourceZH:
			state.Tier = "zlibrary_zh"
			selected = source
		case len(cwa.books) > 0:
			state.Tier = "cwa_other"
			selected = cwa
		case len(source.books) > 0:
			state.Tier = "zlibrary"
			if preferred {
				state.Tier = "zlibrary_other"
			}
			selected = source
		default:
			if se != nil && len(cwa.books) == 0 {
				return Result{Candidates: []domain.Book{}, Searched: state.Searched, Partial: true, Warnings: state.Warnings}, nil
			}
			state.Tier = ""
			selected = scanned{}
		}
	}
	state.ProviderNext = selected.next
	for _, b := range selected.books {
		if tierMatch(b, state.Tier) {
			state.Pending = append(state.Pending, b.CandidateID)
		}
	}
	return c.emit(state, limit)
}
func (c *Catalog) continueSearch(ctx context.Context, state cursor, limit int) (Result, error) {
	if len(state.Pending) < limit && state.ProviderNext != "" {
		p := c.CWA
		if strings.HasPrefix(state.Tier, "zlibrary") {
			p = c.Source
		}
		for i := 0; i < 3 && len(state.Pending) < limit && state.ProviderNext != ""; i++ {
			old := state.ProviderNext
			page, e := p.Search(ctx, state.Query, old)
			if e != nil {
				return Result{}, e
			}
			if page.Next == old && page.Next != "" {
				return Result{}, domain.E("CURSOR_INVALID", "上游游标未前进")
			}
			state.ProviderNext = page.Next
			for _, b := range page.Books {
				if domain.Filter(b, state.Query) && tierMatch(b, state.Tier) {
					b, e = c.remember(b)
					if e != nil {
						return Result{}, e
					}
					state.Pending = append(state.Pending, b.CandidateID)
				}
			}
			if page.Incomplete {
				state.Partial = true
				state.Warnings = append(state.Warnings, "search_incomplete")
			}
		}
	}
	return c.emit(state, limit)
}
func (c *Catalog) emit(state cursor, limit int) (Result, error) {
	r := Result{Candidates: []domain.Book{}, Searched: state.Searched, Partial: state.Partial, Warnings: state.Warnings}
	n := min(limit, len(state.Pending))
	for _, id := range state.Pending[:n] {
		var b domain.Book
		if e := c.Store.GetCache("candidates", id, &b); e != nil {
			return r, domain.E("CURSOR_INVALID", "分页候选已过期或被缓存上限回收")
		}
		r.Candidates = append(r.Candidates, b)
	}
	state.Pending = state.Pending[n:]
	if state.Tier != "" {
		t := state.Tier
		r.Tier = &t
	}
	if state.ProviderNext != "" {
		r.Partial = true
		r.Warnings = append(r.Warnings, "search_incomplete")
	}
	if len(state.Pending) > 0 || state.ProviderNext != "" {
		r.Next = domain.ID()
		if e := c.Store.PutCache("cursors", r.Next, state, 30*time.Minute); e != nil {
			return r, e
		}
	}
	return r, nil
}
func (c *Catalog) Candidate(id string) (domain.Book, error) {
	var b domain.Book
	e := c.Store.GetCache("candidates", id, &b)
	return b, e
}
