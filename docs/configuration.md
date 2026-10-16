# 配置与运行

推荐 Node.js 24 LTS，最低 22.19。Windows/macOS/Linux 的 x64/arm64 为验收目标，Linux 同时覆盖 glibc 和 musl。glibc 预编译 SQLite 要求 glibc ≥2.34、GLIBCXX_3.4.29；旧 NAS 应在已有 MCPHub 的现代 Node 环境启动。实际通过的平台见 [validation](validation.md)。

## 启动与客户端

启动：`npx -y cwa-book-mcp@latest`。默认就是 stdio，无 transport 参数。当前源码 0.2.0 尚未发布，npm latest 仍为 0.1.0；测试新行为先 `npm ci && npm run build`，配置相同环境变量后运行 `node dist/cli.js`，或使用本地 tarball：`npx -y --package ./cwa-book-mcp-0.2.0.tgz cwa-book-mcp`。

- [标准 MCP 配置](../examples/local.json)：支持本地进程的桌面/CLI 客户端。
- [MCPHub](../examples/mcphub.json)：在 Hub 主机安装 Node，用 command/args/env 导入，`perSessionClient:false` 共享单个子进程。单设备断开不终止共享任务。
- [VS Code](../examples/vscode.json)：使用 `servers` 外层，放入工作区的 .vscode/mcp.json。
- Claude Code：`claude mcp add-json cwa-books '<单个 server 对象>'`。Windows 上如某客户端不能直接启动 npx，使用 `command:"cmd"`、`args:["/d","/s","/c","npx","-y","cwa-book-mcp@latest"]`。
- 手机/网页客户端连接远端 MCPHub；它们不在本机执行 npx。Hub 需能访问 CWA；仅使用签名文件网关时才需为调用方配置其文件端口的可达入口。

首次 npx 会下载 npm 包；离线首次启动不可用。默认使用 @latest 跟随发布渠道，升级后需重启 MCP 服务；需要锁定或回退时可改为 @0.1.0 等固定版本。应用发布编译后的 JS，运行用户不需要 TypeScript 编译器、Go、Python 或本项目容器。生产依赖无安装后下载器，安装脚本禁用也经过 tarball 验证。

## 环境变量

| 变量                                | 默认值 / 用途                                             |
| ----------------------------------- | --------------------------------------------------------- |
| CWA_BASE_URL                        | 必填；Hub 与下载设备均可达的 CWA URL，可含反代子路径      |
| CWA_USERNAME、CWA_PASSWORD          | 必填；具有 OPDS 读取及下载权限的账号                      |
| CWA_WRITE_ENABLED                   | false；由操作者确认 CWA 设置后设 true                     |
| STATE_DIR                           | 自动私有用户数据目录；必须为本地可持久化磁盘              |
| INSTANCE_ID                         | default；多个独立客户端使用不同值                         |
| FILE_HOST、FILE_PORT                | 127.0.0.1、0（动态端口）                                  |
| PUBLIC_BASE_URL                     | 本机默认自动生成；本程序签名文件入口，须为 Agent 可达 URL |
| ZLIBRARY_BASE_URL                   | 可选固定入口；省略时根据已配置的书源认证自动发现与切换    |
| ZLIBRARY_EMAIL、ZLIBRARY_PASSWORD   | 邮箱密码登录方式，须成对设置                              |
| ZLIBRARY_USER_ID、ZLIBRARY_USER_KEY | 已有 remix 会话方式，须成对设置；与邮箱方式互斥           |
| ZLIBRARY_FILE_ORIGINS               | 特殊部署的额外精确 origin，逗号分隔；普通公网下载无需配置 |
| MAX_FILE_BYTES                      | 2147483648（2 GiB），范围 1 KiB–16 GiB                    |
| MAX_STAGING_BYTES                   | 8589934592（8 GiB），包括暂存与验证文件、并发预留         |
| TRANSFER_TIMEOUT_SECONDS            | 1800，整个文件传输截止时间；单次无数据等待另限 30 秒      |
| METADATA_TIMEOUT_SECONDS            | 30，单次元数据请求截止时间                                |
| CONFIRM_TIMEOUT_SECONDS             | 600，提交后等待 CWA 的核对窗口                            |
| POLL_INTERVAL_SECONDS               | 5，后台核对间隔                                           |
| TICKET_TTL_SECONDS                  | 900，上传/下载授权有效期                                  |
| RETENTION_DAYS                      | 30，完成任务与幂等记录保留期；未提交旧任务过期            |

