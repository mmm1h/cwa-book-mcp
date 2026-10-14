# 开发构建 acebc97

应用源码：`acebc9799564d7f5cd1b1b8d9105820462d22e22`，Go 1.27.2。已生成 Linux amd64/arm64 二进制与 tar.gz；构建脚本为 tools/package，产物在被忽略的 dist 中。此记录不表示已经部署现有 NAS 或发布远程镜像。

```sh
go run ./tools/package -version acebc97 -out dist
```

二进制采用 `CGO_ENABLED=0`、`-trimpath -buildvcs=false -ldflags='-s -w -X main.version=acebc97'`。归档包含可执行主程序、项目 MIT 许可证和所有实际运行依赖许可证；时间固定，不包含配置、会话、书籍或任务数据库。

| 产物 | 字节数 | SHA-256 |
| --- | --- | --- |
| cwa-book-mcp-acebc97-linux-amd64.tar.gz | 4,568,251 | 6bb8980fda2f5979b8626df49e6764cb7c3455311e84db38ba22b732d6d51c34 |
| cwa-book-mcp-acebc97-linux-arm64.tar.gz | 4,126,280 | 26f564acd2554089061ca62499606770772d3ca57b99803f3592a2e835abb150 |
| cwa-book-mcp-linux-amd64 | 10,920,096 | 6f829bbdd9d170d684013989c4c7ebdba4923562a432c3b78651e9d71528cc4b |
| cwa-book-mcp-linux-arm64 | 10,092,704 | 7dffc5bbe8d1affb09f3ceaadd854f0b8a2868702d20d145c34fbc675866f388 |

在两个独立输出目录重复构建，四个文件的 SHA-256 完全一致。二进制 ELF machine 分别为 62（AMD64）和 183（AArch64）；归档主程序为 0755。机器可读清单见 [checksums](checksums-acebc97.json)。

实际验证与版本见 [验证记录](../validation.md)，内存门槛与实测数字见 [资源报告](../benchmarks/README.md)。首次接入先按 [运行文档](../operations.md) 配置私有秘密文件、格式规则、MCPHub 与文件通路，记录实际环境验收结果。
