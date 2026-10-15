# 当前实现验证

以下仅为 Node.js/TypeScript 实现的证据，更新时间 2026-10-15。npm 尚未发布，生产 NAS/CWA/MCPHub 未修改。

| 项目                                | 实际结果                                                                                                                                               |
| ----------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Windows x64 / Node 25.5.0           | 类型、lint、格式和行为回归实施中；首批 21 个测试通过，补充了未知提交重试回归                                                                           |
| Windows x64 / Node 22.19.0、24.22.0 | 21 个回归全部通过，包含真实 stdio 子进程和 SIGKILL 恢复                                                                                                |
| npm tarball                         | 空缓存、中文/空格空目录、禁用安装脚本；npx 冷启动和两次缓存重启、五工具发现/调用通过                                                                   |
| MCPHub 1.1.3                        | 隔离实例从 tarball 启动 stdio 子进程，五工具发现/调用通过；两个下游客户端先后连接，复用同一状态目录成功                                                |
| Claude Code 2.1.300                 | 隔离 CLAUDE_CONFIG_DIR，实际 npx tarball 配置健康检查为 Connected                                                                                      |
| 运行时依赖                          | npm audit --omit=dev：0 漏洞；许可证已收集，shrinkwrap 纳入包内                                                                                        |
| Windows 256 MiB 文件负载            | 完整上传、CWA 回读/验证和 HTTP 下载通过；50 个顺序任务、1000 条缓存；RSS 峰值约 158 MiB，结束约 102 MiB，事件循环 p99 33.29 ms。仅观测，不作为固定门槛 |
| 跨平台与原版 CWA                    | 新 acceptance 工作流待运行，不能沿用旧 Go 结果                                                                                                         |

## 可重复验证

- `npm run check`：配置、SQLite 所有权/事务/密钥、中文优先分页、CWA CSRF、源站登录/会话/额度/验证码、流式校验、签名票据、幂等、响应丢失、重启和协议。
- `npm run test:package`：实际 tarball，空目录与空缓存，关闭安装脚本，验证包内容及 npx stdio 生命周期。
- `npm run test:clients`：指定 MCPHUB_ENTRY 与/或 CLAUDE_CLI_PATH，全部配置与账号是临时测试内容；不修改已有客户端配置。
- `npm run test:integration`：仅在 CWA_INTEGRATION=1 时向指定的隔离原版 CWA 上传原创测试文件；验证 EPUB 入库与 PDF→EPUB 转换。
- `node --expose-gc scripts/resources.mjs`：默认 256 MiB，RESOURCE_FILE_BYTES 可设为 1073741824；报告写入忽略目录 .work/resources-current.json。

CI 定义位于 [.github/workflows/ci.yml](../.github/workflows/ci.yml)：Node 22/24 × 六个平台，以及 Linux glibc/musl、原版 CWA v4.0.8、MCPHub/CLI 和双架构 1 GiB 资源观测。测试使用独立临时目录和原版容器，不自动配置生产 CWA。

## 尚需外部验收

真实 Z-Library 账号的站点可用性、当前 EAPI/RPC 行为、额度及实际 CDN origin；目前协议和错误分支使用本地模拟验证。需要操作者提供会话或账号，不能把模拟测试称为真实书源通过。

公共 npm 首次发布与公共 registry 的完整安装；当前无 npm 登录。完成产物验收后由维护者认证，随后可配置 OIDC。

目标 NAS/CWA 设置、MCPHub 网络、反代上传限制、Agent 的 PUT/GET 能力，以及手机/网页到 Hub 的实际访问；隔离客户端兼容性不替代这些部署验收。
