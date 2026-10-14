# 运维设计

当前是操作文档设计，程序和配置文件尚未实现。下面的键名与示例供开发者落地；发布前必须用实际程序验证并同步，不能当作已经可执行的安装指南。

## 部署形态

一个常驻 Go 服务，优先与 CWA 处于可访问其内网地址的环境。MCPHub 通过 Streamable HTTP 连接本服务；设备使用现有外网入口访问本服务签发的文件链接。

只挂载本服务自己的持久状态和暂存目录。无需 CWA 源码、Calibre 库目录、Docker socket 或主机 SSH 权限。

运行镜像包含程序和 CA 证书，以非 root 用户运行。初始容器限制和资源验收以 [内存预算](../plan/02-stack-and-memory.md) 为准；CWA 和 MCPHub 的内存不计入本服务 RSS，但部署容量仍由操作者安排。

## 计划配置

| 配置键 | 用途 |
| --- | --- |
| LISTEN_ADDR | 本服务监听地址 |
| PUBLIC_BASE_URL | 设备可达的文件接口前缀 |
| MCP_TOKEN_FILE | MCPHub 调用本服务的 Bearer 凭据文件 |
| TICKET_KEY_FILE | 文件票据签名密钥文件 |
| STATE_DIR | bbolt 和本服务私有状态 |
| STAGING_DIR | 有配额的临时书籍目录 |
| CWA_BASE_URL | 服务端访问 CWA 的地址，优先内网 |
| CWA_USERNAME / CWA_PASSWORD_FILE | 专用 CWA 账号及密码文件 |
| CWA_EXPECTED_FORMAT | 与现有 CWA 转换规则一致的预期格式 |
| CWA_WAIT_RETAINED_FORMATS | 是否等待 CWA 应保留的原格式 |
| CWA_MAX_UPLOAD_BYTES | 该通路实际允许的上传大小 |
| ZLIB_DOMAIN | 固定或验证后的书源入口 |
| ZLIB_SESSION_FILE | 书源会话，或按已选适配实现使用账号秘密文件 |
| MAX_FILE_BYTES / MAX_STAGING_BYTES | 文件与暂存配额 |
| GOMEMLIMIT | Go 运行时软限制，不是 RSS 硬限制 |

所有文件权限按最小可访问范围设置，秘密文件只读挂载。日志不打印配置秘密、完整 Cookie、签名 URL 或上传正文。

不把生产配置复制到仓库。后续提供 .env.example 和 Compose 示例时只包含占位符。

## MCPHub 配置草案

~~~json
{
  "mcpServers": {
    "cwa-books": {
      "type": "streamable-http",
      "url": "http://cwa-book-mcp:8080/mcp",
      "headers": {
        "Authorization": "Bearer <MCP_SERVICE_TOKEN>"
      },
      "perSessionClient": false,
      "enabled": true
    }
  }
}
~~~

这是待对照实际 MCPHub 版本验证的结构示例。内网上游地址可能需要在 MCPHub 的允许网络配置中显式放行。不要把示例占位符当成有效令牌。

MCPHub 的对外认证和本服务的上游认证是两层独立配置。设备只配置 MCPHub 工具入口，文件票据由工具动态返回；无需每台设备保存 CWA 账号。

## 首次接入检查

1. 确认实际 CWA 是 Calibre-Web Automated，并记录版本。
2. 验证服务账号具备搜索、下载和新书上传权限。
3. 记录转换目标、保留格式、忽略格式、自动合并和自动清理设置；不由程序修改。
4. 从本服务所在位置验证 CWA_BASE_URL，而不是只验证浏览器可访问。
5. 验证 PUBLIC_BASE_URL 对设备可达；沿用现有穿透方式，不由程序自动配置域名。
6. 验证 MCPHub 与首个 Agent 的协议、文件读写和 HTTP 能力。
7. 用可丢弃的合成书执行一次真实上传、转换、下载验收，并确认资源报告。

## 大文件与 Cloudflare

官方文档当前列出 Free/Pro 的单请求上传上限为 100 MB；实际区域设置还可能更低，部署时重新核对。

- 设备 → 本服务：应用分块可以把每次请求控制在限制内。
- 本服务 → CWA：CWA 接收的是完整 multipart 文件，请求上限依旧存在。
- 大文件需要这第二段走已可用的内网地址或满足已有通路限制。
- 不修改 CWA 来增加分块 API，也不把 HTTP chunked encoding 当作多请求。

引用：[Cloudflare 413 文档](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/4xx-client-error/error-413/)、[MCPHub 配置](https://docs.mcphub.app/configuration/mcp-settings)。

## 升级、备份与恢复

- 发布时固定镜像标签/摘要和依赖版本；升级前备份本服务状态。
- bbolt 使用事务内一致性备份，或停止服务后复制；不得不加说明地复制正写入的文件。
- 任务数据库与未完成暂存文件需要对应；只备份数据库时标明某些上传可能需要设备重新发送。
- schema 升级先备份、明确版本迁移；失败时保留旧库，不自动清空。
- 进程恢复先核对可能已经提交到 CWA 的任务，不直接重跑所有任务。
- CWA 本身的备份由其现有运维负责，本服务不替代书库备份。

## 故障处理

| 现象 | 操作方向 |
| --- | --- |
| AUTH_EXPIRED / CAPTCHA_REQUIRED | 在服务端恢复账号状态，然后显式重试任务 |
| CWA 上传返回 200 但长期不可下载 | 检查权限、格式规则和 CWA 日志；保持未确认，不反复上传 |
| 多个候选匹配 | 用户选择 CWA 候选，服务验证后 resolve |
| 413 | 区分设备上传段和 CWA 上传段，核对通路限制 |
| OOM 或 RSS 超标 | 保存脱敏性能摘要，分析分配/并发/响应限制，不直接扩大门槛 |
| 暂存空间耗尽 | 停止接收新任务，执行本服务到期清理，保留 CWA 书籍 |
| 书源请求长时间不取消 | 修复 adapter 的真正超时/取消，不只是缩短工具等待时间 |

详细失败语义以 [接口契约](../plan/03-contracts.md) 为准。
