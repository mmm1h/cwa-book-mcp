# MCP 与文件接口契约

本页描述已实现的首版契约。实际 JSON Schema 由官方 SDK 从输入类型生成，五个工具在 internal/mcp 注册并经协议测试覆盖。新增工具或改变语义需要同步本页。

## 共通规则

- MCP 路径 /mcp，使用 Streamable HTTP；业务后台任务独立于工具请求。
- 工具返回结构化小结果；禁止书籍字节、Cookie、密码、源站下载凭据进入工具结果。
- 业务错误在 isError=true 时仍保留 structuredContent.error 的 code、message、retryable，另提供文本 JSON；并发背压可有限重试。
- 标识符由服务产生。Agent 不能指定服务器文件路径、任意下载 URL 或任意 CWA 上游地址。
- 用户选择候选后才调用 acquire_book；候选过期返回 CANDIDATE_EXPIRED，重新搜索取得新候选。已创建任务使用自己的快照，不依赖候选缓存继续存活。
- 时间用 UTC RFC 3339，大小用字节，格式用小写规范名。
- request_id 在 acquire_book、upload_book 的所有 action 中必填；幂等冲突不可静默覆盖。
- 所有返回中的 ready 必须满足 [CWA 核对规则](04-cwa-integration.md)。

幂等键为（工具名、action、request_id）。在任务历史保留期内，同键同参数返回第一次操作的结果/关联任务，同键不同参数报 IDEMPOTENCY_CONFLICT；校验幂等键先于检查候选是否过期。同一业务重试沿用该键，新业务使用新键。刷新票据若重放原键，返回原票据；要取得新的有效期必须使用新的 request_id。

候选、任务历史和票据时限的数值只在 [资源上限](02-stack-and-memory.md) 定义。历史被清理后不再承诺对旧键去重，客户端不能把旧键重新用于新业务。

## 五个工具

| 工具 | 主要输入 | 主要输出 | 副作用 |
| --- | --- | --- | --- |
| search_books | query；可选 author、language、formats、cursor、limit | candidates、selected_tier、searched、partial、warnings、next_cursor | 只查询，允许有界候选缓存 |
| acquire_book | action、request_id；按 action 提供 candidate_id 或 task_id | task_id、state、reused_existing | 创建、恢复、确认或取消任务 |
| upload_book | action、request_id；prepare 时 title、filename、size_bytes；可选 author/language/sha256/required_format | task_id、upload_ticket、传输说明 | 准备本地上传 |
| get_task | task_id | state、error、CWA 候选/ID、实际格式、received_bytes、next_poll_after_seconds、expires_at | 仅查询 |
| get_download_url | cwa_book_id、format；可选 task_id | url、expires_at、filename、size_bytes | 签发临时下载票据 |

最短调用流程：

- CWA 已有书：search_books → 用户选择 → get_download_url（选择已有格式）→ Agent 完整 GET，无需先建获取任务。
- 获取新书：search_books → 用户选择 → acquire_book(start) → get_task，ready 后 get_download_url → Agent 完整 GET。
- 上传本地书：upload_book(prepare) → Agent 完整 PUT → get_task，ready 后按需取 CWA 文件。

### search_books

language 默认 zh_preferred，另支持显式语言代码或 any。精确排序规则只维护在 [scope](00-scope.md)，这里不复制算法。

每个候选包含：candidate_id、origin（cwa/zlibrary）、title、authors、language（允许 unknown）、formats、size（可空）、available_in_cwa，以及必要的来源 ID。不要把源站真实下载 URL 当成候选交给设备。

正常一次返回一个优先级层的候选，selected_tier 标明 cwa_zh、zlibrary_zh、cwa_other 或 zlibrary_other；显式语言/any 使用 cwa 或 zlibrary。没有候选时为 null。partial=true 时说明未完成或失败的渠道；已有英文候选不能掩盖中文渠道查询失败。不同渠道结果不按标题粗暴合并成一个条目。

cursor 绑定规范化查询、筛选条件、当前层级和分页位置，禁止任意 URL。翻页不重放第一页；条件变化或游标过期返回 CURSOR_INVALID。达到页数预算仍未完成时保留 next_cursor，并标记 incomplete；不得悄悄把未查完当作查无结果。一次调用的总查询与时间预算见资源上限。

