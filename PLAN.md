# 当前方案与进度

本文件是唯一架构与进度入口。重建保留 Git 历史和仓库地址，工作树不保留旧实现或历史方案。

## 技术方向

TypeScript strict、ESM、tsc 编译；官方 MCP SDK、Zod、better-sqlite3、Undici、tough-cookie、saxes、cheerio、yauzl。Node 24 LTS 推荐，最低 22.19。npm 发布编译后的 JS 与 npm-shrinkwrap.json，不使用安装后下载器或编译器，不捆绑原生模块。

better-sqlite3 使用随包分发的 N-API 预编译文件。目标 Windows/macOS/Linux 的 x64/arm64 与 Linux musl；glibc 系统至少 2.34、libstdc++ 至少提供 GLIBCXX_3.4.29。实际平台结果见 validation。内存不是技术栈选型门槛；流式文件、事件循环、吞吐与泄漏仍需验证。

## 模块和数据流

CLI/config → MCP 工具 → catalog/tasks → CWA/source；files 与 MCP 共用进程。只有 stdio MCP，无 HTTP MCP 服务。HTTP 只提供签名文件上传/下载。任务 SQLite WAL、临时文件与票据密钥位于私有用户数据目录，不依赖 npm 缓存或工作目录。

任务通过 SQLite 事务先登记幂等请求；副作用前写入提交意图。CWA 上传串行，下载与 HTTP 文件流有并发上限。提交结果未知进入核对，不自动再次提交。只有从 CWA 拉取并验证目标格式文件才可 ready。相同标题多条匹配进入人工选择。CWA ID 和 UUID 必须一起核对。

进程独占状态目录，持久数据与所有权锁使用独立 SQLite 文件；操作系统锁随强杀释放。EOF、SIGINT、SIGTERM 退出并释放 HTTP/worker。崩溃恢复已提交任务为核对，完整暂存文件可恢复，残缺文件删除。固定文件端口允许重启后未过期票据继续使用；动态端口需刷新链接。

中文优先按 CWA 中文、书源中文、CWA 其他、书源其他搜索。分页、错误和缺少认证都明确返回，不把不完整搜索当作零结果。书源支持邮箱密码及已有 remix 会话二选一；自动更新清单、无凭据探测、缓存与有限故障切换，固定入口可选。同源登录、有限重新认证；认证 API 返回的公网 HTTPS 下载与重定向自动接入，跨源不转发认证。

## 范围和验收

完整范围：搜索、书源获取、本地上传、CWA 下载、持久任务、失败恢复与原生 npm 分发。EPUB/PDF 整文件流传输，校验长度、哈希和基础格式。CWA 唯一持久书库，负责转换/重复处理。写入默认禁用；不自动配置或部署生产服务。无译本隔离、元数据写回、分块续传、额外运行时或外部数据库。

| 阶段           | 当前状态                                                    | 退出条件                                                |
| -------------- | ----------------------------------------------------------- | ------------------------------------------------------- |
| 工程与文档重建 | 完成；本地 47 个行为测试通过                                | 六份当前文档、CLI/stdio、配置、SQLite、锁定依赖         |
| CWA 和文件闭环 | 完成；隔离转换及目标 stdio 上传、入库、下载与重启通过       | 搜索/读写/验证/幂等/强杀恢复，EPUB 入库与 PDF→EPUB 转换 |
| 书源闭环       | 完成；真实自动入口获取、目标 CWA 入库与下载、书库复用通过   | 两种认证及同源 RPC、搜索/获取、书库复用、错误边界       |
| 分发与客户端   | 完成；20 项 CI、MCPHub/CLI 与空缓存 tarball 通过            | Node 22/24、六个平台及 Linux musl，实际 stdio 接入      |
| 发布           | 0.1.0 已在 latest 发布，公共 registry 安装与 stdio 验收通过 | 后续可配置 OIDC；默认安装与发布均使用 latest            |
| OPDS 直接交付  | 目标 CWA 直连通过；MCP 工具改造待实现                       | 默认直连、可选网关及客户端认证下载验收                  |

