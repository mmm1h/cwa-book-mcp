# 当前方案与进度

本文件是唯一架构与进度入口。当前用户指令优先；不保留历史方案副本。

## 目标与边界

Agent 保存并读取实际 EPUB/PDF；仅有链接、NAS 路径或 CWA ready 不代表文件已交付。默认下载并加入 CWA，也可仅下载，随后在缓存有效期内补入库。CWA 是唯一持久书库，负责转换、入库与重复处理，不修改其源码、数据库或生产配置。

TypeScript strict、ESM、Node.js 24 LTS 推荐，最低 22.19。原生 npm/npx 分发，stdio 是唯一 MCP 传输。授权文件 HTTP 与 MCP 共用进程，仅在本地附件上传或显式网关交付时使用。不引入其他运行时、外部数据库、队列或管理页面。内存不作为选栈门槛，文件流、并发和临时磁盘仍受控。

## 原文件交付

1. search_books：保持 CWA 中文 → 书源中文 → CWA 其他 → 书源其他。失败/未完成搜索不等于无结果。
2. download_book：显式选择 Z 候选及原文件格式；默认 save_to_cwa:true。候选身份经认证 API 核对，下载文件使用匿名请求，流式落盘后完成格式、长度与摘要校验。
3. get_task.download.status 为 available 后，get_download_url 默认返回书源原文件链接；Agent 使用现有 HTTP 工具保存并读取，无需自行处理账号、匹配和常规校验。
4. 同一暂存文件自动交给 CWA；入库状态独立报告。转换、入库或确认失败不隐藏已验证的原文件链接。
5. 仅下载时传 save_to_cwa:false；在本地复用期限内可 collect。复用不会再次下载书源。

链接通过认证 API 获取，传给 Agent 的文件 GET 不使用账号会话。拒绝包含已知账号/会话信息的链接；读取 URL 中可识别的绝对 expires 时间，未知时 expires_at:null。available_until 取本地复用窗口和已知上游到期的较早者，不保证上游持续可用；cache_until 单独控制补入库的文件缓存。重复取链接不申请新票；取票结果不明、过期和缓存丢失不自动重新消耗额度。

此路径不新增 NAS 端口、MCPHub 路由、CF 隧道或部署服务。客户端须具有 HTTP 文件保存/读取能力。CWA 独有书或转换后的文件继续使用客户端已配置认证的 OPDS，或显式可达的签名网关；不会暗中换成另一个书源版本。

## 模块与恢复

CLI/config → MCP → catalog/tasks → CWA/source；SQLite WAL 保存任务、幂等请求、短期候选与下载能力链接，文件位于私有暂存目录。状态独立于 npm 缓存和工作目录；进程独占目录，OS 锁在强杀后释放。EOF、SIGINT、SIGTERM 关闭 worker、传输和监听。

下载与入库分别表示：download.available 是原文件已验证，link_issued 是已提供链接，ready 只表示已验证 CWA 的 ID/UUID、元数据和目标格式。程序不能凭这些状态宣称 Agent 已保存。

CWA 上传由既有单 worker 串行执行。副作用前持久化取票/提交意图，重启不盲目重放。完整下载文件可恢复校验，残片清理；提交结果未知先查询书库。相同标题多条匹配进入 needs_review，由调用方明确选择。只有实际从 CWA 验证目标格式后才 ready。

仅下载缓存默认 15 分钟，过期清理；自动入库文件复用至提交后清理，未完成旧输入按保留期处理。CWA 文件验证使用独立临时副本，传输结束删除。不建立另一份持久书库。只处理 EPUB/PDF 整文件，不实现自定义分块、译本隔离或元数据写回。

## 当前交付

| 阶段     | 状态 / 验收要求                                                                             |
| -------- | ------------------------------------------------------------------------------------------- |
| 方案     | 已确认：直达原文件、默认自动入库、可仅下载/补入库                                           |
| 实现     | 六工具、独立状态、匿名原文件链接、缓存复用与恢复                                            |
| 本地验证 | 类型/lint/格式、行为/stdio、实际 tarball 空缓存                                             |
| 真实书源 | EPUB/PDF 不同出口匿名重用与摘要/额度观察；不等同于所有 CDN 或目标 NAS 已验收                |
| 发布     | 0.3.0 已通过 OIDC 发布到 latest，公共包空缓存与六工具复验通过；证据见 validation 与 Release |

[接口契约](docs/contracts.md) · [配置](docs/configuration.md) · [当前验证](docs/validation.md)

运行时使用官方 MCP SDK、Zod、better-sqlite3、Undici、tough-cookie、saxes、cheerio、yauzl。SQLite 的 N-API 预编译文件随包依赖提供，禁用安装脚本也需可启动；glibc 至少 2.34，libstdc++ 至少 GLIBCXX_3.4.29。CI 覆盖 Node 22/24、六种桌面/服务器平台与 Linux musl，不沿用旧实现的结果。
