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

## 建议模块

以下是将来的代码布局，当前不创建空实现。

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
testdata/               小型合成书籍和脱敏 HTTP fixture
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

格式规则从部署配置声明，并在真实验收时与 CWA 对照。本服务不擅自改变转换目标。

## 本地上传

upload_book 准备任务和上传票据；Agent 使用 HTTP 发送文件。完整文件验收、落盘后才进入后台队列。上传过程尚未完成时，设备断开需要恢复上传；已经完成上传的后台任务继续运行。

应用级分块采用独立的小请求，最终文件仍保存在磁盘。HTTP Transfer-Encoding: chunked 不是绕过代理单请求限制的办法。

## 状态机

~~~mermaid
stateDiagram-v2
    [*] --> awaiting_upload: 本地文件
    [*] --> queued: 书源获取
    awaiting_upload --> queued: 完整文件验收
    queued --> downloading: 需要书源文件
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
    retry_wait --> queued: 到期重试
    ready --> [*]
~~~

实现还需允许各阶段进入 failed（明确且不可重试的错误）。blocked、needs_review、uncertain 保留任务与原因；get_task 不自动重发上传。

## 持久化与恢复

- request_id 是幂等键；同键同参数返回原任务，同键不同参数报 IDEMPOTENCY_CONFLICT。
- 在外部副作用之前提交意图，之后提交结果。上传响应丢失不允许当作从未上传。
- 重启后：未完整上传的文件恢复到已确认偏移；downloading 默认清理未完成下载后有限重试，不声称支持源站 Range 续传。
- submitting / uncertain / reconciling 一律先查 CWA；无法判断时进入 needs_review。
- 单 worker 负责书源获取与 CWA 提交；等待入库使用持久化 next_poll_at 调度，不为每个任务保留等待 goroutine。
- bbolt 事务保持短小；网络请求、文件复制和等待均在事务外进行。
- 同一业务任务具有进程内互斥与持久状态校验，多个设备不会重复执行。
- 只有一个服务实例可打开状态文件。首版不实现多副本高可用。
- 干净退出停止领取任务，给在途传输有限收尾时间，保存状态；不把服务器退出传播为永久业务失败。

## 入库确认的语义

ready 表示 CWA 已提供满足本次基本条件的书籍格式，不等于逐字证明这份内容就是源站指定译本，也不等于 CWA 所有后台工作已经结束。

完整规则、歧义处理和无需改 CWA 的限制见 [CWA 集成](04-cwa-integration.md)。