当前验收的提交、CI 链接与资源观测统一见 [验证结果](docs/validation.md)。发布工作流要求待发布提交的完整 CI 成功，再校验公共 registry 包的完整性及 stdio 启动。

验证包括失去上传响应、强杀、重复请求、过期票据、身份变化、权限错误、中文与空格路径、stdout 纯协议、无凭据日志；资源结果记录观测值，不沿用 Go 资源门槛。需要真实书源会话和生产接入的项目独立标注，不能用模拟通过替代。

## OPDS 直接交付评估（待实现）

2026-10-16，目标 CWA 原有 HTTPS 入口的 OPDS 搜索和整文件下载已验证通过；不需要增加 OPDS 服务、端口或反代。当前 Cwa.open 已使用这个入口，额外文件入口来自本程序的签名转发层。npm 0.1.0 的 get_download_url 仍返回该转发层地址，尚未提供直连模式。

下一版拟采用以下方案，当前接口仍以 [contracts](docs/contracts.md) 为准：

- get_download_url 增加 delivery:opds|gateway，默认 opds。直连返回 acquisition URL、CWA 网页书籍入口和认证要求；支持认证下载的阅读器或 Agent 使用 OPDS，浏览器通过 CWA 网页登录后下载。复用 CWA_BASE_URL，保留反代子路径；相对 acquisition 链接按 feed URL 解析并验证同源。
- 返回值不包含密码、Authorization、Cookie 或 URL 用户信息。MCP 进程 env 中的认证不会自动传给客户端。OPDS 使用客户端自行保存的 CWA Basic 认证，远程传输使用 HTTPS；网页 Cookie 与 OPDS Basic 分别处理。客户端只支持短时授权链接时显式选择 gateway，并配置可达的文件入口。
- ready 和发放入口前仍核对 CWA ID/UUID、目标格式并实际流式校验文件。直连结果用 verification 记录此次观察的时间、长度和 SHA-256；普通 CWA URL 遵循账号权限，不是任务级授权，不带虚构的 expires_at，也不保证之后仍为同一版本。文件可能被删除、替换或重新导出；需要每次 GET 都由本程序核对身份时使用 gateway。
- 签名网关保留现有用途、身份绑定与限时授权。upload_book 的本地整文件 PUT 继续使用网关；OPDS Acquisition 不提供该上传能力。书源获取到 CWA 和直连下载不需要对外开放本程序文件端口。

OPDS feed 的 length 只作提示；实测提示 348,685 字节，实际下载 348,728 字节。以此次 HTTP 响应长度、完整流计数、格式和摘要为准，不以 feed 的长度、updated 或 mtime 判断文件完整性或未变化。CWA 导出可以嵌入元数据，不能要求源文件、目录声明与导出文件摘要固定一致。

实现验收须覆盖上述差异、相对链接/反代子路径、401/权限错误/404、缺格式、ID/UUID 变化、登录 HTML 与截断文件、跨源重定向不携带认证，以及直连/gateway 的兼容性。目标 CWA 已实测；真实阅读器、浏览器下载交互和远端 Agent 自动认证保存仍须分别验收。MCP 的 resource_link 可承载入口，但不保证客户端自动鉴权或保存文件。

依据：[OPDS 1.2 Acquisition](https://specs.opds.io/opds-1.2)、[CWA v4.0.8 下载路由](https://github.com/crocodilestick/Calibre-Web-Automated/blob/3ce7bf80c8c66485eec7218c6b6c3fef8e6e6ba5/cps/opds.py#L681)、[KOReader 认证下载实现](https://github.com/koreader/koreader/blob/e551601fc43cf775c11069ec09301d26edb015fc/plugins/opds.koplugin/opdsbrowser.lua#L1358)、[MCP resource_link](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)。实测与上游问题边界见 [validation](docs/validation.md#opds-直连验证与上游边界)。
