package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/catalog"
	"github.com/mmm1h/cwa-book-mcp/internal/jobs"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type blockingBackend struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingBackend) Search(ctx context.Context, _ catalog.Input) (any, error) {
	b.entered <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
		return map[string]any{"candidates": []any{}}, nil
	}
}
func (*blockingBackend) Acquire(context.Context, jobs.AcquireInput) (json.RawMessage, error) {
	return nil, nil
}
func (*blockingBackend) Upload(jobs.UploadInput) (json.RawMessage, error) { return nil, nil }
func (*blockingBackend) Task(context.Context, string) (any, error)        { return nil, nil }
func (*blockingBackend) DownloadURL(context.Context, string, string, string) (any, error) {
	return nil, nil
}

func TestSharedToolsBoundConcurrentBackendCalls(t *testing.T) {
	b := &blockingBackend{entered: make(chan struct{}, 8), release: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	ss, e := NewServer(b, "test").Connect(ctx, serverTransport, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	c := sdk.NewClient(&sdk.Implementation{Name: "synthetic-hub", Version: "test"}, nil)
	sess, e := c.Connect(ctx, clientTransport, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer sess.Close()
	// The SDK injects metadata into CallToolParams. Each concurrent call owns
	// its params rather than sharing a mutable request object.
	params := func() *sdk.CallToolParams {
		return &sdk.CallToolParams{Name: "search_books", Arguments: map[string]any{"query": "合成书"}}
	}
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, e := sess.CallTool(ctx, params())
			done <- e
		}()
	}
	for i := 0; i < 8; i++ {
		select {
		case <-b.entered:
		case <-ctx.Done():
			t.Fatal("eight calls did not reach backend")
		}
	}
	r, e := sess.CallTool(ctx, params())
	close(b.release)
	if e != nil || !r.IsError {
		t.Fatal("ninth backend call was not rejected", r, e)
	}
	data, e := json.Marshal(r.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	var result struct {
		Error struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if e = json.Unmarshal(data, &result); e != nil || result.Error.Code != "BUSY" || !result.Error.Retryable {
		t.Fatal("backpressure lost structured error", result, e)
	}
	for i := 0; i < 8; i++ {
		if e = <-done; e != nil {
			t.Fatal(e)
		}
	}
	// Released slots are usable by later tools.
	if r, e = sess.CallTool(ctx, params()); e != nil || r.IsError {
		t.Fatal("control slots leaked", r, e)
	}
}
