# Agent 工作说明

先读本文件和 [PLAN.md](PLAN.md)，按任务读取配置、接口或验证文档。当前用户指令优先；旧提交、旧记忆和旧 Go 方案不是需求来源。

- Node.js + TypeScript，原生 npm 包，stdio 是唯一 MCP 传输。授权文件 HTTP 接口与 MCP 共用进程。
- 默认安装与 npm 发布渠道为 latest；仅在明确要求预览版本时使用 next。
- CWA 是唯一持久书库，负责转换、入库与重复处理；不改其源码、数据库、内部脚本或自动修改配置。
- 中文优先顺序：CWA 中文 → 书源中文 → CWA 其他 → 书源其他。失败或未完成搜索不能当作无结果。
- 仅在 CWA 身份与目标格式文件均验证通过后进入 ready。上传结果不明先核对，不自动重传。
- EPUB/PDF，流式整文件 PUT/GET；不做分块续传、译本隔离、书名改写、标签注入或元数据写回。
- 不引入其他应用运行时、外部数据库、队列、浏览器自动化或管理页面。SQLite 为本地状态；无固定 RSS 预算，仍须控制并发、文件流和临时存储。
- 不提交凭据、真实书籍、私有日志、运行时状态或真实部署域名。样例使用 localhost、example.invalid 和占位符。
- 不调用 OpenViking remember 工具或任何 namespaced 变体。
- 不自动改生产 NAS、CWA、MCPHub；隔离测试可用原版 CWA 容器，它不是应用分发方式。
- 默认 main 聚焦提交。完成阶段同步 PLAN.md 和 docs/validation.md；只记录当前实现的实际证据，不沿用旧实现测试结论。
- 文档只保留 README.md、AGENTS.md、PLAN.md、docs/configuration.md、docs/contracts.md、docs/validation.md；许可证说明可单列。不保留历史文档副本。

接口改动读 [contracts](docs/contracts.md)，配置/打包读 [configuration](docs/configuration.md)，验证读 [validation](docs/validation.md)。运行 npm run check，行为改动补充有价值的回归测试；发布前测试实际 tarball 和干净缓存启动。
