package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/mmm1h/cwa-book-mcp/internal/catalog"
	"github.com/mmm1h/cwa-book-mcp/internal/domain"
	"github.com/mmm1h/cwa-book-mcp/internal/jobs"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type Backend interface {
	Search(context.Context, catalog.Input) (any, error)
	Acquire(context.Context, jobs.AcquireInput) (json.RawMessage, error)
	Upload(jobs.UploadInput) (json.RawMessage, error)
	Task(context.Context, string) (any, error)
	DownloadURL(context.Context, string, string, string) (any, error)
}
type SearchInput struct {
	Query    string   `json:"query"`
	Author   string   `json:"author,omitempty"`
	Language string   `json:"language,omitempty"`
	Formats  []string `json:"formats,omitempty"`
	Cursor   string   `json:"cursor,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}
type TaskInput struct {
	ID string `json:"task_id"`
}
type DownloadInput struct {
	ID     string `json:"cwa_book_id"`
	Format string `json:"format"`
	TaskID string `json:"task_id,omitempty"`
}

func adapt[I any](controls chan struct{}, fn func(context.Context, I) (any, error)) sdk.ToolHandlerFor[I, map[string]any] {
	return func(ctx context.Context, r *sdk.CallToolRequest, a I) (*sdk.CallToolResult, map[string]any, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		select {
		case controls <- struct{}{}:
			defer func() { <-controls }()
		default:
			return &sdk.CallToolResult{IsError: true}, map[string]any{"error": domain.Temporary("BUSY", "控制请求并发已达上限")}, nil
		}
		v, e := fn(ctx, a)
		if e != nil {
			d := domain.AsError(e)
			// The typed SDK handler writes its second return value over
			// StructuredContent. Return errors through that value as well.
			return &sdk.CallToolResult{IsError: true}, map[string]any{"error": d}, nil
		}
		b, e := json.Marshal(v)
		if e != nil {
			return nil, nil, e
		}
		if len(b) > 128<<10 {
			return &sdk.CallToolResult{IsError: true}, map[string]any{"error": domain.E("RESULT_TOO_LARGE", "请缩小查询范围")}, nil
		}
		var out map[string]any
		if e = json.Unmarshal(b, &out); e != nil {
			return nil, nil, e
		}
		return nil, out, nil
	}
}

// NewServer shares the same tools and limits across stdio and HTTP transports.
func NewServer(b Backend, version string) *sdk.Server {
	s := sdk.NewServer(&sdk.Implementation{Name: "cwa-book-mcp", Version: version}, nil)
	controls := make(chan struct{}, 8)
	sdk.AddTool(s, &sdk.Tool{Name: "search_books", Description: "只搜索候选，不获取。默认中文优先：CWA 中文→书源中文→CWA 其他→书源其他。用户选择后再获取。分页需保持原查询。"}, adapt(controls, func(ctx context.Context, a SearchInput) (any, error) {
		return b.Search(ctx, catalog.Input{Query: domain.Query{Query: a.Query, Author: a.Author, Language: a.Language, Formats: a.Formats}, Cursor: a.Cursor, Limit: a.Limit})
	}))
	sdk.AddTool(s, &sdk.Tool{Name: "acquire_book", Description: "start 获取已选候选；retry 恢复阻塞；resolve 确认 needs_review 的 CWA 记录；cancel 停止本服务任务。所有 action 必填 request_id。已可能提交时只查库，不重传。"}, adapt(controls, func(ctx context.Context, a jobs.AcquireInput) (any, error) { return b.Acquire(ctx, a) }))
	sdk.AddTool(s, &sdk.Tool{Name: "upload_book", Description: "prepare 提供书名、文件名和字节数，取得完整 PUT 的临时票据；refresh_ticket 刷新 awaiting_upload 票据，须新 request_id。文件由 Agent 流式发送，后台独立入库。"}, adapt(controls, func(ctx context.Context, a jobs.UploadInput) (any, error) { return b.Upload(a) }))
	sdk.AddTool(s, &sdk.Tool{Name: "get_task", Description: "查询持久任务。ready 仅表示 CWA 所需格式已实际验证。不会刷新上传票据或自动重传。"}, adapt(controls, func(ctx context.Context, a TaskInput) (any, error) { return b.Task(ctx, a.ID) }))
	sdk.AddTool(s, &sdk.Tool{Name: "get_download_url", Description: "为近期 CWA 候选或 ready 任务签发临时完整 GET 链接；只从 CWA 交付。Agent 保存到本地，失败从头重试，不支持 Range。"}, adapt(controls, func(ctx context.Context, a DownloadInput) (any, error) {
		return b.DownloadURL(ctx, a.ID, a.Format, a.TaskID)
	}))
	return s
}

func Handler(b Backend, version string) http.Handler {
	s := NewServer(b, version)
	return sdk.NewStreamableHTTPHandler(func(r *http.Request) *sdk.Server { return s }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}
