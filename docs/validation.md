# 当前实现验证

以下仅为 Node.js/TypeScript 实现的证据，更新时间 2026-10-15。提交 [88e4dd6](https://github.com/mmm1h/cwa-book-mcp/commit/88e4dd61753905458d747dfed5bf2cebf577b197) 的 [Node acceptance](https://github.com/mmm1h/cwa-book-mcp/actions/runs/38685526062) 全部 20 项通过。之后的上传反馈、书源兼容和自动域名维护已在本地通过 47 个测试及空缓存 tarball 验证，尚未重新运行完整平台 CI。npm 公开发布按用户要求暂缓。

| 项目                                               | 实际结果                                                                                                  |
| -------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| Node 22.19.0 / 24，Windows/macOS/Linux × x64/arm64 | 12 项 CI 均通过类型、lint、格式、26 个行为测试及实际 tarball 安装                                         |
| Linux musl，Node 22 / 24 × x64/arm64               | 4 项 Alpine CI 均通过相同检查及安装；禁用安装脚本                                                         |
| npm tarball                                        | 空缓存、中文/空格空目录、禁用安装脚本；npx 冷启动、五工具发现/调用与两次缓存重启通过                      |
| 原版 CWA v4.0.8                                    | 独立临时书库，原创 EPUB 入库和 PDF→EPUB 转换均达到 ready，实际下载文件通过格式与身份验证                  |
| MCPHub 1.1.3                                       | 隔离实例从 tarball 启动 stdio 子进程，五工具发现/调用通过；两个下游客户端先后连接，共享进程与状态持续可用 |
| Claude Code 2.1.300                                | 隔离配置中的实际 npx tarball 健康检查为 Connected；未调用模型                                             |
| 运行时依赖                                         | npm audit --omit=dev：0 漏洞；许可证已收集，shrinkwrap 纳入包内                                           |

当前 47 个行为测试覆盖：四个 MCP 协议版本、真实 stdio 子进程、上传后强杀与恢复、响应丢失后的核对、防重复提交、丢失输入恢复、人工匹配、EPUB CRC/OPF、票据与身份变化、书源会话/RPC/额度/验证码及书号兼容，以及自动域名维护、提前刷新、缓存/内置回退、旧会话复用、并发与取消隔离、公网 CDN 重定向和 DNS 私网阻断。

原版 CWA 镜像 digest 为 `sha256:5e00373854247750cc3e4479b492ae09293ff5e06ed10177f226634d97888679`。本次 EPUB→EPUB 下载为 6841 字节，PDF→EPUB 为 43723 字节；SHA256、镜像与输出记录在该 CI 的 `original-cwa` artifact 中。测试未修改 CWA 源码或自动调整其设置。

## 目标 CWA 本地验证

2026-10-15，Windows x64、Node 25.5.0，从本地 0.1.0 安装包连接用户授权的 CWA v4.0.8。账号仅在测试进程环境中使用，仓库不保存凭据、真实地址或书籍。

| 项目                         | 结果                                                              |
| ---------------------------- | ----------------------------------------------------------------- |
| 网页登录、OPDS、stdio 五工具 | 通过；中文优先搜索、分页和未配置书源提示符合契约                  |
| 已有 EPUB 获取与签名下载     | ready；ID/UUID、格式、6,039,148 字节及 SHA-256 一致               |
| 恢复与文件接口               | 重启后任务及幂等记录保留；错误票据 403、Range 416；验证文件已清理 |
| 原创 EPUB 真实上传           | 未通过入库验收；提交后约 7 分钟进入 needs_review                  |

CWA 的上传响应为 HTTP 200 和 location，但同一会话同时提示排队失败。用户随后将已校验的真实 EPUB 手动上传到网页，同样复现。用户提供的容器日志明确记录 chown 的 Operation not permitted，以及 `Failed to queue upload for ingest: Ingest directory not writable: /cwa-book-ingest`；已定位到入库目录不可写，具体挂载/UID/GID/ACL 由 NAS 端检查修复。应用日志为 stdout，网页日志接口不可读取。

[v4.0.8 上传源码](https://github.com/crocodilestick/Calibre-Web-Automated/blob/3ce7bf80c8c66485eec7218c6b6c3fef8e6e6ba5/cps/editbooks.py) 与此错误位置一致。部署排查应验证实际应用身份的写入、重命名与目录权限；[NETWORK_SHARE_MODE 的上游说明](https://github.com/crocodilestick/Calibre-Web-Automated#-deploying-on-network-shares-nfssmb-) 同时涉及 chown、SQLite WAL 和监视方式，不能代替目录写权限。本机未修改目标部署设置，修复后的真实入库与下载仍待验收。

已修复上传反馈遗漏：提交后读取同一会话的错误提示，保存脱敏 submission_error；核对与 retry 仍不重新提交。新增测试覆盖 HTTP 200 排队错误的保留，以及报错后实际已入库的安全核对。实际目标的未确认任务和原创测试文件保留在本机忽略目录或临时状态目录，未自动重传、删除书库记录或调整 CWA 配置。

## 真实书源下载验证

2026-10-15，复用本项目的 Source 和 Files 模块，真实账号认证、会话复用、中文搜索、书籍详情核对及 EPUB 流式下载通过。随后在未设置入口和下载 origin 允许列表的自动模式下复测：三个无凭据探测中选出可用入口，复用原有会话，从认证响应给出的独立 CDN 下载 174,397 字节 EPUB，通过 ZIP/CRC、OPF 校验并记录 SHA-256。本机 TUN 的 Fake-IP DNS 模式也实际通过。

本机忽略目录中的 `download-results.json` 和 `automatic-domain-results.json` 分别记录两次验证；自动模式没有固定入口或额外 CDN 配置。文件交给用户手动上传，没有创建 CWA 任务或提交入库；本项不替代完整 MCP 书源到 CWA 的验收。实测发现的可空、多值书号问题已有回归；入口故障切换、缓存/维护源失效和密码/额度/验证码边界由模拟测试验证，未把所有公开入口都描述为已实测登录。

## 资源观测

同一 CI 在 Node 24.21.0 下完成 1 GiB 文件上传、模拟 CWA 回读/验证及 HTTP 下载，并执行 50 个顺序任务、保留 1000 条缓存记录。

| Linux 架构 | 大文件流程耗时 | RSS 峰值 / 结束   | 事件循环 p99 / 最大 |
| ---------- | -------------- | ----------------- | ------------------- |
| x64        | 39.75 秒       | 207.1 / 189.9 MiB | 21.45 / 337.64 ms   |
| arm64      | 32.21 秒       | 203.2 / 183.7 MiB | 21.54 / 362.55 ms   |

原始报告为该 CI 的 `resources-ubuntu-24.04` 和 `resources-ubuntu-24.04-arm` artifacts，保留 14 天。这是指定工作负载的观测，不是固定 RSS 门槛，也不等于长期稳定性或真实网络吞吐保证。

## 可重复验证

- `npm run check`：配置、SQLite 所有权/事务/密钥、中文优先分页、CWA CSRF、源站登录/会话/额度/验证码、流式校验、签名票据、幂等、响应丢失、重启和协议。
- `npm run test:package`：实际 tarball，空目录与空缓存，关闭安装脚本，验证包内容及 npx stdio 生命周期。设置 PUBLISHED_PACKAGE_SPEC 为当前 name@version 可验证公共 registry 的包完整性和同一套启动流程。
- `npm run test:clients`：指定 MCPHUB_ENTRY 与/或 CLAUDE_CLI_PATH，全部配置与账号是临时测试内容；不修改已有客户端配置。
- `npm run test:integration`：仅在 CWA_INTEGRATION=1 时向指定的隔离原版 CWA 上传原创测试文件；验证 EPUB 入库与 PDF→EPUB 转换。
- `node --expose-gc scripts/resources.mjs`：默认 256 MiB，RESOURCE_FILE_BYTES 可设为 1073741824；报告写入忽略目录 .work/resources-current.json。

CI 定义位于 [.github/workflows/ci.yml](../.github/workflows/ci.yml)：12 个平台任务、4 个 musl 任务、1 个原版 CWA 任务、1 个客户端任务、2 个资源任务，共 20 项。测试使用独立临时目录；容器仅用于原版 CWA 和 musl 验收。

## 尚需外部验收

公共 npm 首次发布与公共 registry 的完整安装；当前 npm whoami 返回 ENEEDAUTH。产物与发布工作流已就绪，维护者登录后可先发布 next，随后配置 OIDC。

目标 CWA 仍需在 NAS 端修复入库目录权限并完成入库验收；MCPHub 网络、反代上传限制、远端 Agent 的 PUT/GET 能力，以及手机/网页到 Hub 的实际访问仍需验证。本机读取通过和隔离客户端兼容性不替代这些部署验收。
