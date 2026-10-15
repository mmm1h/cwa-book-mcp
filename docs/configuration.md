# 配置与运行

推荐 Node.js 24 LTS，最低 22.19。Windows/macOS/Linux 的 x64/arm64 为验收目标，Linux 同时覆盖 glibc 和 musl。glibc 预编译 SQLite 要求 glibc ≥2.34、GLIBCXX_3.4.29；旧 NAS 应在已有 MCPHub 的现代 Node 环境启动。实际通过的平台见 [validation](validation.md)。

## 启动与客户端

发布后：`npx -y cwa-book-mcp@0.1.0`。默认就是 stdio，无 transport 参数。开发阶段先 `npm ci && npm run build`，配置相同环境变量后运行 `node dist/cli.js`。本地 tarball 可使用 `npx -y --package ./cwa-book-mcp-0.1.0.tgz cwa-book-mcp`。

- [标准 MCP 配置](../examples/local.json)：支持本地进程的桌面/CLI 客户端。
- [MCPHub](../examples/mcphub.json)：在 Hub 主机安装 Node，用 command/args/env 导入，`perSessionClient:false` 共享单个子进程。单设备断开不终止共享任务。
- [VS Code](../examples/vscode.json)：使用 `servers` 外层，放入工作区的 .vscode/mcp.json。
- Claude Code：`claude mcp add-json cwa-books '<单个 server 对象>'`。Windows 上如某客户端不能直接启动 npx，使用 `command:"cmd"`、`args:["/d","/s","/c","npx","-y","cwa-book-mcp@0.1.0"]`。
- 手机/网页客户端连接远端 MCPHub；它们不在本机执行 npx。Hub 若运行于容器，文件端口与 CWA 网络需在该容器中可达。

首次 npx 会下载 npm 包；离线首次启动不可用。固定版本便于升级回退。应用发布编译后的 JS，运行用户不需要 TypeScript 编译器、Go、Python 或本项目容器。生产依赖无安装后下载器，安装脚本禁用也经过 tarball 验证。

## 环境变量

| 变量                                | 默认值 / 用途                                                        |
| ----------------------------------- | -------------------------------------------------------------------- |
| CWA_BASE_URL                        | 必填；CWA 根 URL，可含反代子路径                                     |
| CWA_USERNAME、CWA_PASSWORD          | 必填；具有 OPDS 读取权限的账号                                       |
| CWA_WRITE_ENABLED                   | false；由操作者确认 CWA 设置后设 true                                |
| STATE_DIR                           | 自动私有用户数据目录；必须为本地可持久化磁盘                         |
| INSTANCE_ID                         | default；多个独立客户端使用不同值                                    |
| FILE_HOST、FILE_PORT                | 127.0.0.1、0（动态端口）                                             |
| PUBLIC_BASE_URL                     | 本机默认自动生成；远端 Agent 可达的文件入口 URL                      |
| ZLIBRARY_BASE_URL                   | 未设置则关闭书源；由操作者提供可信同源入口                           |
| ZLIBRARY_EMAIL、ZLIBRARY_PASSWORD   | 邮箱密码登录方式，须成对设置                                         |
| ZLIBRARY_USER_ID、ZLIBRARY_USER_KEY | 已有 remix 会话方式，须成对设置；与邮箱方式互斥                      |
| ZLIBRARY_FILE_ORIGINS               | 额外下载 origin 的逗号列表，如 https://example.invalid；不支持通配符 |
| MAX_FILE_BYTES                      | 2147483648（2 GiB），范围 1 KiB–16 GiB                               |
| MAX_STAGING_BYTES                   | 8589934592（8 GiB），包括暂存与验证文件、并发预留                    |
| TRANSFER_TIMEOUT_SECONDS            | 1800，整个文件传输截止时间；单次无数据等待另限 30 秒                 |
| METADATA_TIMEOUT_SECONDS            | 30，单次元数据请求截止时间                                           |
| CONFIRM_TIMEOUT_SECONDS             | 600，提交后等待 CWA 的核对窗口                                       |
| POLL_INTERVAL_SECONDS               | 5，后台核对间隔                                                      |
| TICKET_TTL_SECONDS                  | 900，上传/下载授权有效期                                             |
| RETENTION_DAYS                      | 30，完成任务与幂等记录保留期；未提交旧任务过期                       |

CWA_USERNAME、CWA_PASSWORD 和四个 ZLIBRARY 认证变量均支持同名 `_FILE`。文件为 UTF-8 文本，移除末尾换行；同一变量与其 _FILE 不能同时提供。只在运行环境保存凭据。日志不会输出凭据、书源下载地址或授权票据。

