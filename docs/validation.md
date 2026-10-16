# 当前验证结果

当前代码版本 0.3.0。只记录实际验证及边界；npm 发布结果以 [registry](https://www.npmjs.com/package/cwa-book-mcp) 与 [v0.3.0 Release](https://github.com/mmm1h/cwa-book-mcp/releases/tag/v0.3.0) 为准。用户 MCPHub 不会随仓库或 npm 发布自动升级。

## 本地与真实书源

2026-10-16，Windows x64、Node 25.5.0：

| 项目                         | 实际结果                                                                                              |
| ---------------------------- | ----------------------------------------------------------------------------------------------------- |
| 类型、lint、格式、行为/stdio | 80 个测试通过；新增原文件交付、默认自动入库、仅下载后 collect、独立状态、匿名 GET、到期及旧状态升级   |
| 恢复与副作用                 | 覆盖完成落盘后的恢复、取票响应丢失、坏文件、受保护/含会话链接、过期、缓存丢失、重复请求与提交响应丢失 |
| EPUB 真实下载                | 554,075 字节；一个出口完成匿名校验，另一个不同公网出口匿名 GET，文件长度/摘要一致                     |
| PDF 真实下载                 | 1,046,584 字节；相同实验通过基础 PDF 校验和摘要比对                                                   |
| 额度观察                     | EPUB 取票/校验后 1→2，第二出口 GET 后仍为 2；PDF 2→3，第二出口 GET 后仍为 3                           |
| 现有网络配置                 | 实验使用独立的本机临时代理进程；全局配置未变，没有修改 NAS、CWA、MCPHub 或 CF                         |

私有实验记录在忽略目录 .work/direct-link-20261016/results.json，能力链接和实际书籍不随仓库/安装包分发。这是两个样本及两个出口的观察，不保证其他 CDN、长时间有效性、所有账号额度行为或所有客户端能力。Agent 实际保存后才能确认达到读取目标，不能从 link_issued 推导。

延迟重用实验约 8 分钟后返回 HTTP 410；样本 URL 的绝对 expires 表明有效期约 5 分钟。实现据此读取已知上游到期时间并及时拒绝发链接，不把本地默认 15 分钟复用窗口视作上游保证。文件缓存与链接到期独立，链接失效仍可在缓存期限内补入库。私有记录为 replay-results.json。

## 发布验收

发布前对待发布提交执行：

- npm run check：完整类型/lint/格式与行为/真实 stdio 测试。
- npm run test:package：实际 tarball，中文/空格空目录与空缓存、禁用安装脚本、六工具发现/调用；匿名书源原文件、OPDS 认证下载、显式网关与两次重启，校验状态持久化。
- 完整 [Node acceptance](https://github.com/mmm1h/cwa-book-mcp/actions/workflows/ci.yml)：Node 22.19/24 × Windows/macOS/Linux x64/arm64，Linux musl、原版隔离 CWA、MCPHub/Claude 客户端与流式资源测试。
- npm audit --omit=dev；发布 latest 后设置 PUBLISHED_PACKAGE_SPEC=cwa-book-mcp@0.3.0，再运行实际公共包验收，核对 registry integrity 与待发布安装包完全一致。

0.3.0 的 CI、实际安装包和公共 registry 验收结果在对应 GitHub Release 记录；未完成这些检查前不得把源码构建描述为公开发布。自动 OIDC 发布还需 npm trusted publisher 配置；交互发布使用维护者本人官方浏览器授权及 2FA，不保存长期令牌。

## CWA 与客户端边界

0.3.0 新工具在目标实例完成真实 stdio → 书源下载校验 → Agent 匿名保存/验证 291,152 字节 EPUB。对应 CWA 上传收到处理失败反馈，核对窗口后仍未确认入库，任务保留 needs_review，没有重传。随后独立原创诊断请求收到正常上传确认，但这不能代替该书源任务的入库验证。现场错误日志仍输出容器，网页不可读；不将本次现场入库描述为通过。私有记录为 .work/live-direct-20261016/results.json。

既有目标 CWA v4.0.8 已验证网页登录、OPDS、原创 EPUB 入库、PDF→EPUB 转换、身份/文件校验与重启恢复。曾出现的 /cwa-book-ingest 不可写已由用户在 NAS 端修复，修复后真实上传闭环通过。此历史基线不替代新版本的测试。

目标 CWA 的匿名 OPDS 返回 401，Basic 为 200；网页 Cookie 不足以认证 OPDS。feed length 可与实际导出长度不同，完整性以实际响应和文件为准。CWA 导出可能改变元数据、长度/摘要，因此原文件和 CWA 结果分别验证。

MCPHub 既有实例工具通路曾通过，但其 127.0.0.1 动态文件端口无法从远端 Agent 访问。书源直链方案因此使用现有出站网络；默认入库也走现有 CWA 地址。新版本不会自动修改生产部署、添加反代路径或开放端口。MCPHub/客户端的二进制 resource 保存没有完整验收，不作为本版本交付路径。

## 可重复验证

原版 CWA 测试：CWA_INTEGRATION=1 时，npm run test:integration 向指定的隔离原版实例上传原创文件，验证 EPUB 入库及 PDF→EPUB 转换，不修改 CWA 源码。

客户端测试：设置 MCPHUB_ENTRY 和/或 CLAUDE_CLI_PATH 后执行 npm run test:clients，使用临时配置验证实际 tarball stdio 接入及 Hub 共享进程，不改已有配置。

资源测试：node --expose-gc scripts/resources.mjs；RESOURCE_FILE_BYTES 可设为 1073741824。观测 RSS、事件循环与磁盘，不设固定 RSS 选栈门槛。CI 资源与原版 CWA 的原始输出见对应运行 artifacts。

真实书源和目标 CWA 验收使用私有凭据与独立状态；不得把模拟通过当作现场已升级或所有远端客户端都能保存文件。