### acquire_book

- action=start：candidate_id，required_format 可选，提交前复核 CWA。
- action=retry：task_id；仅恢复 blocked、retry_wait、needs_review、uncertain。未排除 CWA 已接收时只恢复核对，不能强制重传；有证据表明未发送或明确拒绝时，按架构中的恢复点处理。ready、failed、cancelled 为终态；其他运行状态不重复入队。
- action=resolve：task_id、cwa_book_id；供用户明确选择 needs_review 中的 CWA 记录。接受该任务提供的候选，或本服务近期搜索返回的 CWA 候选；不接受未经读取的任意 ID。人工确认的元数据差异与验证规则见 CWA 集成。
- action=cancel：task_id；停止本服务的后续任务、释放本服务资源，不删除或撤销 CWA 中的内容；重复取消返回原终态。可能已提交时返回 cwa_submission_may_have_succeeded。
- retry/resolve 可作用于本地上传产生的后续入库任务，不能隐式重建缺失的设备本地文件。
- required_format 未指定时使用 CWA_EXPECTED_FORMAT，并在创建任务时固定。用户选中的 CWA 候选已经有该格式时只做复用验证；缺少时返回 FORMAT_UNAVAILABLE，不把 CWA 候选当作自动获取新书的指令。
- start 在任务已持久化后尽快返回，不等待书源下载和 CWA 转换。

### upload_book

- action=prepare：提供可用于查库的 title、文件名、明确大小和 request_id，服务分配 awaiting_upload 任务。author、language 仅在知道时提供；文件名只用于展示与类型判断，不能代替匹配身份。
- title 缺失返回 INVALID_ARGUMENT 并说明需补充书名；首版不要求自己实现 PDF/各类书籍的元数据提取器。不知道的作者或语言必须留空。
- action=refresh_ticket：task_id；只用于仍为 awaiting_upload 且未到期的任务，保持匹配条件、大小和哈希约束。无完整文件时从头上传。
- 可选 sha256 是输入文件完整性信息；转换后不以源文件哈希等于目标文件哈希作为成功条件。
- prepare 的 size_bytes 超过文件、暂存空间或已知 CWA 通路限制时立即拒绝。
- 工具返回 method=PUT、url、headers、expires_at、size_bytes 和 task_id；headers 声明 Content-Type: application/octet-stream、Content-Length。Agent 流式发送本地文件，不需学习 CWA 登录或 CSRF。

### get_task

state 使用架构中的状态名，error 含 code、message、retryable 和可选 retry_after_seconds。仅在实际传输时报告已传字节；它是进度而非续传偏移，CWA 转换阶段不伪造百分比。get_task 不刷新票据、不触发重新上传，也不依赖 MCP 协议的 Tasks 扩展。

ready 返回 CWA ID、已验证格式和 verification_basis。该字段必须区分 reused_existing、matched_after_submission、user_resolved；不声称证明了某个译本或逐字相同的文件。

### get_download_url

只能为 CWA 书籍签发链接，禁止为源站文件或本地暂存文件签发对外下载链接。签发时查询该记录、格式与权限，size_bytes 无可靠值时返回 null；给出 task_id 时须与 ready 任务的 CWA ID、格式一致。签发本身不创建任务或把任务标成 ready。

不带 task_id 时，使用本服务近期搜索或任务返回的 CWA ID；对应身份记录被有界缓存回收后，重新搜索再签发。已经签发且未到期的链接独立于该缓存，GET 仍通过票据内的 ID/UUID 验证 CWA 记录。

实际 GET 再校验 CWA 响应并流式转发；记录删除或授权失效时报告错误。设备端保存到哪里由 Agent 决定。使用默认格式的任务，不影响用户直接请求 CWA 中已经存在的其他格式。

## 同进程文件接口

| 接口 | 用途 |
| --- | --- |
| PUT /files/uploads/{id} | 按上传票据接收一个完整文件，校验后自动入队 |
| GET /files/downloads/{ticket} | 按票据从 CWA 流式转发 |
| GET /healthz | 本服务存活；不主动访问外部账号 |
| GET /readyz | 配置与本服务存储可用；外部故障在工具中单独报告 |

