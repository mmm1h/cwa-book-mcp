# cwa-book-mcp

让多设备上的 Agent 通过 MCPHub 搜书、获取新书、上传本地书籍，并统一从 Calibre-Web Automated（CWA）取书。

直接在 MCPHub 中用 `command/args/env` 添加，由 MCPHub 启动一个 Go 二进制。默认接入示例使用 stdio，无需单独部署本项目容器。

**P0–P4 的 HTTP 基线开发与隔离验收已完成，MCPHub stdio 接入已补充验证。** 五个工具、文件闭环、原版 CWA 集成及 HTTP 基线的 Linux amd64/arm64 资源门槛有证据；目标 MCPHub 实例仍待接入，见 [plan/README.md](plan/README.md)。

默认中文优先：CWA 中文 → Z-Library 中文 → CWA 其他语言 → Z-Library 其他语言。搜索与获取分开，用户选定书籍后才下载。

新书在服务端下载并提交给 CWA。转换和入库由 CWA 完成；所有交付给设备的书籍都来自 CWA。本项目只暂存文件和保存任务记录。

## 已确定的边界

- 一个 Go 服务、一个部署组件，接入现有 MCPHub。
- 不使用 Python 作为应用、辅助服务或开发工具链依赖。
- 不修改、分叉或注入 CWA 源码，不直接写入 Calibre 数据库。
- 首版不处理译者、版次隔离，不改写书名或嵌入任务标签。
- 文件流式传输到磁盘，任务持久化，客户端断开后任务继续。
- 首版支持 EPUB/PDF，写入默认关闭；由操作者核对 CWA 标准设置后启用。
- 内存发布门槛：空闲 RSS ≤32 MiB、规定负载峰值 ≤96 MiB、128 MiB 容器内完成负载。实际结果见 [资源报告](docs/benchmarks/README.md)，口径见 [内存预算](plan/02-stack-and-memory.md)。

固定 Go 1.27.2。`go vet ./...`、`go test ./... -timeout 90s`、`go build ./cmd/cwa-book-mcp` 可完成本地检查与构建。

五个工具：search_books、acquire_book、upload_book、get_task、get_download_url。stdio 使用 MCPHub 管理的标准输入输出，日志只写 stderr；可选 HTTP 模式的 /mcp 使用 Bearer。两种模式的文件接口都使用短期票据，只有实际验证到 CWA 所需格式才 ready，上传响应不代表入库成功。

将对应架构的二进制放到 MCPHub 能执行的位置，将 [mcphub.json](examples/mcphub.json) 合并到 MCPHub 配置，替换程序路径、CWA 地址/账号和文件入口即可。stdio 模式自动在 STATE_DIR 保存票据密钥，无需 MCP token。安装位置、文件入口、升级备份与故障处理见 [运行文档](docs/operations.md#mcphub-直接启动推荐)。Docker/Compose 只作为可选 HTTP 运行方式和隔离测试工具。

## 从这里开始

| 你要做什么 | 入口 |
| --- | --- |
| 接手开发 | [AGENTS.md](AGENTS.md) → [计划与进度](plan/README.md) → [开发交接](plan/07-agent-handoff.md) |
| 看需求和开发边界 | [范围与行为](plan/00-scope.md) |
| 看服务怎么组成 | [架构](plan/01-architecture.md) |
| 看技术栈与低内存约束 | [技术栈与内存](plan/02-stack-and-memory.md) |
| 查某项接口或验收要求 | [文档导航](docs/README.md) |

MCPHub 统一配置工具入口；文件传输使用同一服务签发的临时地址，Agent 仍需具备本地文件读写及 HTTP 能力。首版不支持 Range 或应用分块。

许可证：[MIT](LICENSE)、[固定第三方依赖原始许可证](third_party/licenses/README.md)。通过 HTTP 使用的 CWA 保留其许可证。
