# 运行与维护

程序、五个 MCP 工具、整文件 HTTP 接口和持久任务已实现。验收结果见
[验证记录](validation.md)，阶段状态只在 [计划入口](../plan/README.md) 维护。
开发不会自动修改现有 NAS/CWA/MCPHub；以下配置由操作者执行。

## 构建与检查

固定 Go 1.27.2。Windows 本次工具链在被忽略的 `.tools/go/bin/go.exe`；其他机器将相同 Go 版本放入 PATH。

```sh
go vet ./...
go test ./... -timeout 90s
go test -race ./cmd/cwa-book-mcp ./internal/config ./internal/mcp ./internal/store ./internal/files ./internal/jobs ./internal/catalog ./internal/server ./internal/source/zlib -timeout 120s
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o dist/cwa-book-mcp-linux-amd64 ./cmd/cwa-book-mcp
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o dist/cwa-book-mcp-linux-arm64 ./cmd/cwa-book-mcp
```

运行只需一个 Go 二进制，无额外数据库或转换器；构建机器需要 Go，MCPHub 运行机器不需要安装 Go。更新依赖后执行
`go run ./tools/notices` 重建[第三方许可证](../third_party/licenses/README.md)，复核书源最小补丁。

打包两个 Linux 架构的二进制、MCPHub 示例、运行文档、许可证和 SHA-256 清单：

```sh
go run ./tools/package -version dev -out dist
```

发布或私有部署时将 version 换成确认过的代码版本。tar.gz 内的主程序权限为 0755；固定 Go、源码和 version 时使用 `-trimpath -buildvcs=false` 与固定归档时间生成可重复产物。开发构建与测试证据见 [构建记录](releases/development-acebc97.md)。

## MCPHub 直接启动（推荐）

1. 将对应 MCPHub 运行平台的 Linux amd64/arm64 归档解压到 MCPHub 能执行的位置，例如 `/opt/cwa-book-mcp`。归档内主程序名为 `cwa-book-mcp`；可运行 `--version` 检查。使用已有二进制时无需 Docker 或 Go 运行时。
2. 将 [mcphub.json](../examples/mcphub.json) 合并到 MCPHub 的 `mcpServers`。替换 command 的绝对路径、CWA 地址、账号/密码、私有 state/staging 目录和 PUBLIC_BASE_URL；args 为 `["--transport=stdio"]`，perSessionClient 保持 false。
3. MCPHub 保存并启用服务后，通过 stdio 管道完成 initialize、tools/list、tools/call。先保持 CWA_WRITE_ENABLED=false，用现有 CWA 书测试 search_books、get_download_url 和实际文件 GET；新增书籍仍需先核对 CWA 格式规则后启用写入。

command 和 env 中的路径均属于 **MCPHub 的运行环境**。若 MCPHub 原本就在容器中，程序和可写 state/staging 目录需要出现在那个已有容器内；宿主机路径不会自动可见。这种方式仍只新增本项目的一个 Go 子进程，无需另建本项目容器。state/staging 使用绝对路径且由运行 MCPHub 的账号读写，避免依赖其工作目录；备份时同时保存 state 中的 ticket-key。

stdio 支持通过 CWA_PASSWORD 直接设置服务端密码，也支持 CWA_PASSWORD_FILE；同一凭据只能选一种形式。MCP token 不需要配置，未显式提供 TICKET_KEY/TICKET_KEY_FILE 时，程序在私有 STATE_DIR 自动创建并复用 0600 的 ticket-key。stdout 只输出 MCP JSON-RPC，日志写 stderr；错误不会打印密码或密钥。

同一进程还会监听 LISTEN_ADDR，提供短期授权文件 PUT/GET 与健康检查，stdio 模式不暴露 HTTP /mcp。PUBLIC_BASE_URL 必须是 **Agent 设备能够访问的文件入口**，可通过已有反向代理转发到该监听地址；含路径前缀时需剥离前缀。添加 stdio 服务解决工具连接，文件入口的可达性仍需配置和验证。

MCPHub 共享一个上游子进程，设备断线不会停止后台任务。MCPHub 停止服务、关闭 stdin 或重启时，本进程退出、文件接口关闭，持久任务在下次启动恢复。不要为各设备设置 perSessionClient=true；一个状态库只允许一个进程打开。

