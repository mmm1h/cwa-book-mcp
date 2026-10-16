# 当前实现验证

以下仅为 Node.js/TypeScript 实现的证据，更新时间 2026-10-16。当前源码 0.2.0 已通过本地 65 项行为测试、空缓存 tarball 和目标 CWA 下载交付；尚未公开发布。npm latest 仍为 0.1.0，其完整平台、集成与客户端验收作为发布基线单独列出。

## 0.2.0 下载交付验收

Windows x64、Node 25.5.0，默认 get_download_url 返回 CWA OPDS 链接及网页入口，书籍身份与文件校验由服务端完成；客户端不承担重复校验步骤。

| 项目           | 实际结果                                                                                                                                                             |
| -------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| npm run check  | 类型、lint、格式及 65 项行为测试全部通过                                                                                                                             |
| 新增回归       | EPUB/PDF、相对 acquisition 链接、反代子路径、目录长度差异、旧任务重启、401/403/404、文件损坏/登录 HTML、书籍删除/身份改变/缺格式、链接凭据与跨源拒绝、取消及网关兼容 |
| 真实 stdio     | 默认 OPDS、显式 gateway、结构化与文本结果一致；未知 delivery 被 schema 拒绝                                                                                          |
| 实际 tarball   | 空缓存、中文/空格空目录、禁用安装脚本；默认直连和签名网关均实际下载 EPUB；两次重启保持任务                                                                           |
| 目标 CWA       | 从当前构建的真实 stdio 创建只读书库任务，服务端 ready 校验、默认 OPDS 和显式 gateway 均通过；实际文件 348,728 字节，格式及 SHA-256 一致                              |
| 认证及网页入口 | OPDS 匿名 401，Basic 下载 200；返回的网页入口匿名 302 到 CWA 登录页；结果无凭据、无伪造的直连过期时间                                                                |

本次未向 CWA 上传书籍或修改生产配置，测试文件与独立状态已清理。私有目标报告为 `.work/opds-delivery-verification/report.json`。当前构建与安装包已验证，用户 MCPHub 的升级、设备界面及新版本完整平台 CI 仍须单独确认。

## 平台与发布基线