CWA_USERNAME、CWA_PASSWORD 和四个 ZLIBRARY 认证变量均支持同名 `_FILE`。文件为 UTF-8 文本，移除末尾换行；同一变量与其 _FILE 不能同时提供。只在运行环境保存凭据。日志不会输出凭据、书源下载地址或授权票据。

只配置完整的一组书源认证即可启用；未配置认证与入口时关闭书源。首次使用自动读取 [heartleo/zlib 的维护清单](https://github.com/heartleo/zlib/blob/main/domain_policy.go)，通过无凭据的 `/eapi/info/domains` 探测选取可用入口，并接纳受信任入口公布的后续域名。清单每天按需刷新，入口失效时可提前刷新（失败后至少间隔五分钟）；维护源不可用时回退缓存与内置入口。不会抓取导航页中的任意链接作为登录目标。设置 ZLIBRARY_BASE_URL 可固定到自选入口，此时不自动切换。

书源适配在本项目内用 TypeScript 实现，参考同一上游的 EAPI/RPC 协议。域名缓存 source-domains.json 和会话缓存 source-session.json 位于 STATE_DIR；不缓存密码。并发请求共用发现与登录；搜索、详情遇到通路故障最多换一次入口，认证失效最多重新登录一次。密码错误、CAPTCHA、额度不足和业务拒绝不会触发跨站轮询。文件取票或下载响应丢失不自动重放，避免额外消耗额度。

下载跟随已认证 API 返回的公网 HTTPS 地址及其重定向，无需填写 CDN 域名。跨源不转发 Cookie、Authorization、remix 或自定义认证头，拒绝本地地址、私网 DNS 结果及 HTTPS 降级；兼容 TUN 代理的 198.18.0.0/15 Fake-IP DNS，仍校验原域名 TLS 证书。只有操作者明确配置的固定入口及精确 origin 允许列表可接入本地代理。

## CWA 与文件入口

先保证标准 OPDS 搜索和下载可用。写入前确认账号上传权限、标准登录/CSRF、允许的 EPUB/PDF 输入、目标转换格式与重复处理设置；本程序不会自动修改它们。转换属于 CWA；已有记录缺少目标格式时会受阻，不通过覆盖已有格式触发转换。

get_download_url 默认在 MCP 内部重新核对书籍 ID/UUID、目标格式与完整文件，再返回 CWA OPDS 链接 url、网页入口 web_url 和服务端校验记录 verification。客户端只需打开或保存文件，无需重复做身份、格式或摘要校验。普通 CWA 下载链接按 CWA 账号权限访问，不带本程序的过期票据。

CWA 已有入口可直接提供下载，不需为 OPDS 另开端口：

远端 Hub 的 CWA_BASE_URL 使用已有 CWA 的可达 HTTPS 地址；如填容器内部的 `http://cwa:8083`，生成的直连地址也只在该网络内可用。

| 使用方                            | CWA 入口与认证                                                                                              |
| --------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| OPDS 阅读器或支持认证下载的 Agent | 使用返回的 url，例如 `https://example.invalid/opds/download/123/epub/`，配合客户端保存的 CWA Basic 认证下载 |
| 浏览器用户                        | 打开返回的 web_url，例如 `https://example.invalid/book/123`，按正常网页登录后使用下载按钮                   |

普通网页 Cookie 在目标实例上不能用于 OPDS 下载，浏览器使用 web_url。CWA 登录认证与服务端的书籍/文件校验是两回事；MCP env 的账号不会自动转交给下载客户端，返回值也不包含密码、Authorization 或 Cookie。远端 OPDS 使用 HTTPS；示例路径需保留实际反代子路径。

upload_book 的本地整文件 PUT 和 get_download_url 的 delivery:gateway 使用签名网关。远端使用时配置固定 FILE_PORT 和 PUBLIC_BASE_URL，例如 `https://example.invalid/cwa-books`。反代转发至本进程的文件端口，可以保留或去掉该前缀；须允许完整 PUT/GET、准确请求长度和足够的上传大小/超时。PUBLIC_BASE_URL 与动态端口不能混用。文件链接中的 localhost 永远指向访问者自身，不能直接交给其他设备。

签名网关只接受限时链接。网关 GET 会先把 CWA 文件流式验证到私有临时目录，再流式传给调用者，因此需要磁盘空间，首字节有验证延迟。验证文件随后删除；本进程不建立另一份持久书库。网关单次整文件传输不支持 Range 或断点续传，Agent 需具有实际 PUT/GET 文件能力；CWA 直连的传输能力由 CWA 提供。

## 状态、诊断与恢复

默认位置：Windows 的 LOCALAPPDATA、macOS 的 Library/Application Support、Linux 的 XDG_DATA_HOME（或 ~/.local/share），下设 cwa-book-mcp/v1/实例哈希。哈希由 CWA URL、用户名、INSTANCE_ID 决定，与密码和 npm 缓存无关。明确 STATE_DIR 时也会校验实例身份。每个目录只允许一个进程；MCPHub 共享进程，独立客户端隔离目录。

状态包含 tasks.sqlite、owner.sqlite、ticket.key、可选 source-session.json 和 staging。任务库使用 WAL；独立 SQLite 事务提供进程锁，强杀后由 OS 释放。Unix 目录权限 0700，密钥/会话 0600；Windows 依赖用户目录 ACL，自定义共享路径须由操作者限制访问。

`cwa-book-mcp diagnose` 只读取 CWA 搜索接口，不创建状态、不写入 CWA、不登录书源；成功仅表示读取通路可用。默认 stdio 模式只在 stderr 输出脱敏错误。常见配置错误包含 CONFIG_REQUIRED、STATE_IN_USE、FILE_PORT_IN_USE、SQLITE_UNAVAILABLE。

若 CWA 反馈上传排队失败，get_task 的 submission_error 会保留原因；即使 HTTP 200，也继续核对书库。检查 CWA 服务日志中的 `Failed to queue upload for ingest` 及目录权限等具体异常，不通过重复上传或自动改配置处理。

日志若为 `Ingest directory not writable: /cwa-book-ingest`，检查对应宿主机卷是否读写、实际进程 UID/GID、目录及父目录 ACL。PUID/PGID 使用拥有该目录权限的真实用户数值，不假设为 1000。`NETWORK_SHARE_MODE=true` 可跳过相关 chown，但同时影响 SQLite WAL 与文件监视方式，应按实际挂载评估，且不能代替写权限。错误日志为 stdout 时须查看容器日志。

EOF、SIGINT、SIGTERM 会关闭本进程。重启后核对已提交任务、恢复已完整落盘但尚未更新数据库的文件，并清理残片。上传结果未知的任务永不自动重新提交；retry 是继续核对。确认失败时检查 CWA，必要时由操作者取消并重新创建任务。

固定端口且密钥未变时，重启后未过期票据仍有效。动态端口重启后须刷新链接。ticket.key 损坏或丢失会拒绝启动，不能静默生成新密钥。备份时先停进程，再备份整个状态目录；不在运行中只复制 tasks.sqlite。无旧 Go 状态迁移。

已完成/失败/取消任务按保留期删除，幂等保证也以该保留期为界。未提交过期任务清理输入；已提交且无法确认的任务保留待人工处理，输入暂存可清除，因为不会自动重传。未完成任务上限 1000，候选/游标缓存上限 10000、有效期 30 分钟。

## 打包与发布

`npm run check` → `npm run test:package` → `npm pack`。包包含 dist、用户文档、许可证和 npm-shrinkwrap.json，不含源码、测试文件、凭据或 Go 产物。改依赖后运行 `node scripts/notices.mjs` 更新许可证。

默认发布到 latest：交互发布执行 `npm publish --access public --tag latest`，GitHub 发布工作流也默认 latest。仅在明确要求预览版本时选择 next。同一已发布版本只调整标签，不重复发布；例如 `npm dist-tag add cwa-book-mcp@0.1.0 latest`。当前 latest 已指向 0.1.0，已有 next 标签保留兼容。

交互发布使用已验证邮箱、已启用 2FA 的账号执行 `npm login`；默认浏览器授权，终端密码登录也可能要求一次性验证。包名变更时须同步所有示例。

首次发布后在 npm 为本仓库的 publish.yml 配置 GitHub Actions trusted publisher，并建立 npm environment。后续手动工作流使用 OIDC/provenance，发布前要求当前提交的完整 acceptance 工作流成功，不保存长期 npm 令牌。在对应发布提交上，以 `PUBLISHED_PACKAGE_SPEC=cwa-book-mcp@0.1.0` 运行 `npm run test:package`，核对公共包 integrity 与本地产物一致，再验证空缓存安装、五工具发现/调用及重启。真实书源和目标部署的剩余验收仍须按 [validation](validation.md) 完成。

参考：[npm 登录](https://docs.npmjs.com/cli/v11/commands/npm-login)、[npm 2FA](https://docs.npmjs.com/about-two-factor-authentication)、[npm trusted publishers](https://docs.npmjs.com/trusted-publishers)、[npm shrinkwrap](https://docs.npmjs.com/cli/v11/configuring-npm/npm-shrinkwrap-json)、[MCPHub 配置](https://docs.mcphub.app/configuration/mcp-settings)、[VS Code MCP](https://code.visualstudio.com/docs/copilot/customization/mcp-servers)。