具体 URL 是否把签名置于路径或查询参数由实现统一决定，日志必须脱敏。不能同时引入第二套长期客户端凭据体系。

### 单文件上传

- 一次 PUT 接收整个文件，仍以固定小缓冲流式写盘。明确 Content-Length，与 prepare 的 size_bytes 一致，并在读取中强制上限。
- 同一任务只允许一个在途 PUT；并发写返回 UPLOAD_IN_PROGRESS。上传票据绑定任务、输入约束、用途与有效期。
- 收完后校验总长度、类型、基本完整性及可选 SHA-256，落盘并原子发布，再持久化 queued；HTTP 202 返回 task_id/state，无需 complete 调用。
- 断开、超时、长度错误或哈希错误清理 .part，保持 awaiting_upload；可在任务有效期内用原票据或刷新票据从头重试。
- 响应丢失先 get_task。已接收完整文件时重复 PUT 只返回现有任务，不覆盖成品，也不再次提交 CWA。
- 首版不实现分块、续传偏移、乱序写或额外上传状态端点；task_id 是恢复与查询入口。

### 下载与授权

- MCPHub 到 /mcp 使用配置的 Bearer 凭据；文件端点使用短期、用途受限的票据。
- 下载票据绑定 CWA ID、UUID、格式和到期时间，不依赖候选缓存保留；在有效期内可重试完整 GET。首版不支持 Range/断点续传，不返回 206；传输失败需重新获取完整文件。
- 转发请求在服务端附加 CWA 凭据；源站/CWA Cookie 不返回给设备。
- Content-Type、Content-Disposition、长度和重定向要验证；登录页即使 HTTP 200 也不是有效书籍。
- 文件响应 Cache-Control: private, no-store。票据、账号和敏感查询参数不进入日志。
- 上游连接失败不能在已返回部分正文后改成 JSON 成功；让客户端发现不完整传输并按规则重试。
- 输入 URL 与重定向必须受上游 origin/域名配置约束。CWA 的显式内网地址可以访问，但不能把接口变为任意 URL 代理。

## 错误与重试

| 错误码 | 行为 |
| --- | --- |
| INVALID_ARGUMENT / IDEMPOTENCY_CONFLICT | 调用方修正输入 |
| CANDIDATE_EXPIRED / CURSOR_INVALID | 重新搜索，不影响已存在的任务 |
| TASK_NOT_FOUND / TASK_EXPIRED | 任务不存在或到期；按需创建新业务 |
| CWA_UNAVAILABLE / SOURCE_UNAVAILABLE | 有限重试；不当成无书 |
| AUTH_REQUIRED / AUTH_EXPIRED | blocked，服务端凭据恢复后显式重试 |
| CAPTCHA_REQUIRED / QUOTA_EXCEEDED | blocked；不给绕过方案或无限重试 |
| RATE_LIMITED / BUSY | 返回 retry_after_seconds |
| FILE_TOO_LARGE / STORAGE_FULL | 拒绝接收或暂停任务，清理未完成部分；完整文件按任务保留规则处理 |
| UPLOAD_IN_PROGRESS / LENGTH_MISMATCH / CHECKSUM_MISMATCH | 不进入后台队列；查询状态或从头重试设备上传 |
| UPLOAD_OUTCOME_UNKNOWN | uncertain，先核对书库 |
| IMPORT_UNCONFIRMED | 超时尚未确认；进入 needs_review，不自动再次上传 |
| AMBIGUOUS_CWA_MATCH | needs_review，提供有限候选供选择 |
| FORMAT_NOT_READY | 继续有界轮询 |
| FORMAT_UNAVAILABLE / UNSUPPORTED_FORMAT | 现有记录缺所选格式，或当前 CWA 规则不支持生成；不能无限等待 |
| CWA_FILE_INVALID | 不 ready；保留核验错误 |

协议层错误与业务错误分别按 MCP SDK 规范表达。错误详情不能带完整响应页面、凭据或本机路径。

后台自动重试仅针对可证明安全的临时错误，同一步最多 3 次尝试，带退避且不超过任务总寿命；耗尽转 blocked，允许显式 retry。CWA 确认轮询不是重复上传，预算在 CWA 集成文档定义。错误发生在查询工具时直接返回错误，不创建一个虚假的 blocked 任务。
