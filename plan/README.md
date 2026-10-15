# 计划与进度

本目录保存需求、设计、开发顺序和交接。这里是唯一进度入口；详细任务在 [实施计划](05-implementation.md)，验收条件在 [验收](06-validation.md)。

**当前阶段：HTTP 基线 P0–P4 完成；MCPHub 直接启动的 stdio 修正已完成本地验证；P5 待目标环境。** 推荐在 MCPHub 配置 command/args/env 启动单个 Go 二进制，无需本项目容器。原版 CWA 闭环与 Linux 双架构完整资源负载的历史证据属于 HTTP 基线。

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
| P0：依赖与集成风险验证 | 完成 | [验证记录](../docs/validation.md)、[书源补丁](../third_party/zlib/PATCHES.md) |
| P1：Go 服务、存储和 MCP 基础 | 完成 | [验证记录 P1](../docs/validation.md)、internal/store、internal/server 测试 |
| P2：CWA 读取与中文优先搜索 | 完成 | [验证记录 P2](../docs/validation.md)、internal/catalog 与 internal/server 测试 |
| P3：服务端获取、本地上传、入库核对 | 完成 | [验证记录 P3](../docs/validation.md)、原版 CWA v4.0.8 隔离测试 |
| P4：资源验收、打包与操作文档 | 完成 | [双架构资源报告](../docs/benchmarks/README.md)、[构建与校验值](../docs/releases/development-acebc97.md)、[运行文档](../docs/operations.md) |
| MCPHub 原生 stdio 接入修正 | 本地开发验证完成；Linux 双架构构建完成 | [stdio 验证记录](../docs/validation.md#mcphub-stdio-接入修正)、[MCPHub 配置](../examples/mcphub.json)、[构建记录](../docs/releases/development-stdio-20261015.md) |
| P5：目标实例端到端验收 | 待操作者安全配置与部署授权 | [所需输入与验收方法](../docs/operations.md#目标实例验收所需输入) |

## 已确定与待测

已确定：Go、单服务、CWA 唯一书库、中文优先、不修改 CWA、不处理译本隔离、低内存门槛。首版采用流式整文件上传，每个任务只等待一个目标格式。

用户于 2026-10-15 明确要求直接添加到 MCPHub。已补充 --transport=stdio、环境变量凭据、自动持久票据密钥和共享进程退出恢复；默认 MCPHub 示例已改为 stdio，HTTP/Compose 为可选兼容和隔离测试方式，见 [决策 D11](../docs/decisions.md#d11mcphub-直接启动-stdio容器交付改为可选)。

已验证：固定书源核心的取消、响应上限与流传输；直接 SDK 协议互通；原版 CWA v4.0.8 的 EPUB 导入和 PDF→EPUB 转换；双架构 128 MiB 容器的 1 GiB 传输、100 个顺序任务、1000 条历史、故障恢复与 30 分钟稳定性。对应应用源码为 `acebc97`，详细方法和原始数据链接见退出证据。

待验收：目标 CWA 版本与配置、真实书源会话及额度、MCPHub 实际 stdio 启动与聚合兼容、目标 Agent 的文件能力、跨 Cloudflare 的实际传输限制。新增 stdio 模式未重跑 Linux 双架构完整资源长测，既有资源数字不作为其新证据。代码开发不会自动修改现有部署。

最新范围已替代早期讨论中的“版本标题＋任务标签”方案。相关研究只用于解释边界，不重新带入开发范围。
