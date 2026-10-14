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

## P1 服务、存储与生命周期

`go test ./... -timeout 45s` 通过。任务数据库重新打开后保留幂等结果；20 个
并发相同请求只建一个任务；同键不同参数拒绝；精确源 ID 任务合并；第 33 个
活跃任务拒绝，取消释放名额；候选 TTL 和历史清理通过。

同一 HTTP 服务注册五个工具、Bearer 验证、用途受限票据、健康检查；MCP
客户端退出后后台继续，第二个 SDK 客户端取得同一任务并从 CWA 替身下载。
默认一个 worker、两个文件许可、八个控制许可；等待核对只保存轮询时间。

代码、HTTP 替身测试与直接 SDK 客户端测试已落地；真实环境边界见前文。

## P2 搜索与读取

`internal/catalog` 的八项优先级矩阵、CWA 故障不回退、绑定条件的分页游标均
通过。`internal/cwa` 真实模板形状的 75 条 feed 分两页返回，第二页从 51 开始；
UUID 详情核验、Basic 鉴权、HTML 登录页识别通过。书源仅启用 EAPI，搜索数组
解码时上限 50，来源 URL/凭据不会出现在 MCP 结果中。

`internal/server` 通过五工具直连调用、短期 CWA 链接、实际 GET 哈希一致、Range
拒绝、票据篡改/到期、HTML 冒充文件拒绝。原版 CWA 测试另行记录。

## P3 受控闭环与恢复

`internal/jobs` 通过本地上传与书源获取到 CWA 替身的闭环；输入 EPUB、目标 PDF
字节不同，仍按输出格式完整校验后 ready。HTTP 200 未入库不会 ready；未知
提交重启后只核对，上传计数保持一次；多个候选进入 needs_review，resolve 后
保留 user_resolved；原子文件已发布而 queued 未落库时可恢复。

`go test -race ./internal/store ./internal/files ./internal/jobs ./internal/catalog
./internal/server -timeout 120s` 在 Windows amd64 / LLVM MinGW 20261006 下通过。
Linux/arm64 race 及原版 CWA 通过隔离 acceptance workflow 执行，结果待补。
