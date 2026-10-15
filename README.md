# cwa-book-mcp

通过 MCP 搜索 CWA 书库、从配置的 Z-Library 书源获取书籍、上传本地 EPUB/PDF，并取得经过验证的 CWA 下载地址。CWA 负责入库与转换。

Node.js + TypeScript，单进程，stdio MCP，npm 分发。推荐 Node.js 24 LTS，最低 22.19；无需本项目 Docker 容器。

**当前处于 0.1.0 开发验收，尚未发布 npm。** 发布后，支持本地进程的 MCP 客户端可使用：

```json
{
  "mcpServers": {
    "cwa-books": {
      "command": "npx",
      "args": ["-y", "cwa-book-mcp@0.1.0"],
      "env": {
        "CWA_BASE_URL": "http://localhost:8083",
        "CWA_USERNAME": "<CWA_USER>",
        "CWA_PASSWORD": "<CWA_PASSWORD>"
      }
    }
  }
}
```

写入默认关闭。确认 CWA 上传权限、转换与重复处理设置后，再启用 CWA_WRITE_ENABLED=true。书源是可选配置；仅配置 CWA 即可搜索、读取书库。

手机与网页客户端通过远端 MCPHub 使用此进程。远端文件链接还需设置 Agent 可达的 PUBLIC_BASE_URL。不同客户端的 JSON 外层格式不同，详见[配置与接入](docs/configuration.md)。

五个工具：search_books、acquire_book、upload_book、get_task、get_download_url。文件通过短时授权 HTTP 链接传输，不经过 MCP 消息。

开发：`npm ci` → `npm run check` → `npm run build`。从当前仓库启动使用 `node dist/cli.js`，验收打包使用 `npm run test:package`。

[当前方案与进度](PLAN.md) · [配置](docs/configuration.md) · [接口契约](docs/contracts.md) · [验证结果](docs/validation.md)
