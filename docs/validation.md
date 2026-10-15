# 当前实现验证

以下仅为 Node.js/TypeScript 实现的证据，更新时间 2026-10-15。提交 [88e4dd6](https://github.com/mmm1h/cwa-book-mcp/commit/88e4dd61753905458d747dfed5bf2cebf577b197) 的 [Node acceptance](https://github.com/mmm1h/cwa-book-mcp/actions/runs/38685526062) 全部 20 项通过。之后的上传反馈与书源兼容修复已在本地通过 30 个测试及空缓存 tarball 验证，尚未重新运行完整平台 CI。npm 公开发布按用户要求暂缓。

| 项目                                               | 实际结果                                                                                                  |
| -------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| Node 22.19.0 / 24，Windows/macOS/Linux × x64/arm64 | 12 项 CI 均通过类型、lint、格式、26 个行为测试及实际 tarball 安装                                         |
| Linux musl，Node 22 / 24 × x64/arm64               | 4 项 Alpine CI 均通过相同检查及安装；禁用安装脚本                                                         |
| npm tarball                                        | 空缓存、中文/空格空目录、禁用安装脚本；npx 冷启动、五工具发现/调用与两次缓存重启通过                      |
| 原版 CWA v4.0.8                                    | 独立临时书库，原创 EPUB 入库和 PDF→EPUB 转换均达到 ready，实际下载文件通过格式与身份验证                  |
| MCPHub 1.1.3                                       | 隔离实例从 tarball 启动 stdio 子进程，五工具发现/调用通过；两个下游客户端先后连接，共享进程与状态持续可用 |
| Claude Code 2.1.300                                | 隔离配置中的实际 npx tarball 健康检查为 Connected；未调用模型                                             |
| 运行时依赖                                         | npm audit --omit=dev：0 漏洞；许可证已收集，shrinkwrap 纳入包内                                           |

当前 30 个行为测试覆盖：四个 MCP 协议版本、真实 stdio 子进程、上传后强杀与恢复、响应丢失后的核对、重复请求及同书不同请求防重投、丢失输入恢复、人工匹配 CWA 记录、EPUB CRC/OPF 校验、票据与身份变化、跨源凭据隔离、书源会话/RPC/额度/验证码、可空与多值书号，以及并发登录只认证一次。

原版 CWA 镜像 digest 为 `sha256:5e00373854247750cc3e4479b492ae09293ff5e06ed10177f226634d97888679`。本次 EPUB→EPUB 下载为 6841 字节，PDF→EPUB 为 43723 字节；SHA256、镜像与输出记录在该 CI 的 `original-cwa` artifact 中。测试未修改 CWA 源码或自动调整其设置。

## 目标 CWA 本地验证

2026-10-15，Windows x64、Node 25.5.0，从本地 0.1.0 安装包连接用户授权的 CWA v4.0.8。账号仅在测试进程环境中使用，仓库不保存凭据、真实地址或书籍。

| 项目                         | 结果                                                              |
| ---------------------------- | ----------------------------------------------------------------- |
| 网页登录、OPDS、stdio 五工具 | 通过；中文优先搜索、分页和未配置书源提示符合契约                  |
| 已有 EPUB 获取与签名下载     | ready；ID/UUID、格式、6,039,148 字节及 SHA-256 一致               |
| 恢复与文件接口               | 重启后任务及幂等记录保留；错误票据 403、Range 416；验证文件已清理 |
| 原创 EPUB 真实上传           | 未通过入库验收；提交后约 7 分钟进入 needs_review                  |

CWA 的上传响应为 HTTP 200 和 location，但同一会话同时提示上传任务排队失败。只读检查确认允许 EPUB、目标格式为 EPUB、监控服务报告运行；这些信息仍不足以定位排队异常。尚需对应 CWA 服务日志，不能把排队失败推断为已经入库或确定未接收。

上游排查已核对 [v4.0.8 上传源码](https://github.com/crocodilestick/Calibre-Web-Automated/blob/3ce7bf80c8c66485eec7218c6b6c3fef8e6e6ba5/cps/editbooks.py)：账号无上传权限会返回 403，本次提示来自通过权限检查后的保存或排队异常。同类记录包括入库目录权限 [#603](https://github.com/crocodilestick/Calibre-Web-Automated/issues/603)、未挂载入库目录 [#995](https://github.com/crocodilestick/Calibre-Web-Automated/issues/995#issuecomment-3832106829)，以及文件名过长的未合并修复 [#1511](https://github.com/crocodilestick/Calibre-Web-Automated/pull/1511)。MCP 上传使用短文件名 `book.epub`；用户反馈网页上传正常，这些案例均不能直接证明目标实例根因。另有 v4.0.8 [#1569](https://github.com/crocodilestick/Calibre-Web-Automated/issues/1569) 报告入库时数据库锁定，但发生在文件已排队后的阶段，与本次提示不同。

已修复上传反馈遗漏：提交后读取同一会话的错误提示，保存脱敏 submission_error；核对与 retry 仍不重新提交。新增测试覆盖 HTTP 200 排队错误的保留，以及报错后实际已入库的安全核对。实际目标的未确认任务和原创测试文件保留在本机忽略目录或临时状态目录，未自动重传、删除书库记录或调整 CWA 配置。

## 真实书源下载验证

2026-10-15，复用本项目的 Source 和 Files 模块，真实账号认证、会话复用、中文搜索、书籍详情核对及 EPUB 流式下载通过。文件为 174,397 字节，完成 ZIP/CRC、OPF 校验并记录 SHA-256；结果保存在本机忽略目录 `.work/live-zlibrary/download-results.json`。按用户要求，文件交给用户手动上传，没有创建 CWA 任务或提交入库；本项不替代完整 MCP 书源到 CWA 的验收。

实测发现并修复可选书号为 null、多个书号组成长字符串导致整页搜索失败的问题；缺失字段保持未知，多值字段选取一个长度有效的标识。回归测试同时验证搜索和后续下载。实际文件使用书源返回的独立 CDN origin，通过精确允许列表接入；验证码、额度不足和其他部署入口仍需分别验证。

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

目标 CWA 上传排队异常仍需定位并完成入库验收；MCPHub 网络、反代上传限制、远端 Agent 的 PUT/GET 能力，以及手机/网页到 Hub 的实际访问仍需验证。本机读取通过和隔离客户端兼容性不替代这些部署验收。
