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
| CWA_MAX_UPLOAD_BYTES | CWA 通路实际允许的整个 multipart 请求大小，须扣除协议开销计算文件上限 |
| ZLIB_DOMAIN | 固定或验证后的书源入口 |
| ZLIB_SESSION_FILE | 书源会话，或按已选适配实现使用账号秘密文件 |
| MAX_FILE_BYTES / MAX_STAGING_BYTES | 文件与暂存配额；前者同时按设备文件入口的实际请求上限收紧 |
| GOMEMLIMIT | Go 运行时软限制，不是 RSS 硬限制 |

MCP_TOKEN_FILE、TICKET_KEY_FILE 和密码文件只读挂载。ZLIB_SESSION_FILE 位于本服务私有状态目录，适配器需要保存刷新后的登录状态时允许安全写回，不能一律只读挂载导致刷新失败。日志不打印配置秘密、完整 Cookie、签名 URL 或上传正文。

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

- 设备 → 本服务：首版是完整单次 PUT；MAX_FILE_BYTES 必须小于等于该段实际请求上限。
- 本服务 → CWA：完整 multipart 文件，CWA_MAX_UPLOAD_BYTES 还要为头部、CSRF 字段与边界留出空间。
- 允许的文件大小取应用配置与两段通路上限中的较小值；通路上限由操作者填写，不宣称能自动探测未知代理限制。MB 与 MiB 应转换为字节再比较。
- 例如某段已确认上限为 100 MB 时，可以把 MAX_FILE_BYTES 收紧到 90 MiB，为协议开销留余量；不按 512 MiB 应用默认值直接放行。
- 大文件需要每一段都能承受完整请求。CWA 内网地址只解决第二段，不能解决设备入口的限制。
- 首版不通过应用分块规避此限制；若以后确有大文件需求，再单独评估续传。HTTP chunked encoding 也不等于多个请求。

引用：[Cloudflare 413 文档](https://developers.cloudflare.com/support/troubleshooting/http-status-codes/4xx-client-error/error-413/)、[MCPHub 配置](https://docs.mcphub.app/configuration/mcp-settings)。

## 升级、备份与恢复

- 发布时固定镜像标签/摘要和依赖版本；升级前备份本服务状态。
- bbolt 使用事务内一致性备份，或停止服务后复制；不得不加说明地复制正写入的文件。
- 任务数据库与完整暂存文件需要对应；只备份数据库时标明某些输入需要重新获取。设备上传的未完成 .part 文件不恢复偏移，重启后从头发送。
- schema 升级先备份、明确版本迁移；失败时保留旧库，不自动清空。
- 进程恢复先核对可能已经提交到 CWA 的任务，不直接重跑所有任务。
- CWA 本身的备份由其现有运维负责，本服务不替代书库备份。

## 故障处理

| 现象 | 操作方向 |
| --- | --- |
| AUTH_EXPIRED / CAPTCHA_REQUIRED | 在服务端恢复账号状态，然后显式重试任务 |
| CWA 上传返回 200 但长期不可下载 | 检查权限、格式规则和 CWA 日志；保持未确认，不反复上传 |
| 多个候选匹配 | 用户选择 CWA 候选，服务验证后 resolve |
| 没有可确认候选 | 重新 search_books 获取 CWA 候选；用户确认后 resolve，不能直接改成 ready |
| 不再需要的等待/阻塞任务 | acquire_book 的 cancel 释放本服务资源；已发生的 CWA 上传不会撤销 |
| 413 | 区分设备上传段和 CWA 上传段，核对通路限制 |
| OOM 或 RSS 超标 | 保存脱敏性能摘要，分析分配/并发/响应限制，不直接扩大门槛 |
| 暂存空间耗尽 | 停止接收新任务，执行本服务到期清理，保留 CWA 书籍 |
| 书源请求长时间不取消 | 修复 adapter 的真正超时/取消，不只是缩短工具等待时间 |

详细失败语义以 [接口契约](../plan/03-contracts.md) 为准。
