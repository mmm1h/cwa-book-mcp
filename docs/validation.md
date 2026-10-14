# 开发验证记录

日期：2026-10-14。开发环境为 Windows amd64；隔离 CI 使用原生 Linux amd64/arm64。Go 1.27.2 官方 Windows ZIP 的 SHA-256：
`1314008898bd40df77af4b014f777f08873dbdfbcd3d92308728ee03304fe04f`。
工具链仅安装于被忽略的 `.tools/go`，未改动系统安装或现有部署。

## P0 依赖与协议

- 官方 MCP SDK v1.8.0、bbolt v1.5.0；Go 1.27.2。固定版本写入 go.mod/go.sum。
- 书源 MIT 核心固定 `6a299fdc59927f7e84f94390c4e54bfaf4d06cee`，最小适配见
  [PATCHES](../third_party/zlib/PATCHES.md)。`go list -deps ./cmd/cwa-book-mcp`
  未发现 bubbletea、cobra、lipgloss 或 charmbracelet 依赖。
- `go test ./internal/httpx ./internal/mcp -v -timeout 30s` 通过：40 ms context
  实际取消底层书源请求，解压后超 4 MiB 响应被拒绝，跨 origin 重定向被拒绝。
- 官方 SDK Streamable HTTP 客户端完成 initialize、tools/list、tools/call，
  协商协议 `2026-07-28`。这证明直接 SDK 互通，不证明目标 MCPHub 互通。
- 初始输入与输出只支持 EPUB/PDF；PDF 检查签名与 EOF，EPUB 检查有界 ZIP
  目录、CRC、mimetype、container 与 OPF。属于基本完整性检查。
- CWA 固定源码 `/opds/search?query=` 返回完整 feed。实现通过流式条目偏移
  提供有界本地分页，同时支持上游 next 链接；不修改 CWA。

当前 Windows 机器没有 Docker 或可用 WSL；隔离原版 CWA、双架构 Linux 和 Compose 测试在 GitHub 原生 runner 上执行。
[检查运行](https://github.com/mmm1h/cwa-book-mcp/actions/runs/38578199958) 对应应用源码 `acebc97`，格式、vet、全部无凭据测试、重点模块 race 和双架构构建通过。
目标 NAS/MCPHub/Agent、书源真实账号与外网通路仍待操作者验收，受控环境不替代这些证据。

## P1 服务、存储与生命周期

`go test ./... -timeout 45s` 通过。任务数据库重新打开后保留幂等结果；20 个
并发相同请求只建一个任务；同键不同参数拒绝；精确源 ID 任务合并；第 33 个
活跃任务拒绝，取消释放名额；候选 TTL 和历史清理通过。

补充回归：过期源任务清理后，新任务的精确去重索引仍有效；SDK 类型化输出保留业务错误的结构化 code/message/retryable。

同一 HTTP 服务注册五个工具、Bearer 验证、用途受限票据、健康检查；MCP
客户端退出后后台继续，第二个 SDK 客户端取得同一任务并从 CWA 替身下载。
默认一个 worker、两个文件许可、八个控制许可；等待核对只保存轮询时间。

代码、HTTP 替身测试与直接 SDK 客户端测试已落地；真实环境边界见前文。

## P2 搜索与读取

`internal/catalog` 的八项优先级矩阵、CWA 故障不回退、绑定条件的分页游标均
通过。`internal/cwa` 真实模板形状的 75 条 feed 分两页返回，第二页从 51 开始；
UUID 详情核验、Basic 鉴权、HTML 登录页识别通过。书源仅启用 EAPI，搜索数组
解码时上限 50，来源 URL/凭据不会出现在 MCP 结果中。

前三页都被作者或格式筛选排除时，保留 CWA/书源后续位置，下一次调用可找到第四页的匹配书；已经查完的渠道不重查。双渠道继续扫描仍遵守中文优先，书源失败不被记录为空结果；连续翻页的警告不会累积重复项。

`internal/server` 通过五工具直连调用、短期 CWA 链接、实际 GET 哈希一致、Range
拒绝、票据篡改/到期、HTML 冒充文件拒绝。原版 CWA 测试另行记录。

签发的下载票据绑定 ID/UUID；清理 1000 条候选导致缓存淘汰后，已经签发的有效链接仍能读取对应 CWA 记录。移除或替换记录仍需在 CWA 详情核验时拒绝。

## P3 受控闭环与恢复

`internal/jobs` 通过本地上传与书源获取到 CWA 替身的闭环；输入 EPUB、目标 PDF
字节不同，仍按输出格式完整校验后 ready。HTTP 200 未入库不会 ready；未知
提交重启后只核对，上传计数保持一次；多个候选进入 needs_review，resolve 后
保留 user_resolved；原子文件已发布而 queued 未落库时可恢复。

`go test -race ./internal/store ./internal/files ./internal/jobs ./internal/catalog
./internal/server ./internal/source/zlib -timeout 120s` 在 Windows amd64 / LLVM MinGW 20261006、Linux amd64 和 Linux arm64 下通过。
停滞 HTTP PUT 可被任务取消立即中断；取消与传输登记并发时不遗漏。伪造 ZIP 目录的 5000 条目文件在分配目录对象前拒绝，XML container 的 CRC 错误也被拒绝。

## 原版 CWA 集成

[隔离验收运行](https://github.com/mmm1h/cwa-book-mcp/actions/runs/38578199982) 对应 `acebc97`。使用原版 `ghcr.io/crocodilestick/calibre-web-automated:v4.0.8`，实际镜像摘要：

`sha256:5e00373854247750cc3e4479b492ae09293ff5e06ed10177f226634d97888679`。

独立临时配置、ingest、书库与测试账号；未调整 CWA 设置。原创 EPUB→EPUB 和 PDF→EPUB 均经真实网页 multipart 上传、OPDS 搜索、UUID 详情与实际文件完整性验证后 ready，CWA ID 分别为 2、3，依据为 matched_after_submission。总耗时 22.855 秒。原始证据见 [测试输出](compatibility/original-cwa-acebc97.txt)。

真实 `/me` 页面包含修改密码输入框，已据此修正登录页识别：同时出现 username/password 输入才视为登录表单，不能仅凭 password 字段判断未登录。

## P4 打包与资源

同一隔离运行的两个资源 job 已验证 Compose 配置、默认 UID/GID 10001、具名卷权限、只读根文件系统、128 MiB / 无 swap、健康检查与持久卷重启。Docker BuildKit 按目标平台选择 amd64/arm64，Compose 无需在 ARM 主机手工指定 amd64。

两架构均完成 5 分钟空闲、三档文件混合传输、100 个顺序任务、1000 条历史和 30 分钟稳定性，八项资源门槛全部通过。停滞上传取消、部分上传崩溃恢复、32 任务积压、文件接口背压与未知提交重启五项故障也均通过。最终数字、cgroup 判定方法与未改写的原始 JSON 见 [资源报告](benchmarks/README.md)。

Linux amd64/arm64 二进制、非 root 镜像和 Compose 均已验证；两个独立输出目录的归档和二进制 SHA-256 一致。构建方法及四个校验值见 [构建记录](releases/development-acebc97.md)。源码、替身、原版 CWA 和目标实例的证据等级分别保留；目标 P5 所需配置与验收步骤集中在 [运行文档](operations.md#目标实例验收所需输入)。
