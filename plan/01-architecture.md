# 架构

## 服务与数据流

~~~mermaid
flowchart LR
    A["设备上的 Agent"] -->|"MCP 工具"| H["MCPHub"]
    H -->|"Streamable HTTP /mcp"| M["cwa-book-mcp：一个 Go 进程"]
    A <-->|"临时授权文件接口"| M
    M -->|"搜索、详情、下载"| Z["Z-Library"]
    M <-->|"OPDS / 网页上传 / 下载"| C["原版 CWA"]
    M --> B["本服务 bbolt：任务与幂等记录"]
    M --> T["本服务磁盘暂存"]
    C --> L["唯一持久书库"]
~~~

MCPHub 承担工具聚合，不传输书籍二进制。MCP 与文件接口位于同一监听服务；后台任务不依赖某个 MCP 会话存活。

CWA 会话可以跨业务请求复用，但保存在 CWA adapter 内，与 MCP 传输会话无关。MCP 请求尽量无会话状态，避免 MCPHub 为每个设备建立独立业务进程。

## 实现模块

以下目录已实现；合成书和 HTTP 替身在测试时生成，不保存实际书籍。

~~~text
cmd/cwa-book-mcp/        入口、配置、生命周期
internal/mcp/           五个工具的 schema 和调用适配
internal/catalog/       中文优先编排、规范化和匹配
internal/source/zlib/   heartleo/zlib 封装、账号状态与限制
internal/cwa/           OPDS、登录、CSRF、上传和下载验证
internal/jobs/          状态机、调度、恢复和有限重试
internal/store/         bbolt、幂等键、到期索引与迁移
internal/files/         磁盘暂存、上传票据、CWA 下载转发
internal/config/        配置和启动校验
internal/domain/        受限模型、错误、语言和基本匹配
internal/httpx/         有界响应、截止时间、origin 和重定向
internal/server/        Bearer MCP 入口及授权文件 HTTP 路由
internal/diagnostics/   显式启用的私有开发指标文件
internal/testfixture/   原创合成书和受控 HTTP 替身
internal/integration/   显式启用的隔离原版 CWA 验收
tools/                  资源负载、报告门槛、测试书、许可证与打包工具
~~~

不额外建立通用 provider 插件框架。仅对需要测试替身或隔离上游变化的边界使用接口。

## 所有权

| 对象 | 所有者 |
| --- | --- |
| 最终书籍、格式、转换、重复处理 | CWA |
| Z-Library 登录状态、源候选、下载 | source adapter |
| CWA 登录状态与 CSRF | CWA adapter |
| 业务任务、幂等请求、CWA ID 对应关系 | jobs + store |
| 临时文件、上传会话、清理 | files |
| 设备端的保存位置和实际 HTTP 请求 | Agent |

bbolt 不保存整本书、HTML 页面或整库索引。映射只帮助恢复与去重，不能作为书籍仍存在的证明；交付前必须再查询 CWA。

## 获取新书

1. 接收用户选中的候选和 request_id，持久化任务。
2. 再查 CWA，能复用则验证下载并 ready。
3. 否则后台 worker 将书源文件流式写入任务专属 .part 文件。
4. 检查长度、类型和基本完整性；同步并原子发布为暂存成品。
5. 在提交前记录 submitting 状态和核对线索。
6. 使用 CWA 网页会话流式上传；响应只表示受理线索。
7. polling/reconciling 核对 CWA 候选及目标格式，验证实际下载。
8. ready 后签发 CWA 授权转发链接，清理暂存书籍。

每个任务只有一个 required_format，规则见 [CWA 集成](04-cwa-integration.md)。本服务不擅自改变转换目标。

## 本地上传

upload_book 准备任务、匹配信息和上传票据；Agent 使用一次流式 PUT 发送完整文件。服务校验、落盘并入队后返回受理结果；已经完成上传的后台任务继续运行。

