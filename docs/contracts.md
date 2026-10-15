# 接口契约

stdio 是唯一 MCP 传输；HTTP 仅用于授权文件。默认启动无交互提示，stdout 只有 JSON-RPC，日志走 stderr。成功工具同时返回 structuredContent 与等价 JSON 文本；业务错误返回 isError:true 和 error:{code,message,retryable}。schema 无效输入由 SDK 拒绝。

## 工具

| 工具             | 输入与行为                                                                                                                                      |
| ---------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| search_books     | query 必填；author 可选；language 默认 zh_preferred，可用 any 或语言代码；formats 为 epub/pdf 数组；limit 默认 20、最大 50；cursor 可续查       |
| acquire_book     | action 默认 start，需要 request_id、candidate_id、required_format；retry/cancel 使用 task_id；resolve 使用 task_id 与 CWA candidate_id          |
| upload_book      | action 默认 prepare，需要 request_id、title、filename、size_bytes、required_format；可选 authors、language、sha256；refresh_ticket 使用 task_id |
| get_task         | task_id；返回当前持久任务和可供 resolve 的 choices                                                                                              |
| get_download_url | task_id；仅 ready，重新核验 CWA 身份与文件后返回 url、expires_at、size_bytes、sha256                                                            |

request_id 为调用方生成的 1–128 字符幂等键。创建请求先事务登记，再开始副作用。同 key、同参数返回原任务；不同参数返回 IDEMPOTENCY_CONFLICT。不同 request_id 仍会在获取/提交前核对书库。一个任务只要求 epub 或 pdf 一种输出格式。

search_books 返回 candidates、selected_tier、searched、partial、warnings、next_cursor。candidate_id 为服务端保存的短期标识，不能传 URL、任意文件路径或伪造上游参数。游标绑定查询条件，可修改 limit。候选和游标 30 分钟失效。

默认优先 CWA 中文 → 书源中文 → CWA 其他 → 书源其他，找到一层候选后只分页该层；language:any 采用 CWA → 书源。每次调用最多读取三个上游页，不完整时用 next_cursor 继续。上游失败会返回 partial/warnings，不降级为“无结果”；未配置书源时明确标记 SOURCE_NOT_CONFIGURED，并可返回书库其他语言结果。语言来自元数据，不从书名字符推断。

上传 title/authors/language 应与文件元数据对应，本工具不会改写书名。EPUB/PDF 以外输入拒绝；sha256 可用于整文件完整性校验。CWA 多个匹配进入 needs_review，由调用方选择 choices 中的候选。提交后因入库元数据差异无法自动确认时，也可重新 search_books，再 resolve 明确选择 CWA 记录；这不会改写元数据。resolve 始终保留语言与 ID/UUID 校验。

## 任务状态

| 状态                        | 含义 / 后续操作                                                           |
| --------------------------- | ------------------------------------------------------------------------- |
| awaiting_upload             | 等待按票据 PUT；可 refresh_ticket                                         |
| queued、downloading、staged | 书库核对、书源下载或完整文件待提交                                        |
| submitting                  | 已持久化提交意图；重启后一律先核对                                        |
| reconciling、verifying      | 等待 CWA，或实际验证目标格式文件                                          |
| blocked                     | 认证、权限、输入、格式或通路受阻；修复后 retry                            |
| needs_review                | 多个匹配或提交后无法确认；选择 choices，或人工检查后 retry                |
| ready                       | 已核验 CWA ID/UUID、元数据、目标格式、文件长度与格式，并记录 SHA-256/时间 |
| failed、cancelled           | 过期/失败，或主动取消；不删除 CWA 记录                                    |

ready 表示 verification.at 时点的验证结果；下载仍重新核对，不能使用曾经成功的结果绕过 CWA 删除、身份改变或文件损坏。get_task 不自动发放下载地址。

CWA HTTP 200、上传返回 location、书源成功下载都不等于 ready。EPUB 校验 ZIP 路径、重复/加密/符号链接条目、数量、展开大小、每条目 CRC、首条未压缩 mimetype、container、OPF 包结构与阅读顺序；PDF 校验头部和 EOF 标记。这是基础完整性验证，不是文档渲染或内容安全扫描。

retry 对已提交任务仅延长核对窗口，不重新下载或提交。提交前的输入文件丢失时，retry 恢复为 awaiting_upload（本地）或 queued（书源）。同一书籍已有未确认提交时，新任务返回 RELATED_SUBMISSION_PENDING 与 waiting_for，处理关联任务后再重试。上传请求已发出但响应断开时保留提交意图。cancel 会中止本地传输和跟踪，已被 CWA 接收的书籍/转换不会撤销。MCP 调用取消不删除已创建的持久任务；客户端断开后是否继续运行由宿主是否保留子进程决定。

## 文件接口

upload_book.prepare 返回：

```json
{
  "task_id": "<UUID>",
  "state": "awaiting_upload",
  "upload": {
    "method": "PUT",
    "url": "http://localhost:8765/files/upload/<UUID>?ticket=<SIGNED_TICKET>",
    "headers": { "Content-Length": "12345", "Content-Type": "application/epub+zip" },
    "expires_at": "<ISO8601>"
  }
}
```

按返回方法/地址发送整文件，必须提供准确 Content-Length；不接受 chunked 或压缩编码。通过长度、可选哈希和格式校验后原子发布暂存文件。再次发送相同任务与相同内容返回成功，不覆盖文件、不重复入库；内容不同返回冲突。单任务同时上传返回 UPLOAD_IN_PROGRESS。

下载为 GET /files/download/{task_id}?ticket=...，只从 CWA 取得目标格式。返回完整 HTTP 200、准确长度和 SHA-256 ETag；Range 请求返回 416。没有任意 URL 代理、服务端路径、静态目录列表或 HTTP MCP 路由。

票据 HMAC-SHA256 签名，绑定用途、任务、格式、期限；下载还绑定 CWA ID/UUID。错误/过期/用途不符返回 403。票据不能用于其他任务。HTTP 并发上限 4，额外请求返回 503；CWA 提交由单后台 worker 串行处理。GET 会先在临时磁盘完整校验，成功后才发送正文，传输结束清理临时副本。

常见错误：WRITE_DISABLED、AUTH_REQUIRED、AUTH_EXPIRED、CAPTCHA_REQUIRED、QUOTA_EXCEEDED、ORIGIN_BLOCKED、SEARCH_INCOMPLETE、REFERENCE_EXPIRED、BOOK_IDENTITY_CHANGED、FORMAT_UNAVAILABLE、FILE_INVALID、HASH_MISMATCH、LENGTH_MISMATCH、STORAGE_FULL、UPLOAD_OUTCOME_UNKNOWN、RECONCILIATION_REQUIRED。所有工具时间戳为 Unix 毫秒；票据内部 expires 为 Unix 秒，客户端链接的 expires_at 为 ISO8601。