0.1.0 发布提交 [c8db4aa](https://github.com/mmm1h/cwa-book-mcp/commit/c8db4aa2dcfd3f83451775c38ad9ac1c4f3bc160) 的 [Node acceptance](https://github.com/mmm1h/cwa-book-mcp/actions/runs/38723235707) 全部 20 项通过，覆盖当时 47 个行为测试；目标 CWA 上传和真实书源获取闭环、公共 registry 安装均已通过。

| 项目                                               | 实际结果                                                                                                  |
| -------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| Node 22.19.0 / 24，Windows/macOS/Linux × x64/arm64 | 12 项 CI 均通过类型、lint、格式、47 个行为测试及实际 tarball 安装                                         |
| Linux musl，Node 22 / 24 × x64/arm64               | 4 项 Alpine CI 均通过相同检查及安装；禁用安装脚本                                                         |
| npm tarball / 公共 registry                        | 本地包与公共 0.1.0 均通过空缓存、中文/空格空目录、禁用安装脚本、五工具发现/调用及两次重启；状态保留       |
| 原版 CWA v4.0.8                                    | 独立临时书库，原创 EPUB 入库和 PDF→EPUB 转换均达到 ready，实际下载文件通过格式与身份验证                  |
| MCPHub 1.1.3                                       | 隔离实例从 tarball 启动 stdio 子进程，五工具发现/调用通过；两个下游客户端先后连接，共享进程与状态持续可用 |
| Claude Code 2.1.300                                | 隔离配置中的实际 npx tarball 健康检查为 Connected；未调用模型                                             |
| 运行时依赖                                         | npm audit --omit=dev：0 漏洞；许可证已收集，shrinkwrap 纳入包内                                           |

原有测试覆盖四个 MCP 协议版本、真实 stdio 子进程、上传后强杀与恢复、响应丢失后的核对、防重复提交、丢失输入恢复、人工匹配、EPUB CRC/OPF、票据与身份变化、书源会话/RPC/额度/验证码及书号兼容，以及自动域名维护、提前刷新、缓存/内置回退、旧会话复用、并发与取消隔离、公网 CDN 重定向和 DNS 私网阻断；这些用例在当前 65 项本地测试中也全部通过。

原版 CWA 镜像 digest 为 `sha256:5e00373854247750cc3e4479b492ae09293ff5e06ed10177f226634d97888679`。本次 EPUB→EPUB 下载为 6844 字节，PDF→EPUB 为 44722 字节；SHA256、镜像与输出记录在该 CI 的 `original-cwa` artifact 中。测试未修改 CWA 源码或自动调整其设置。

公共包 `cwa-book-mcp@0.1.0` 为 110,867 字节，SHA-256 为 `b82b54c0291d1bc3dc65942c2e7618a9f5ea6763fed13d41815196cfda53bda6`；registry integrity 与实际下载均匹配已验收安装包。默认渠道 latest 指向 0.1.0，安装示例与后续发布工作流统一使用 latest。Windows x64、Node 25.5.0 的公共空缓存安装耗时约 89 秒；五工具、两次重启和持久状态通过，私有报告位于忽略目录中的 `public-npm-next/verification.json`。

## 目标 CWA 本地验证

2026-10-15，Windows x64、Node 25.5.0，从本地 0.1.0 安装包连接用户授权的 CWA v4.0.8。账号仅在测试进程环境中使用，仓库不保存凭据、真实地址或书籍。

| 项目                         | 结果                                                               |
| ---------------------------- | ------------------------------------------------------------------ |
| 网页登录、OPDS、stdio 五工具 | 通过；中文优先搜索、分页和未配置书源提示符合契约                   |
| 已有 EPUB 获取与签名下载     | ready；ID/UUID、格式、6,039,148 字节及 SHA-256 一致                |
| 恢复与文件接口               | 重启后任务及幂等记录保留；错误票据 403、Range 416；验证文件已清理  |
| 原创 EPUB 真实上传           | 修复后约 23 秒进入 ready；实际回读 1,833 字节，长度与 SHA-256 一致 |
| 上传恢复与幂等               | 重启后 ready、验证摘要与原任务保留；暂存清理通过，无重复提交       |
| 用户手动上传的 EPUB          | 目标记录实际回读 971,841 字节，通过 ZIP/CRC 与 OPF 校验            |

CWA 的入库目录不可写曾导致 HTTP 200 和 location 同时伴随排队失败，容器日志明确报告 `Ingest directory not writable: /cwa-book-ingest`。用户在 NAS 端修复后，本机从当前安装包复验 stdio → upload_book → 签名 PUT → CWA 入库 → ready → 签名 GET，并验证进程重启、幂等与暂存清理，全部通过。旧的未确认提交先经完整书库查询确认无匹配，没有重传。新生成的唯一原创验收记录和私有任务状态予以保留，没有自动删书或修改部署设置。

[v4.0.8 上传源码](https://github.com/crocodilestick/Calibre-Web-Automated/blob/3ce7bf80c8c66485eec7218c6b6c3fef8e6e6ba5/cps/editbooks.py) 与原错误位置一致。部署排查应验证实际应用身份的写入、重命名与目录权限；[NETWORK_SHARE_MODE 的上游说明](https://github.com/crocodilestick/Calibre-Web-Automated#-deploying-on-network-shares-nfssmb-) 同时涉及 chown、SQLite WAL 和监视方式，不能代替目录写权限。本次不推测 NAS 端采用的具体修复方式。

上传反馈回归覆盖 HTTP 200 排队错误的保留，以及报错后实际已入库的安全核对。提交后读取同一会话的错误提示，保存脱敏 submission_error；核对与 retry 仍不重新提交。私有验收结果位于本机忽略目录中的 `live-cwa-post-repair-upload-results.json` 和 `live-cwa-revalidation/read-results.json`，不随仓库或安装包分发。

## 真实书源下载验证

2026-10-15，复用本项目的 Source 和 Files 模块，真实账号认证、会话复用、中文搜索、书籍详情核对及 EPUB 流式下载通过。随后在未设置入口和下载 origin 允许列表的自动模式下复测：三个无凭据探测中选出可用入口，复用原有会话，从认证响应给出的独立 CDN 下载 174,397 字节 EPUB，通过 ZIP/CRC、OPF 校验并记录 SHA-256。本机 TUN 的 Fake-IP DNS 模式也实际通过。

修复目标 CWA 后，使用当前安装包另行完成真实 stdio MCP 获取闭环：search_books 在 CWA 无对应中文候选时自动搜索书源，acquire_book 下载 104,229 字节 EPUB 并提交目标 CWA，随后达到 ready；get_download_url 从 CWA 回读 104,530 字节，长度、SHA-256 和身份验证一致。进程重启后任务与幂等记录保留；同一书源候选使用新的请求键再次获取时直接复用已入库记录，没有重新下载或提交，暂存文件清理通过。此流程未配置固定入口或 CDN 允许列表，复用了已有书源会话。

私有 `download-results.json`、`automatic-domain-results.json` 和 `live-source-cwa-results.json` 分别记录独立下载与完整获取验收，不随仓库分发。CWA 处理后的文件可以与源文件具有不同长度和摘要，下载完整性以实际 CWA 文件验证为准。可空、多值书号问题已有回归；入口故障切换、缓存/维护源失效和密码/额度/验证码边界由模拟测试验证，未把所有公开入口都描述为已实测登录。

## 用户 MCPHub 现场验证

2026-10-16，通过用户已接入的 MCPHub 调用 0.1.0 的五个工具。CWA 搜索、已有书获取、get_task 和下载票据生成通过；书源获取约 11 秒达到 ready，源文件 353,490 字节，CWA 回读 348,728 字节，通过格式与摘要验证。同 request_id 返回原任务，没有重复获取或提交。

upload_book 的准备与刷新票据通过；该任务未接收文件，验收后已取消。返回的文件入口为远端进程的 127.0.0.1 动态端口，从测试 Agent 执行真实 GET/PUT 均为 ECONNREFUSED。因此本次未验证 Hub 的文件下载与本地附件上传闭环；不能将五工具可调用等同于文件链路可达。未修改生产配置，已入库记录保留。私有报告为 `.work/hub-20261016/results.json`。

## OPDS 直连验证与上游边界

2026-10-16，使用同一目标 CWA v4.0.8 的既有 HTTPS 入口，完成十项 OPDS/网页登录探测；未上传书籍或修改配置。

| 验证                      | 实际结果                                                                                                            |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| 匿名 OPDS 搜索、下载      | 均为 401，带 Basic challenge                                                                                        |
| Basic 认证搜索、下载      | 均为 200；从 feed 获取同源 acquisition URL；实际 EPUB 348,728 字节，通过 ZIP/CRC/OPF，SHA-256 与 Hub ready 验证一致 |
| 上游 Range                | 206，返回前 1,024 字节；这是 CWA 的能力，本程序签名网关仍返回 416                                                   |
| 网页书籍页与下载          | 匿名书籍页 302 到登录页；网页登录后书籍页与下载均为 200；下载长度、摘要与 OPDS 一致                                 |
| 仅用网页 Cookie 下载 OPDS | 401；仅网页 Cookie 不足以认证 OPDS                                                                                  |
| 不存在的书籍              | 404                                                                                                                 |

feed 的 length 为 348,685，实际 Content-Length 和文件均为 348,728；已记录差异，不将其当作传输损坏。当前实现的完整性校验使用下载响应和实际文件，不依赖 feed length。私有探测结果为 `.work/opds-research/direct-probe.json`。

[CWA feed 模板](https://github.com/crocodilestick/Calibre-Web-Automated/blob/3ce7bf80c8c66485eec7218c6b6c3fef8e6e6ba5/cps/templates/feed.xml#L76) 的长度取自库内记录，[下载实现](https://github.com/crocodilestick/Calibre-Web-Automated/blob/3ce7bf80c8c66485eec7218c6b6c3fef8e6e6ba5/cps/helper.py#L1219) 可在导出时嵌入元数据；本次未更改设置以追查差异的具体成因。上游 [#1340](https://github.com/crocodilestick/Calibre-Web-Automated/issues/1340) 报告 v4.0.6 导出摘要未记录，[#1146](https://github.com/crocodilestick/Calibre-Web-Automated/issues/1146) 报告目录更新时间不更新；两项仍开放，但所审计 v4.0.8 源码已有导出后摘要记录逻辑，不能把旧版报告直接视为目标实例故障。

结论：CWA 现有入口足以提供直连下载。0.2.0 的 MCP 默认交付已实现并在目标实例验证；阅读器界面、手机浏览器交互和其他远端 Agent 的认证保存能力仍需各自验收。

## 资源观测

同一 CI 在 Node 24.21.0 下完成 1 GiB 文件上传、模拟 CWA 回读/验证及 HTTP 下载，并执行 50 个顺序任务、保留 1000 条缓存记录。

| Linux 架构 | 大文件流程耗时 | RSS 峰值 / 结束   | 事件循环 p99 / 最大 |
| ---------- | -------------- | ----------------- | ------------------- |
| x64        | 38.23 秒       | 202.3 / 186.9 MiB | 21.54 / 389.55 ms   |
| arm64      | 32.46 秒       | 226.0 / 199.3 MiB | 21.58 / 402.13 ms   |

原始报告为该 CI 的 `resources-ubuntu-24.04` 和 `resources-ubuntu-24.04-arm` artifacts，保留 14 天。这是指定工作负载的观测，不是固定 RSS 门槛，也不等于长期稳定性或真实网络吞吐保证。

## 可重复验证

- `npm run check`：配置、SQLite 所有权/事务/密钥、中文优先分页、CWA CSRF、源站登录/会话/额度/验证码、流式校验、签名票据、幂等、响应丢失、重启和协议。
- `npm run test:package`：实际 tarball，空目录与空缓存，关闭安装脚本，验证包内容、五工具调用、默认 OPDS/显式网关的实际下载及重启状态。在对应发布提交上设置 PUBLISHED_PACKAGE_SPEC 为其 name@version，可验证公共 registry 的包完整性和同一套启动流程。
- `npm run test:clients`：指定 MCPHUB_ENTRY 与/或 CLAUDE_CLI_PATH，全部配置与账号是临时测试内容；不修改已有客户端配置。
- `npm run test:integration`：仅在 CWA_INTEGRATION=1 时向指定的隔离原版 CWA 上传原创测试文件；验证 EPUB 入库与 PDF→EPUB 转换。
- `node --expose-gc scripts/resources.mjs`：默认 256 MiB，RESOURCE_FILE_BYTES 可设为 1073741824；报告写入忽略目录 .work/resources-current.json。

CI 定义位于 [.github/workflows/ci.yml](../.github/workflows/ci.yml)：12 个平台任务、4 个 musl 任务、1 个原版 CWA 任务、1 个客户端任务、2 个资源任务，共 20 项。测试使用独立临时目录；容器仅用于原版 CWA 和 musl 验收。

## 尚需外部验收

后续自动发布仍需在 npm 配置本仓库的 trusted publisher，并建立 GitHub npm environment；本次首发为维护者交互发布，不宣称已生成 OIDC/provenance 证明。

MCPHub 0.1.0 的工具通路已现场验证；用户实例升级 0.2.0、签名文件入口可达性、反代上传限制及手机/网页实际操作仍待验收。当前构建的默认 OPDS 返回与真实下载已通过，其他客户端的自动认证/保存不由这一结果推定。
