# 接口契约

stdio 是唯一 MCP 传输；stdout 只有 JSON-RPC，日志走 stderr。成功同时返回 structuredContent 与等价 JSON 文本；业务错误为 isError:true、error:{code,message,retryable}。时间戳为 Unix 毫秒，签名网关 expires_at 为 ISO8601。

## 六个工具

| 工具             | 输入与行为                                                                                                                                  |
| ---------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| search_books     | query 必填；author 可选；language 默认 zh_preferred，也可 any/语言代码；formats 为 epub/pdf 数组；limit 默认 20、最大 50；cursor 续查       |
| download_book    | start 需 request_id、candidate_id、required_format；save_to_cwa 默认 true。仅选定 Z 原文件，不做格式转换。collect/retry/cancel 仅需 task_id |
| acquire_book     | start 需 request_id、candidate_id、required_format；保持书库获取/转换流程。retry/cancel 用 task_id，resolve 用 task_id 与 CWA candidate_id  |
| upload_book      | prepare 需 request_id、title、filename、size_bytes、required_format，可选 authors/language/sha256；refresh_ticket 用 task_id                |
| get_task         | task_id；返回持久任务、独立下载/入库状态及 resolve 的 choices，不自动返回下载能力链接                                                       |
| get_download_url | task_id，delivery 可选 source/opds/gateway；下载任务默认 source，其余默认 opds                                                              |

request_id 为调用方生成的 1–128 字符幂等键。同 key/同参数返回原任务，不同参数返回 IDEMPOTENCY_CONFLICT。download_book 省略 save_to_cwa 与显式 true 等价。原文件下载会消耗书源额度，使用新的 request_id 表示明确的新请求。

候选不可传任意 URL、路径或伪造上游参数；candidate_id 与 cursor 30 分钟失效。默认中文优先：CWA 中文 → 书源中文 → CWA 其他 → 书源其他，找到一层后只分页该层；language:any 为 CWA → 书源。每次最多读三个上游页，使用 next_cursor 继续。partial/warnings 表示不能断言无结果；语言依元数据，不从书名推断。

## 下载并入库

调用 download_book(start, request_id, candidate_id, required_format) 创建任务。默认需 CWA_WRITE_ENABLED=true；传 save_to_cwa:false 不需写权限，也不执行 CWA 入库查询。required_format 必须等于候选原文件格式。CWA 候选返回 DIRECT_DOWNLOAD_UNAVAILABLE，不自动到书源寻找或下载其他版本。

MCP 认证并核对候选 ID/hash/元数据/格式，从书源取文件链接，匿名 GET 后完成文件校验。校验通过即可获取原文件链接，无需等待 CWA 入库。自动入库复用同一临时文件，不要求 Agent 将下载文件传回 NAS。入库核对可复用已有 CWA 匹配，多个匹配仍需 resolve。

任务通过 get_task 分别报告：

- download.status：pending、available、unavailable、expired 或 cancelled；包含 format、filename、verification、available_until、expires_at 和 cache_until，无文件 URL。
- collection：requested 与 state。requested:false 时 state:not_requested；否则使用下表的 CWA 流程状态。
- state:download_available 表示仅下载任务的原文件已验证；不是 CWA ready，也不是 Agent 已保存。

仅下载任务可在缓存期限内调用 download_book(action:collect,task_id)，自动复用原文件入库；重复 collect 不重复提交。缓存过期/丢失返回 INPUT_MISSING，不自动从 Z 补下载。取消只停止跟踪并禁止继续发链接，已发给客户端的上游能力链接由上游控制，无法撤销。

## 下载结果

download_book 任务默认返回：

```json
{
  "task_id": "<UUID>",
  "delivery": "source",
  "status": "link_issued",
  "url": "https://example.invalid/file?ticket=<FILE_CAPABILITY>",
  "filename": "book.epub",
  "format": "epub",
  "authentication": { "scheme": "none" },
  "expires_at": null,
  "available_until": 1792116900000,
  "verification": { "size_bytes": 12345, "sha256": "<SHA256>", "at": 1792116000000 }
}
```

Agent 使用普通 HTTP 文件工具保存并读取；不要求它重复做身份、格式或摘要校验。verification 是服务端此次观察，不保证之后 GET 字节不变。status:link_issued 只表示已提供链接，不表示已保存。URL 的绝对 Unix expires 参数可识别时，expires_at 返回 Unix 毫秒，否则为 null。available_until 取本地窗口与已知上游到期的较早者，非上游保证；已知到期后拒绝发链接。get_task 的 cache_until 单独控制临时文件的补入库期限，下载链接过期仍可在此期限内 collect。