书源自动缓存已验证的会话，不保存密码。认证失效只尝试一次重新登录；CAPTCHA、额度限制和不兼容响应明确返回错误。跨源文件请求必须在精确允许列表内，且不转发 Cookie、Authorization、remix 或自定义认证头。

## CWA 与文件入口

先保证标准 OPDS 搜索和下载可用。写入前确认账号上传权限、标准登录/CSRF、允许的 EPUB/PDF 输入、目标转换格式与重复处理设置；本程序不会自动修改它们。转换属于 CWA；已有记录缺少目标格式时会受阻，不通过覆盖已有格式触发转换。

远端使用固定 FILE_PORT 和 PUBLIC_BASE_URL，例如 `https://example.invalid/cwa-books`。反代转发至本进程的文件端口，可以保留或去掉该前缀；须允许完整 PUT/GET、准确请求长度和足够的上传大小/超时。PUBLIC_BASE_URL 与动态端口不能混用。文件链接中的 localhost 永远指向访问者自身，不能直接交给其他设备。

文件接口只接受限时签名链接。下载前先把 CWA 文件流式验证到私有临时目录，再流式传给调用者，因此需要磁盘空间，首字节有验证延迟。验证文件随后删除；本进程不建立另一份持久书库。单次整文件传输不支持 Range 或断点续传，Agent 需具有实际 PUT/GET 文件能力。

## 状态、诊断与恢复

默认位置：Windows 的 LOCALAPPDATA、macOS 的 Library/Application Support、Linux 的 XDG_DATA_HOME（或 ~/.local/share），下设 cwa-book-mcp/v1/实例哈希。哈希由 CWA URL、用户名、INSTANCE_ID 决定，与密码和 npm 缓存无关。明确 STATE_DIR 时也会校验实例身份。每个目录只允许一个进程；MCPHub 共享进程，独立客户端隔离目录。

状态包含 tasks.sqlite、owner.sqlite、ticket.key、可选 source-session.json 和 staging。任务库使用 WAL；独立 SQLite 事务提供进程锁，强杀后由 OS 释放。Unix 目录权限 0700，密钥/会话 0600；Windows 依赖用户目录 ACL，自定义共享路径须由操作者限制访问。

`cwa-book-mcp diagnose` 只读取 CWA 搜索接口，不创建状态、不写入 CWA、不登录书源；成功仅表示读取通路可用。默认 stdio 模式只在 stderr 输出脱敏错误。常见配置错误包含 CONFIG_REQUIRED、STATE_IN_USE、FILE_PORT_IN_USE、SQLITE_UNAVAILABLE。

EOF、SIGINT、SIGTERM 会关闭本进程。重启后核对已提交任务、恢复已完整落盘但尚未更新数据库的文件，并清理残片。上传结果未知的任务永不自动重新提交；retry 是继续核对。确认失败时检查 CWA，必要时由操作者取消并重新创建任务。

固定端口且密钥未变时，重启后未过期票据仍有效。动态端口重启后须刷新链接。ticket.key 损坏或丢失会拒绝启动，不能静默生成新密钥。备份时先停进程，再备份整个状态目录；不在运行中只复制 tasks.sqlite。无旧 Go 状态迁移。

已完成/失败/取消任务按保留期删除，幂等保证也以该保留期为界。未提交过期任务清理输入；已提交且无法确认的任务保留待人工处理，输入暂存可清除，因为不会自动重传。未完成任务上限 1000，候选/游标缓存上限 10000、有效期 30 分钟。

## 打包与发布

`npm run check` → `npm run test:package` → `npm pack`。包包含 dist、用户文档、许可证和 npm-shrinkwrap.json，不含源码、测试文件、凭据或 Go 产物。改依赖后运行 `node scripts/notices.mjs` 更新许可证。

当前未发布。维护者首次需要 `npm login`，包名 cwa-book-mcp 尚未预留；冲突时改为维护者 scope，并同步示例。先发布 next 通道并从公共 registry 验证，再推广稳定版本。若同一 0.1.0 已发布到 next，使用 `npm dist-tag add cwa-book-mcp@0.1.0 latest` 推广，不能覆盖同版本包。

首次发布后在 npm 为本仓库的 publish.yml 配置 GitHub Actions trusted publisher，并建立 npm environment。后续手动工作流使用 OIDC/provenance，发布前要求当前提交的完整 acceptance 工作流成功，不保存长期 npm 令牌。真实书源和目标部署的剩余验收仍须按 [validation](validation.md) 完成。

参考：[npm trusted publishers](https://docs.npmjs.com/trusted-publishers)、[npm shrinkwrap](https://docs.npmjs.com/cli/v11/configuring-npm/npm-shrinkwrap-json)、[MCPHub 配置](https://docs.mcphub.app/configuration/mcp-settings)、[VS Code MCP](https://code.visualstudio.com/docs/copilot/customization/mcp-servers)。