MCPHub 的 command/args/env 与 perSessionClient 字段以[官方配置说明](https://docs.mcphub.app/configuration/mcp-settings)为依据；目标实例的版本与完整连接仍需 P5 验收。原生 Windows 使用同版本 Go 构建 `.exe` 并填写该环境内的 Windows 绝对路径。

## 可选 HTTP 模式配置

复制 `.env.example` 为被忽略的 `.env`，填写 CWA 内网地址、专用账号、PUBLIC_BASE_URL、格式和两段通路上限；先保持 `CWA_WRITE_ENABLED=false`。
PUBLIC_BASE_URL 是设备文件入口；含路径前缀时反向代理需剥离前缀再转发。

MCP token 与票据密钥各使用独立、至少 32 字节的随机字符串。Linux 示例：

```sh
mkdir -p secrets
openssl rand -hex 32 > secrets/mcp-token
openssl rand -hex 32 > secrets/ticket-key
# 操作者将 CWA 专用密码写入 secrets/cwa-password，不放入 shell 参数或 Git。
chmod 600 secrets/mcp-token secrets/ticket-key secrets/cwa-password
```

Compose 的非 root UID/GID 为 10001。Linux 上三个只读秘密文件必须对该 UID 可读，例如由操作者将文件所有者设为 10001:10001。具名 state/staging 卷按该 UID 初始化；自选 bind mount 也需检查权限。

```sh
docker compose build
docker compose up -d
docker compose ps
```

Compose 仅新增本项目服务：128 MiB、禁用 swap、只读根文件系统、无额外能力，挂载本服务 state/staging 和三个秘密文件。无需 CWA 书库、Docker socket 或 SSH 密钥。
示例只向 localhost 发布端口；操作者将 MCPHub 和现有反向代理接入可达网络，确认 CWA Docker 服务名可解析。BuildKit 自动选择宿主目标平台，支持在 amd64/arm64 主机原生构建。原生二进制也可运行，配置文件路径后启动。`--version` 查看版本，`--healthcheck` 只访问当前 LISTEN_ADDR 的 /healthz；/readyz 检查本服务状态库，不代表上游认证有效。

## 实际配置键

| 键 | 默认 / 约束 |
| --- | --- |
| MCP_TRANSPORT / --transport | http；推荐 MCPHub args 显式指定 --transport=stdio，命令行优先于环境变量 |
| LISTEN_ADDR | :8080；stdio 时为文件监听，http 时 MCP 和文件共用监听 |
| PUBLIC_BASE_URL | http://localhost:8080；设备可达文件入口 |
| STATE_DIR / STAGING_DIR | state / staging；本服务私有目录 |
| MCP_TOKEN / MCP_TOKEN_FILE | HTTP 模式必填，stdio 不使用；32..4096 字节，二选一 |
| TICKET_KEY / TICKET_KEY_FILE | 32..4096 字节，二选一；HTTP 必填，stdio 可省略并自动保存到 STATE_DIR |
| CWA_BASE_URL | 必填；优先内网 |
| CWA_USERNAME | 必填；专用账号 |
| CWA_PASSWORD / CWA_PASSWORD_FILE | 必填；密码值或密码文件，二选一 |
| CWA_WRITE_ENABLED | false；操作者确认 CWA 规则后启用 |
| CWA_EXPECTED_FORMAT | epub；创建任务时固定 |
| CWA_INPUT_FORMATS / CWA_OUTPUT_FORMATS | epub,pdf；按实际转换、保留和忽略规则收紧 |
| CWA_CONFIRM_WINDOW | 15m；大于 0、不超过 1h，从上传结束开始 |
| MAX_FILE_BYTES | 536870912；还受 CWA 通路上限减 65536 字节约束 |
| MAX_STAGING_BYTES | 4294967296；含预留、.part、源文件和验证副本 |
| CWA_MAX_UPLOAD_BYTES | MAX_FILE_BYTES + 65536；整个 multipart 上限 |
| ZLIB_DOMAIN / ZLIB_SESSION_FILE | 可留空以只用 CWA；可信入口及私有会话文件 |
| ZLIB_MODE | eapi；首版只启用已验证通路 |
| ZLIB_FILE_ORIGINS | 可空；逗号分隔的额外精确 origin，仅填经核实的书源 CDN |
| GOMEMLIMIT | MCPHub 示例和 Compose 为 64MiB；不是 RSS 硬限制 |
| CWA_DEBUG_METRICS_FILE | 默认禁用；测试时写私有 heap/goroutine JSON，无 HTTP debug 端点 |

客户端不能传任意 URL、服务器路径或源站凭据。上游只访问配置的 origin，最多五次重定向，跨 origin 不转发认证头和 Cookie。书源元数据串行；压缩前/解压后各至多 4 MiB，每次 10 秒，控制查询总计 30 秒。文件最长 30 分钟、停滞 60 秒，复制缓冲固定 64 KiB。

## 书源会话

服务消费已有合法会话，不保存书源账号密码，不自动处理验证码。会话文件至多 64 KiB，权限 0600，格式如下；值只是占位符：

```json
{"cookies":{"remix_userid":"<SOURCE_USER_ID>","remix_userkey":"<SOURCE_USER_KEY>"}}
```

MCPHub stdio 接入可将 ZLIB_DOMAIN 和 ZLIB_SESSION_FILE 加入 env，文件路径必须在 MCPHub 运行环境中可读；可只配置 CWA，后续再启用书源。Compose 的会话路径为 `/data/state/zlib-session.json`。会话失效后操作者安全替换文件；适配器每次操作重新读取，无须向设备发凭据。认证、验证码或额度错误使任务 blocked，恢复后显式 retry。未配置书源时 CWA 仍可读；需要书源却不可用时返回 partial 和原因，不声称没有中文。

## MCPHub 与文件操作

[mcphub.json](../examples/mcphub.json) 为推荐的 stdio 启动配置；[mcphub-http.json](../examples/mcphub-http.json) 用于已有独立 HTTP 进程，需替换 Bearer。按实际 MCPHub 版本确认协议与运行环境；示例不能作为目标兼容证据。

- 现有 CWA 书：search_books → 选候选/已有格式 → get_download_url → 完整 GET。
- 新书：search_books → 用户选择 → acquire_book(start) → get_task → ready 后取 CWA 链接。
- 本地书：upload_book(prepare) → 按返回 url/headers 完整 PUT → get_task。

PUT 必须带 Content-Length 和 application/octet-stream；失败从头传。响应丢失先 get_task；完整文件已接收时重复 PUT 不覆盖、不重复提交。下载不支持 Range。代理访问日志须脱敏签名路径/查询字符串，链接不进入共享文档或代理缓存；上游 Cookie 不转发给设备。

## 启用写入与大文件

操作者记录实际 CWA 版本、搜索/下载/上传权限、转换目标、保留/忽略格式、自动合并、元数据与清理设置；填写真实输入/输出声明和两段完整请求上限后才启用 CWA_WRITE_ENABLED。用可丢弃合成书验证上传、转换和下载及首个 Agent 的文件能力。程序不调整 CWA 配置；首版没有译本隔离或事务性不覆盖保证。

流式传输不能绕过代理限制。设备→服务为完整 PUT，服务→CWA 为完整 multipart，后者预留 64 KiB。应用默认 512 MiB 不表示外网允许。若已确认某段只有 100 MB，可将 MAX_FILE_BYTES 收紧到 94371840（90 MiB），第二段上限也按实际填写。CWA 内网地址只解决第二段。实际限制重新核对[Cloudflare 官方说明](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/4xx-client-error/error-413/)。

## 升级、备份、恢复

停止本服务后复制 STATE_DIR 与对应完整 STAGING_DIR 到私有备份，不在写入中直接复制 bbolt。CWA 书库由其现有运维独立备份。schema_version 不兼容时启动失败并保留旧库，一个 state.db 只允许一个实例。

stdio 原生升级：在 MCPHub 中停用该条目，备份 state/staging 和票据密钥，替换二进制，再启用条目。票据密钥文件损坏时启动失败，不会悄悄重建导致所有已发票据失效。

重启清理 .part、验证已发布成品并补记 queued；submitting/uncertain/reconciling 保留截止时间、先查库。只备份数据库导致输入缺失时：设备文件重新 prepare，源文件可重新获取，但可能已提交任务仍先核对。取消不删除 CWA 记录。
任务最长 24 小时，含 awaiting_upload/blocked/needs_review；终态最多 1000 条、最长 7 天，幂等记录随历史清理。ready/failed/cancelled 暂存文件在结束或下次启动清理。

| 故障 | 操作 |
| --- | --- |
| AUTH_EXPIRED / CAPTCHA_REQUIRED / QUOTA_EXCEEDED | 恢复合法会话/权限，再显式 retry |
| WRITES_DISABLED | 操作者确认规则后启用写入 |
| HTTP 200 未 ready / UPLOAD_OUTCOME_UNKNOWN | 检查 CWA，保持核对，不重复上传 |
| AMBIGUOUS_CWA_MATCH / IMPORT_UNCONFIRMED | 选择任务候选，或重新搜索 CWA 后 resolve |
| LENGTH_MISMATCH / CHECKSUM_MISMATCH | .part 已清理，重新完整 PUT |
| FILE_TOO_LARGE / 413 | 区分两段通路，收紧字节上限 |
| STORAGE_FULL / BUSY | 取消不需要的任务、处理磁盘余量或等待 |
| CWA 记录被删/HTML 错误页/无权限 | 报告失效，不回退到书源或暂存 |
| RSS/OOM | 保存私有摘要，分析资源生命周期，不提高 128 MiB 门槛 |
| MCPHub command 找不到 / 权限错误 | 检查 MCPHub 运行环境内的程序绝对路径、平台架构与执行权限 |
| cannot listen on LISTEN_ADDR | 选择同一运行环境中未占用的端口，同步文件入口转发 |
| 工具可调用但文件 URL 不可达 | 检查 PUBLIC_BASE_URL、LISTEN_ADDR 和已有反向代理，直接验证 GET/PUT |

## 复现验收

真实书源账号不进入公共 CI。操作者准备隔离原版 CWA、安全设置 CWA_BASE_URL/CWA_USERNAME/CWA_PASSWORD_FILE 后，显式运行：

```sh
CWA_INTEGRATION=1 go test -tags integration ./internal/integration -v -timeout 10m
```

测试只上传原创 EPUB/PDF，不调整 CWA 设置。acceptance workflow 使用固定版本原版 CWA 和独立临时书库，结束删除测试容器，不连接现有部署。

```sh
docker build --build-arg TARGETARCH=amd64 -t cwa-book-mcp:acceptance .
go run ./tools/bench -docker-image cwa-book-mcp:acceptance -work .work/bench-new -report .work/resources.json -idle 5m -stable 30m -sequential 100 -history 1000
go run ./tools/check-report .work/resources.json
```

使用新的 -work 目录。报告分别记录 RSS/HWM、heap、goroutine、FD、cgroup current/peak/max/anon/file、memory.events、OOM 与哈希。Windows WorkingSet/handles 不替代 Linux/容器证据；arm64 在原生 runner 实测。

负载包括停滞 PUT 取消、部分上传后强制崩溃、32 任务积压及第 33 个拒绝、两个文件许可占用时立即拒绝额外 GET、未知提交结果后重启只查库；与三档文件流使用同一个受限服务。控制突发的 BUSY 单独计数并至多重试三次，其他错误使验收失败。代码提交、内核、逻辑 CPU 数与状态库体积记录在原始 JSON；普通构建的测量不混入 race 开销。

## 目标实例验收所需输入

本轮完成的是仓库实现与隔离验收，尚未配置或部署现有 NAS、CWA、MCPHub。将以下输入保存在私有 `.env`、秘密文件与验收记录中，不提交仓库：

| 输入 | 用途 |
| --- | --- |
| 目标架构、CWA 版本与内网地址 | 选择产物并只读探测 OPDS、UUID 详情与下载兼容 |
| CWA 专用账号与权限 | 读取、下载与网页上传；密码只放服务端秘密文件 |
| 当前转换、保留/忽略、合并与清理规则 | 填写输入/输出格式和目标格式声明；程序不调整规则 |
| Z-Library 可信入口、合法会话与必要 CDN origin | 验证真实搜索、下载与账号阻塞，显式启用，不在公共 CI 消耗额度 |
| MCPHub 实际版本、程序位置与启动环境（可选 HTTP 的 Bearer） | 验证 stdio command/args/env 和聚合后的 initialize/tools/list/tools/call |
| PUBLIC_BASE_URL、两段完整请求上限与代理超时 | 验证 Agent PUT/GET 和 Cloudflare 大文件通路 |
| 首个具备文件 I/O 的 Agent 及第二设备 | 验证本地上传、断线后后台继续、跨设备查任务与保存文件 |

操作者确认真实环境测试授权后，先保持写入关闭完成只读探测，再用可丢弃原创测试书验收上传、转换和下载。实际部署的版本、设置、内存和外网限制须记录；隔离结果不证明生产环境已通过。