匿名下载受阻、文件损坏或链接包含已知账号/会话信息时不发链接。get_download_url 重用已验证链接，不再次取票或整文件下载。取票/下载结果不明进入 blocked，不自动申请新票；过期返回 SOURCE_LINK_EXPIRED，新的下载需明确创建新请求。入库失败不影响尚在期限内的已验证原文件链接。

CWA 任务必须 ready 后才能获取 OPDS 或网关链接。OPDS 返回 delivery:opds、url、web_url、authentication:{scheme:basic}、verification；web_url 为人工参考，不代表 Agent 已取得文件。必须由客户端已有的 CWA Basic 认证下载；MCP env 不会自动提供认证。feed acquisition 链接必须同源、无凭据、匹配书籍/格式并保留反代子路径。

显式 delivery:gateway 返回本进程签名 GET 的 url、expires_at、size_bytes、sha256，需要 Agent 可达的文件入口。默认 source 不覆盖 CWA 独有书或转换结果，原文件也可能与 CWA 导出文件具有不同摘要。

## CWA 任务与恢复

| 状态                      | 含义                                                                 |
| ------------------------- | -------------------------------------------------------------------- |
| awaiting_upload           | 等待整文件 PUT，可 refresh_ticket                                    |
| queued/downloading/staged | 核对书库、下载或完整文件待提交                                       |
| submitting                | 已持久化提交意图，重启后先核对                                       |
| reconciling/verifying     | 等待 CWA 或验证实际目标格式文件                                      |
| blocked                   | 认证、权限、输入或通路受阻；检查 error                               |
| needs_review              | 多个匹配或提交后无法确认；选择 choices，或人工处理后 retry           |
| ready                     | 实际核验 CWA ID/UUID、元数据、目标格式、长度/格式并记录 SHA-256/时间 |
| failed/cancelled          | 过期/失败或主动取消，不删除 CWA 记录                                 |

ready 是 verification.at 时点的观察；OPDS/网关发链接仍重新核对 CWA 身份与实际文件。HTTP 200、location、书源下载成功均不能代替 CWA 验证。submission_error 保留脱敏提交反馈，包括 HTTP 200 时的排队失败；错误后仍先核对，不盲目重传。

上传 title/authors/language 应与真实文件元数据对应，不改写书名。resolve 明确选择 CWA 记录，语言及 ID/UUID 仍须匹配。已提交 retry 仅延长核对窗口，不重新下载/提交。同书已有未确认提交返回 RELATED_SUBMISSION_PENDING 与 waiting_for。MCP 调用取消不删除持久任务，宿主终止子进程会停止后台工作。

EPUB 校验 ZIP 路径、重复/加密/符号链接、展开大小、CRC、首条未压缩 mimetype、container 与 OPF/阅读顺序。PDF 校验头与 EOF。这是基础完整性验证，不是文档渲染或安全扫描。

## 可选签名文件接口

upload_book.prepare 返回 task_id、state、upload:{method:PUT,url,headers,expires_at}。整文件 PUT /files/upload/{task_id}?ticket=...，准确 Content-Length，禁止 chunked/压缩，支持可选 SHA-256。重复相同内容成功，不覆盖或重复入库；不同内容冲突。同任务同时上传返回 UPLOAD_IN_PROGRESS。

网关 GET /files/download/{task_id}?ticket=... 只读取 CWA 目标格式，先完整校验再返回 HTTP 200、长度与 SHA-256 ETag，结束清理副本；Range 返回 416。HMAC 票据绑定用途、任务、格式、期限及下载 ID/UUID，错误/过期返回 403。HTTP 并发最多 4，额外请求 503；无任意 URL 代理、服务端路径或目录列表。

常见错误：WRITE_DISABLED、AUTH_REQUIRED、AUTH_EXPIRED、CAPTCHA_REQUIRED、QUOTA_EXCEEDED、SOURCE_UNAVAILABLE、SOURCE_LINK_AUTH_REQUIRED、SOURCE_LINK_PRIVATE、SOURCE_LINK_EXPIRED、SOURCE_OUTCOME_UNKNOWN、DIRECT_DOWNLOAD_UNAVAILABLE、SOURCE_CHANGED、ORIGIN_BLOCKED、SEARCH_INCOMPLETE、REFERENCE_EXPIRED、BOOK_IDENTITY_CHANGED、FORMAT_UNAVAILABLE、FILE_INVALID、HASH_MISMATCH、LENGTH_MISMATCH、STORAGE_FULL、INPUT_MISSING、UPLOAD_OUTCOME_UNKNOWN、RECONCILIATION_REQUIRED。
