# 计划与进度

本目录保存需求、设计、开发顺序和交接。这里是唯一进度入口；详细任务在 [实施计划](05-implementation.md)，验收条件在 [验收](06-validation.md)。

**当前阶段：文档初始化及开发前复核完成，应用开发尚未开始。** 没有运行中的 MCP、部署产物或已测内存数字。

## 阅读顺序

首次接手：根目录 [AGENTS.md](../AGENTS.md) → [范围](00-scope.md) → [架构](01-architecture.md) → [交接](07-agent-handoff.md)。随后只读本阶段需要的专题。

| 文档 | 回答的问题 |
| --- | --- |
| [00-scope.md](00-scope.md) | 做什么、中文怎么优先、明确不做什么 |
| [01-architecture.md](01-architecture.md) | 模块、控制与文件流、状态与恢复 |
| [02-stack-and-memory.md](02-stack-and-memory.md) | 为什么用 Go、资源限制如何验收 |
| [03-contracts.md](03-contracts.md) | MCP 工具、文件接口和错误语义 |
| [04-cwa-integration.md](04-cwa-integration.md) | 如何使用原版 CWA，如何判断可下载 |
| [05-implementation.md](05-implementation.md) | 分阶段任务、依赖和退出条件 |
| [06-validation.md](06-validation.md) | 哪些测试证明需求已满足 |
| [07-agent-handoff.md](07-agent-handoff.md) | 接手者从哪里开始，如何交付 |

## 阶段状态

| 阶段 | 状态 | 退出证据 |
| --- | --- | --- |
| 文档初始化 | 完成 | 本目录、docs 导航、边界与验收定义 |
| 开发前文档复核 | 完成 | 单文件协议、匹配信息、恢复/清理、格式门槛与验收已对齐；见 [决策 D08](../docs/decisions.md) |
| P0：依赖与集成风险验证 | 未开始 | 待补 |
| P1：Go 服务、存储和 MCP 基础 | 未开始 | 待补 |
| P2：CWA 读取与中文优先搜索 | 未开始 | 待补 |
| P3：服务端获取、本地上传、入库核对 | 未开始 | 待补 |
| P4：资源验收、打包与操作文档 | 未开始 | 待补 |
| P5：目标实例端到端验收 | 未开始 | 待补 |

## 已确定与待测

已确定：Go、单服务、CWA 唯一书库、中文优先、不修改 CWA、不处理译本隔离、低内存门槛。首版采用流式整文件上传，每个任务只等待一个目标格式。

待测：heartleo/zlib 固定版本的内存与超时边界、隔离原版 CWA 的真实闭环、目标 CWA 版本与配置、MCPHub 协议兼容、目标 Agent 的文件能力、跨 Cloudflare 的实际传输限制。待测项不影响先做本地模拟开发。

最新范围已替代早期讨论中的“版本标题＋任务标签”方案。相关研究只用于解释边界，不重新带入开发范围。