未完成的上传断开后从头重传，不实现应用分块、偏移协议或独立 complete 接口。若响应丢失，Agent 先 get_task；完整文件已经接收时不能重新上传覆盖。具体规则见 [接口契约](03-contracts.md)。

## 状态机

~~~mermaid
stateDiagram-v2
    [*] --> awaiting_upload: 本地文件
    [*] --> queued: 书源获取
    awaiting_upload --> queued: 完整文件验收
    queued --> downloading: 需要书源文件
    queued --> staged: 已有完整暂存文件
    queued --> verifying: CWA 已有候选
    downloading --> staged
    staged --> submitting
    submitting --> reconciling
    reconciling --> verifying: 找到唯一合格候选
    verifying --> ready: CWA 文件验证通过
    downloading --> retry_wait: 临时失败
    submitting --> uncertain: 提交结果未知
    uncertain --> reconciling: 先查库
    reconciling --> needs_review: 多候选或无法确认
    downloading --> blocked: 认证或额度
    verifying --> blocked: 权限问题
    retry_wait --> queued: 从持久化恢复点继续
    needs_review --> verifying: 用户确认 CWA 记录
    blocked --> queued: 凭据恢复后显式重试
    ready --> [*]
~~~

图中省略通用终止边：不可恢复错误进入 failed；用户取消或任务到期进入 cancelled。终态为 ready、failed、cancelled。blocked、needs_review、uncertain 都不是成功或终态，保留原因并占用未结束任务名额；保留与自动到期规则见 [资源上限](02-stack-and-memory.md)。

状态不能单独决定恢复动作。持久化任务来源、输入成品是否完整、resume_step、submission_attempted、匹配条件、required_format 和核对截止时间。queued 先检查这些信息，再选择验证现有 CWA、下载、使用暂存文件或查库；禁止一律重跑下载。

## 持久化与恢复

- request_id 的作用域、保留期和票据刷新语义以 [接口契约](03-contracts.md) 为准。
- 在外部副作用之前提交意图，之后提交结果。上传响应丢失不允许当作从未上传。
- 重启后：未完成的设备 .part 文件清理，任务回到 awaiting_upload；已有原子发布的完整文件先校验，再补记 queued。downloading 清理未完成下载后有限重试，不声称支持源站 Range 续传。
- submitting / uncertain / reconciling 一律先查 CWA；无法判断时进入 needs_review。
- 已提交任务即使经过 blocked、retry_wait、needs_review，显式 retry 也只恢复核对；不得清空 submission_attempted。只有能证明请求未发出或被明确拒绝时，才允许再次提交。
- 单 worker 执行一个可运行的下载、提交或验证步骤；提交后释放 worker。等待入库使用持久化 next_poll_at 调度，不为每个任务保留等待 goroutine，也不持有文件传输许可等待轮询。
- bbolt 事务保持短小；网络请求、文件复制和等待均在事务外进行。
- 同一任务具有进程内互斥与持久状态校验。不同 request_id 选择同一源站 ID、语言与目标格式时，复用尚未结束的获取任务；这只是精确任务键去重，不进行跨来源或译本身份推断。
- 每次实际文件流只领取一次全局传输许可，结束就释放；下载、提交和验证顺序执行，不嵌套领取造成死锁。等待任务也不占控制请求名额。
- 只有一个服务实例可打开状态文件。首版不实现多副本高可用。
- 干净退出停止领取任务，给在途传输有限收尾时间，保存状态；不把服务器退出传播为永久业务失败。

取消只停止本服务后续动作、清理本服务文件。若 CWA 已可能接收，返回 cwa_submission_may_have_succeeded；CWA 仍可能完成入库。取消和自动到期均不得删除 CWA 记录，也不撤销已经发生的外部副作用。

## 入库确认的语义

ready 表示 CWA 已提供满足本次基本条件的书籍格式，不等于逐字证明这份内容就是源站指定译本，也不等于 CWA 所有后台工作已经结束。

完整规则、歧义处理和无需改 CWA 的限制见 [CWA 集成](04-cwa-integration.md)。
