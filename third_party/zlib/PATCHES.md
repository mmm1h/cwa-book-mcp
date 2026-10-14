# 固定书源核心与最小补丁

来源：heartleo/zlib，MIT，提交 `6a299fdc59927f7e84f94390c4e54bfaf4d06cee`。
仅复制根目录核心 Go 文件及 LICENSE；不包含 CLI/TUI、脚本或原仓库测试。
原文件可在 `.work/zlib-upstream` 与此目录比较，所有改动如下：

- `cwa_adapter.go`：允许注入 HTTP client、操作 context，暴露流式下载响应。
- 元数据请求的 `c.httpClient.Do(req)` 改为 `c.doHTTP(req)`，实际请求受调用 context 取消。
- `client.go`：增加操作 context；遇到 challenge 直接返回错误，不运行挑战求解。
- go.mod 只保留核心实际导入的 goquery，运行时不链接 CLI/TUI。

响应压缩前/解压后上限、10 秒元数据截止时间、60 秒读写停滞、固定 origin
及重定向限制由本项目 `internal/httpx` 注入。任务层统一有限重试，不使用
上游 Download 的命名、写盘与自动重试路径。此补丁不修改 CWA。
