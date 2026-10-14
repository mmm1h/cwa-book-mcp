# cwa-book-mcp

让多设备上的 Agent 通过 MCPHub 搜书、获取新书、上传本地书籍，并统一从 Calibre-Web Automated（CWA）取书。

**当前只有规划与交接文档，没有应用代码、可运行镜像或性能实测结果。** 开发进度以 [plan/README.md](plan/README.md) 为准。

默认中文优先：CWA 中文 → Z-Library 中文 → CWA 其他语言 → Z-Library 其他语言。搜索与获取分开，用户选定书籍后才下载。

新书在服务端下载并提交给 CWA。转换和入库由 CWA 完成；所有交付给设备的书籍都来自 CWA。本项目只暂存文件和保存任务记录。

## 已确定的边界

- 一个 Go 服务、一个部署组件，接入现有 MCPHub。
- 不使用 Python 作为应用、辅助服务或开发工具链依赖。
- 不修改、分叉或注入 CWA 源码，不直接写入 Calibre 数据库。
- 首版不处理译者、版次隔离，不改写书名或嵌入任务标签。
- 文件流式传输到磁盘，任务持久化，客户端断开后任务继续。
- 内存是发布门槛：空闲 RSS ≤ 32 MiB、规定负载峰值 RSS ≤ 96 MiB，128 MiB 容器内通过测试。这些是目标，**尚未实测**；口径见 [内存预算](plan/02-stack-and-memory.md)。

## 从这里开始

| 你要做什么 | 入口 |
| --- | --- |
| 接手开发 | [AGENTS.md](AGENTS.md) → [计划与进度](plan/README.md) → [开发交接](plan/07-agent-handoff.md) |
| 看需求和开发边界 | [范围与行为](plan/00-scope.md) |
| 看服务怎么组成 | [架构](plan/01-architecture.md) |
| 看技术栈与低内存约束 | [技术栈与内存](plan/02-stack-and-memory.md) |
| 查某项接口或验收要求 | [文档导航](docs/README.md) |

这里描述的是待实现的设计。MCPHub 统一配置工具入口；文件传输使用同一服务签发的临时地址，Agent 仍需具备本地文件读写及 HTTP 能力。

许可证：[MIT](LICENSE)。通过 HTTP 使用的 CWA 和其他上游项目保留各自许可证。
