# 开发验证记录

环境：2026-10-14，Windows amd64。Go 1.27.2 官方 ZIP 的 SHA-256：
`1314008898bd40df77af4b014f777f08873dbdfbcd3d92308728ee03304fe04f`。
工具链仅安装于被忽略的 `.tools/go`，未改动系统安装或现有部署。

## P0 依赖与协议

- 官方 MCP SDK v1.8.0、bbolt v1.5.0；Go 1.27.2。固定版本写入 go.mod/go.sum。
- 书源 MIT 核心固定 `6a299fdc59927f7e84f94390c4e54bfaf4d06cee`，最小适配见
  [PATCHES](../third_party/zlib/PATCHES.md)。`go list -deps ./internal/httpx`
  未发现 bubbletea、cobra、lipgloss 或 charmbracelet 依赖。
- `go test ./internal/httpx ./internal/mcp -v -timeout 30s` 通过：40 ms context
  实际取消底层书源请求，解压后超 4 MiB 响应被拒绝，跨 origin 重定向被拒绝。
- 官方 SDK Streamable HTTP 客户端完成 initialize、tools/list、tools/call，
  协商协议 `2026-07-28`。这证明直接 SDK 互通，不证明目标 MCPHub 互通。
- 初始输入与输出只支持 EPUB/PDF；PDF 检查签名与 EOF，EPUB 检查有界 ZIP
  目录、CRC、mimetype、container 与 OPF。属于基本完整性检查。
- CWA 固定源码 `/opds/search?query=` 返回完整 feed。实现通过流式条目偏移
  提供有界本地分页，同时支持上游 next 链接；不修改 CWA。

当前机器没有 Docker，也没有可用 WSL 发行版。隔离原版 CWA 上传/转换闭环、
Linux RSS/cgroup 验收、arm64 实测、目标 NAS/MCPHub/Agent 与书源真实账号测试
尚未执行。HTTP 替身及 Windows 测量不能替代这些证据。
