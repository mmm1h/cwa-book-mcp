# MCP 与文件接口契约

本页描述待实现的首版契约。P1 建立实际 JSON Schema 并使测试覆盖字段约束。新增工具或改变语义需要同步本页。

## 共通规则

- MCP 路径 /mcp，使用 Streamable HTTP；业务后台任务独立于工具请求。
- 工具返回结构化小结果；禁止书籍字节、Cookie、密码、源站下载凭据进入工具结果。
- 标识符由服务产生。Agent 不能指定服务器文件路径、任意下载 URL 或任意 CWA 上游地址。
- 用户选择候选后才调用 acquire_book；候选过期需要重新解析书源信息。
- 时间用 UTC RFC 3339，大小用字节，格式用小写规范名。
- request_id 在写入工具中必填；幂等冲突不可静默覆盖。
- 所有返回中的 ready 必须满足 [CWA 核对规则](04-cwa-integration.md)。

## 五个工具

| 工具 | 主要输入 | 主要输出 | 副作用 |
| --- | --- | --- | --- |
| search_books | query；可选 author、language、formats、cursor、limit | candidates、selected_tier、searched、partial、warnings、next_cursor | 仅查询 |
| acquire_book | action、request_id；按 action 提供 candidate_id 或 task_id 等 | task_id、state、reused_existing | 创建/恢复任务 |
| upload_book | action、request_id；prepare 时 filename、size_bytes、可选 sha256/required_format | task_id、upload_ticket、传输说明 | 准备本地上传 |
| get_task | task_id | state、stage、error、CWA 候选/ID、实际格式、next_poll_after_seconds | 仅查询 |
| get_download_url | cwa_book_id、format；可选 task_id | url、expires_at、filename、size_bytes | 签发临时下载票据 |

### search_books

language 默认 zh_preferred，另支持显式语言代码或 any。精确排序规则只维护在 [scope](00-scope.md)，这里不复制算法。

每个候选包含：candidate_id、origin（cwa/zlibrary）、title、authors、language（允许 unknown）、formats、size（可空）、available_in_cwa，以及必要的来源 ID。不要把源站真实下载 URL 当成候选交给设备。

selected_tier 说明结果来自哪一级。partial=true 时必须给出尚未完成或失败的渠道；已有英文候选不能掩盖中文渠道查询失败。不同渠道结果不按标题粗暴合并成一个条目。

### acquire_book

- action=start：candidate_id，required_format 可选，提交前复核 CWA。
- action=retry：task_id；只能恢复允许重试的状态，上传结果未知时先 reconciling。
- action=resolve：task_id、cwa_book_id；供用户明确选择 needs_review 中的 CWA 候选。仅接受该任务候选，仍验证语言、格式和下载。
- retry/resolve 可作用于本地上传产生的后续入库任务，不能隐式重建缺失的设备本地文件。
- required_format 未指定时使用部署时声明的 CWA 预期输出规则；不能默认为任意当前出现的格式就完成。
- start 在任务已持久化后尽快返回，不等待书源下载和 CWA 转换。

### upload_book

- action=prepare：提供文件名、明确大小和 request_id，服务分配上传任务；文件名只用于展示与类型判断。
- action=refresh_ticket：task_id，保持已有上传偏移和内容约束，刷新短期票据。
- 可选 sha256 是输入文件完整性信息；转换后不以源文件哈希等于目标文件哈希作为成功条件。
- prepare 的 size_bytes 超过文件、暂存空间或已知 CWA 通路限制时立即拒绝。
- 工具返回机器可执行的 HTTP 方法、URL、必要头、分块大小、查询偏移与完成地址，Agent 不需学习 CWA 登录或 CSRF。

### get_task

返回可理解的阶段和稳定错误码。仅在实际传输时报告已传字节；CWA 转换阶段不伪造百分比。

ready 返回 CWA ID、已验证格式和 verification_basis。该字段必须区分 reused_existing、matched_after_submission、user_resolved；不声称证明了某个译本或逐字相同的文件。

### get_download_url

