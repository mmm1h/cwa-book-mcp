package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStreamableHTTPBaseline(t *testing.T) {
	s := sdk.NewServer(&sdk.Implementation{Name: "cwa-book-mcp", Version: "test"}, nil)
	type Input struct {
		Query string `json:"query"`
	}
	type Output struct {
		Value string `json:"value"`
	}
	sdk.AddTool(s, &sdk.Tool{Name: "probe", Description: "确定性协议实验"}, func(ctx context.Context, r *sdk.CallToolRequest, a Input) (*sdk.CallToolResult, Output, error) {
		return nil, Output{a.Query}, nil
	})
	h := sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server { return s }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(h)
	defer ts.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := sdk.NewClient(&sdk.Implementation{Name: "synthetic-agent", Version: "test"}, nil)
	sess, e := c.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: ts.URL}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer sess.Close()
	list, e := sess.ListTools(ctx, nil)
	if e != nil || len(list.Tools) != 1 {
		t.Fatal(list, e)
	}
	result, e := sess.CallTool(ctx, &sdk.CallToolParams{Name: "probe", Arguments: map[string]any{"query": "合成书"}})
	if e != nil || result.IsError {
		t.Fatal(result, e)
	}
	t.Logf("initialize/tools/list/tools/call passed; protocol=%s", sess.InitializeResult().ProtocolVersion)
}
