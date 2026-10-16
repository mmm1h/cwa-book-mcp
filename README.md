# cwa-book-mcp

通过 MCP 搜索 CWA 和 Z-Library，把经过校验的 EPUB/PDF 原文件链接交给 Agent，并默认自动加入 CWA。CWA 负责持久入库、转换和去重。

Node.js + TypeScript，单进程，stdio MCP，npm 分发。推荐 Node.js 24 LTS，最低 22.19；无需本项目 Docker 容器。

**0.3.0 新增 download_book：默认下载并入库，支持仅下载和稍后入库。** 当前发布进度见[验证结果](docs/validation.md)。支持本地进程的 MCP 客户端可使用：

[npm 安装入口](https://www.npmjs.com/package/cwa-book-mcp)：`npx -y cwa-book-mcp@latest`，或导入下方配置。

```json
{
  "mcpServers": {
    "cwa-books": {
      "command": "npx",
      "args": ["-y", "cwa-book-mcp@latest"],
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

启用 Z-Library 只需在 env 中增加 `ZLIBRARY_EMAIL` 和 `ZLIBRARY_PASSWORD`，或使用已有会话 `ZLIBRARY_USER_ID` / `ZLIBRARY_USER_KEY`。程序自动更新、探测和切换入口，自动接受认证响应给出的公网 HTTPS 下载地址，无需维护域名或 CDN 列表。

手机与网页客户端通过远端 MCPHub 使用此进程。书源原文件交付和自动入库沿用现有出站网络与 CWA 地址，无需新增端口、CF 隧道路由或 PUBLIC_BASE_URL。Agent 需要具备将 HTTP 响应保存成文件并读取的工具；仅返回链接不代表已保存。不同客户端的 JSON 格式详见[配置与接入](docs/configuration.md)。

使用：search_books 选定 Z 候选 → download_book 创建任务 → get_task 的 download.status 为 available → get_download_url → Agent 下载读取。默认复用 MCP 校验时的临时文件自动上传 CWA；仅下载传 save_to_cwa:false，缓存有效时可 collect。认证和常规文件校验均在 MCP 内部完成，入库进度单独报告。

六个工具：search_books、download_book、acquire_book、upload_book、get_task、get_download_url。acquire_book 保留书库获取/转换流程。CWA 独有文件或转换结果仍需客户端 CWA 认证或可达的显式签名网关；不会自动换成另一书源版本。upload_book 本地附件 PUT 也需可达的文件入口。文件不通过 MCP 消息传输。

开发：`npm ci` → `npm run check` → `npm run build`。从当前仓库启动使用 `node dist/cli.js`，验收打包使用 `npm run test:package`。

[当前方案与进度](PLAN.md) · [配置](docs/configuration.md) · [接口契约](docs/contracts.md) · [验证结果](docs/validation.md)