只能为 CWA 书籍签发链接，禁止为源站文件或本地暂存文件签发对外下载链接。设备端保存到哪里由 Agent 决定。

## 同进程文件接口

| 接口 | 用途 |
| --- | --- |
| PUT /files/uploads/{id} | 流式发送完整小文件或一个应用分块 |
| GET /files/uploads/{id} | 查询已确认偏移和上传状态 |
| POST /files/uploads/{id}/complete | 校验总长度/可选总哈希，原子发布暂存文件并入队 |
| GET /files/downloads/{ticket} | 按票据从 CWA 流式转发 |
| HEAD /files/downloads/{ticket} | 获取受限文件信息，不返回正文 |
| GET /healthz | 本服务存活；不主动访问外部账号 |
| GET /readyz | 配置与本服务存储可用；外部故障在工具中单独报告 |

具体 URL 是否把签名置于路径或查询参数由实现统一决定，日志必须脱敏。不能同时引入第二套长期客户端凭据体系。

### 分块上传

- 默认块大小 8 MiB；各块也是流式写盘，不缓存整个块。
- 每块声明 Upload-Offset、Content-Length 和块 SHA-256；只接受连续偏移。
- 已确认块重复提交且哈希一致时幂等成功；冲突返回 OFFSET_CONFLICT。
- 元数据记录确认偏移。进程中断时磁盘可能有未确认尾部，恢复到最后确认偏移再续传。
- complete 是幂等操作，必须先确认总长度、类型、可选总哈希及落盘，再进入 queued。
- 上传票据与用户提交的任务/长度/用途绑定；过期后刷新，不能改变文件身份。
- 不支持首版多块并行乱序上传，避免稀疏文件和额外常驻状态。

### 下载与授权

- MCPHub 到 /mcp 使用配置的 Bearer 凭据；文件端点使用短期、用途受限的票据。
- 票据绑定 CWA ID、格式和到期时间，可支持到期前有界重试与 Range；不宣称一次性票据同时支持无限续传。
- 转发请求在服务端附加 CWA 凭据；源站/CWA Cookie 不返回给设备。
- Content-Type、Content-Disposition、长度和重定向要验证；登录页即使 HTTP 200 也不是有效书籍。
- 文件响应 Cache-Control: private, no-store。票据、账号和敏感查询参数不进入日志。
- 上游连接失败不能在已返回部分正文后改成 JSON 成功；让客户端发现不完整传输并按规则重试。
- 输入 URL 与重定向必须受上游 origin/域名配置约束。CWA 的显式内网地址可以访问，但不能把接口变为任意 URL 代理。

## 错误与重试

| 错误码 | 行为 |
| --- | --- |
| INVALID_ARGUMENT / IDEMPOTENCY_CONFLICT | 调用方修正输入 |
| CWA_UNAVAILABLE / SOURCE_UNAVAILABLE | 有限重试；不当成无书 |
| AUTH_REQUIRED / AUTH_EXPIRED | blocked，服务端凭据恢复后显式重试 |
| CAPTCHA_REQUIRED / QUOTA_EXCEEDED | blocked；不给绕过方案或无限重试 |
| RATE_LIMITED / BUSY | 返回 retry_after_seconds |
| FILE_TOO_LARGE / STORAGE_FULL | 拒绝接收或停止传输并清理未完成部分 |
| OFFSET_CONFLICT / CHECKSUM_MISMATCH | 上传会话保留可恢复状态，不能进入队列 |
| UPLOAD_OUTCOME_UNKNOWN | uncertain，先核对书库 |
| IMPORT_UNCONFIRMED | 超时尚未确认；进入 needs_review，不自动再次上传 |
| AMBIGUOUS_CWA_MATCH | needs_review，提供有限候选供选择 |
| FORMAT_NOT_READY | 继续有界轮询 |
| CWA_FILE_INVALID | 不 ready；保留核验错误 |

协议层错误与业务错误分别按 MCP SDK 规范表达。错误详情不能带完整响应页面、凭据或本机路径。
