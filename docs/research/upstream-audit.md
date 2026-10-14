# 上游复用与证据

核验日期：2026-10-14。本文记录设计依据，不代表实际 NAS、账号或完整服务已验证。

## 复用清单

| 项目 | 复用方式 | 不承担的职责 |
| --- | --- | --- |
| [heartleo/zlib](https://github.com/heartleo/zlib) | 导入 Go 核心库；必要时最小有界请求补丁 | 持久任务、CWA 接入、MCP |
| [官方 Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk) | 协议、工具 schema、HTTP transport | 本项目持久业务任务 |
| [bbolt](https://github.com/etcd-io/bbolt) | 嵌入式任务 KV | 书籍正文和权威书库 |
| [CWA](https://github.com/crocodilestick/Calibre-Web-Automated) | 原样部署，通过 HTTP 使用 | 本项目代码中的内置转换模块 |
| [ni-c/calibreweb-mcp](https://github.com/ni-c/calibreweb-mcp) | 参考 OPDS 读取与结果整理 | 上传和完整任务链路 |
| [acato/calibre-web-mcp](https://github.com/acato/calibre-web-mcp) | 参考会话/OPDS 设计 | 不另部署其 MCP，不使用 Python 运行时 |
| [Calibre 上传插件](https://github.com/joao-a-cardoso/calibre-send-to-calibre-web) | 参考登录、CSRF、multipart 流程 | 不作为常驻依赖，不整段复制 GPL 后端 |

首版没有现成项目可直接覆盖完整需求。上游项目数量不等于必须部署的服务数量。

## 可复核基准

- heartleo/zlib：调研参考提交 [6a299fd](https://github.com/heartleo/zlib/tree/6a299fdc59927f7e84f94390c4e54bfaf4d06cee)。[当前模块声明](https://github.com/heartleo/zlib/blob/main/go.mod)最低 Go 1.25；后续开发需要再次固定并核验实际版本。
- Go MCP SDK：调研时发布版本 v1.8.0；实现时核对最低 Go 与协议兼容后固定。
- bbolt：调研时发布版本 v1.5.0；实现时验证目标架构、文件锁和资源表现。
- CWA：本轮源码核验基准 [4fc2c44](https://github.com/crocodilestick/Calibre-Web-Automated/tree/4fc2c4459965efb1b8ff43ea7849c2f47a755d9c)。
- Calibre 导入算法：函数级实验参考 [e4fd9c9](https://github.com/kovidgoyal/calibre/tree/e4fd9c99eff24890973935db33b7c700522bab5f)。这不是目标 CWA 内置 Calibre 版本的声明。

## CWA 上传与完成判定

[editbooks.py](https://github.com/crocodilestick/Calibre-Web-Automated/blob/4fc2c4459965efb1b8ff43ea7849c2f47a755d9c/cps/editbooks.py) 中的 POST /upload 依赖网页登录和上传权限；新书使用 btn-upload。文件以临时路径写完后交给 ingest。

Upload 提示不代表转换与入库已完成；[TaskUpload](https://github.com/crocodilestick/Calibre-Web-Automated/blob/4fc2c4459965efb1b8ff43ea7849c2f47a755d9c/cps/tasks/upload.py) 不能用作完整业务任务状态。

[ingest_processor.py](https://github.com/crocodilestick/Calibre-Web-Automated/blob/4fc2c4459965efb1b8ff43ea7849c2f47a755d9c/scripts/ingest_processor.py) 调用 Calibre 完成转换/入库，但网页上传响应没有把可靠的最终 book_id 返回给外部调用方。

[feed.xml](https://github.com/crocodilestick/Calibre-Web-Automated/blob/4fc2c4459965efb1b8ff43ea7849c2f47a755d9c/cps/templates/feed.xml) 输出书籍标识、基本元数据、标签、格式与下载链接，可用于结果核对。不能仅凭一个标题命中判断本次上传归属。

## 重复处理的已知边界

Calibre 的 [导入逻辑](https://github.com/kovidgoyal/calibre/blob/e4fd9c99eff24890973935db33b7c700522bab5f/src/calibre/db/cli/cmd_add.py) 与 [匹配逻辑](https://github.com/kovidgoyal/calibre/blob/e4fd9c99eff24890973935db33b7c700522bab5f/src/calibre/db/utils.py) 表明，new_record 并不意味着所有格式都强制建新记录；不同格式仍可能加入匹配记录。

旧研究曾考虑改写版本标题、注入任务标签。**该方案已被最新范围撤回，不能作为实现要求。** 本项目接受 CWA 现有重复处理，并在结果存在歧义时返回 needs_review，不承担译本隔离。

普通新书上传与 btn-upload-format 不是同一处理路径。后者针对指定 book_id 添加格式，可能替换同格式，首版不自动使用。

## 已做过的实验与未做的验证

以下是建仓前调研记录，不是本仓库已有自动化测试：

| 证据 | 证明范围 | 不证明什么 |
| --- | --- | --- |
| 现成上传客户端的本地 HTTP 模拟 | 登录、CSRF、会话恢复可复用；HTTP 200 会误报 | 真实 CWA 转换成功 |
| Calibre 原函数配替代数据库的受控实验 | new_record 跨格式合并等行为 | 目标 NAS 内置版本已复现 |
| 未修改的 CWA OPDS 模板配合成记录渲染 | 标签与格式能输出到 feed | 标签经历真实上传/转换后一定保留 |
| 源码阅读 | API 路径、参数与边界 | 账号、网络、权限及资源达标 |

没有执行真实 Z-Library 下载、目标 NAS 上传、完整 MCPHub 集成，也没有本服务的内存基准。

后续需要把当前范围对应的实验实现为仓库内可重复测试；不要复制旧的译本测试作为新功能。

## 许可证与代码使用

本仓库自有内容使用 MIT。heartleo/zlib、Go SDK 和 bbolt 的实际依赖许可证需在固定版本后核对并保留要求的声明。

CWA 通过 HTTP 使用，不把其 GPL 实现代码搬进本仓库。上传插件只作协议流程参考。若确实需要复制第三方代码，先核对该文件的许可证并记录来源，不按仓库名称猜测。

## 其他官方依据

- [MCPHub 配置](https://docs.mcphub.app/configuration/mcp-settings)：Streamable HTTP、上游鉴权与会话配置。
- [Go GC 指南](https://go.dev/doc/gc-guide)：GOMEMLIMIT 的软限制语义。
- [bbolt README](https://github.com/etcd-io/bbolt)：单写事务、文件锁、mmap 等限制。
- [Cloudflare 请求大小限制](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/4xx-client-error/error-413/)：不同传输段需要分别核对。
