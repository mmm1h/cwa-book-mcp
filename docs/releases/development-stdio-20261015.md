# MCPHub stdio 开发构建

2026-10-15，Go 1.27.2。版本 `dev-stdio-20261015`；源码为与本记录一起提交的 stdio 修正。默认 MCPHub 示例通过 command/args/env 启动二进制，详见 [运行文档](../operations.md#mcphub-直接启动推荐)。

```sh
go run ./tools/package -version dev-stdio-20261015 -out dist
go build -trimpath -buildvcs=false -ldflags='-s -w -X main.version=dev-stdio-20261015' -o dist/cwa-book-mcp-windows-amd64.exe ./cmd/cwa-book-mcp
```

第二条是在 Windows amd64 上生成本机二进制；跨平台构建需指定对应 GOOS/GOARCH。Linux 归档由打包工具设置 CGO_ENABLED=0，含主程序、MCPHub stdio/HTTP 示例、运行文档和完整许可证，不需要额外 Go 运行时或本项目容器。产物位于被忽略的 dist，不上传或发布到外部服务。

| 文件 | SHA-256 |
| --- | --- |
| cwa-book-mcp-linux-amd64 | `0ae268ce7ae2d7933e49b604c59cc340d1e806a2c2ae4aa212d650ecf28f28e6` |
| cwa-book-mcp-linux-arm64 | `520a112594a9815d00a320a4477b83cc5f1d9b1d7f7bd10d982fe9ee45290c9c` |
| cwa-book-mcp-dev-stdio-20261015-linux-amd64.tar.gz | `013465ac143a528db1fbb92cff43b99b80b05c8b10c541a484ff24f080aeaf99` |
| cwa-book-mcp-dev-stdio-20261015-linux-arm64.tar.gz | `2586317bf276e57f4700a08a7516a30753aa04ad3d2969859e479b63f1b2234e` |

校验清单为 `dist/checksums-dev-stdio-20261015.json`。主程序归档权限 0755，stdio 显式 args 为 `--transport=stdio`；Windows 本机 `--version` 与 `--help` 已验证。完整测试、协议、恢复与证据边界见 [验证记录](../validation.md#mcphub-stdio-接入修正)。Linux 两架构本次完成构建，新增 stdio 的完整资源负载和目标 MCPHub 连接尚未实测；历史 `acebc97` HTTP 资源报告不替代它们。
