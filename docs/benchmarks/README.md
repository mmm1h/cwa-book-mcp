# 资源报告

门槛唯一详细定义在 [内存预算](../../plan/02-stack-and-memory.md)。本目录只记录实际测量，不将目标冒充成绩。

## Linux 采样与 cgroup 判定

负载驱动、书源和 CWA HTTP 替身在容器外；发布构建的应用单独运行在受限容器内，文件暂存与验证产生的页缓存计入该容器。负载容器使用 runner 的非 root UID/GID；另由 Compose smoke 验证镜像默认 10001:10001 和具名卷权限。文件阶段每 100 ms、其他阶段约每秒采样 `/proc/<pid>/status` 的 VmRSS/VmHWM、FD，以及 cgroup v2 的 current/peak/max、anon/file 和 memory.events；各内核文件依次读取。程序每秒更新私有 Go heap/goroutine 指标。race 构建另行执行。

cgroup 验收核对配置的硬限制、完整负载完成情况和 OOM；原始 current/peak 不修整。[Linux 内核文档](https://docs.kernel.org/admin-guide/cgroup-v2.html#memory) 明确说明 memory.max 下 “Under certain circumstances, the usage may go over the limit temporarily.” 因此，瞬时 current 高于配置上限不单独判失败。memory.events.max 记录触及上限的回收压力；oom/oom_kill 或 Docker OOMKilled 则使验收失败。

旧 [626cce0 运行](https://github.com/mmm1h/cwa-book-mcp/actions/runs/38569192699) 的错误判定要求每个 current 样本都不越限，因此在 amd64 超出 8 KiB、arm64 超出 60 KiB 时误报。旧运行保留其失败结论；修正后重新执行完整验收，不改写旧报告，也不提高容器限制。

## Linux amd64/arm64 完整验收

2026-10-14，[完整运行](https://github.com/mmm1h/cwa-book-mcp/actions/runs/38578199982) 三个 job 均成功。应用与负载源码 `acebc9799564d7f5cd1b1b8d9105820462d22e22`，Go 1.27.2，依赖见 go.mod/go.sum；普通 `CGO_ENABLED=0`、`-trimpath -ldflags='-s -w -X main.version=<commit>'` 构建，GOMEMLIMIT=64MiB，容器 128 MiB 且无 swap。amd64 使用原生 ubuntu-24.04，arm64 使用原生 ubuntu-24.04-arm；两者均为 GitHub/Azure runner、4 个逻辑 CPU、Linux 6.17.0-1022-azure。

| 实测指标 | amd64 | arm64 |
| --- | --- | --- |
| 5 分钟空闲最大 RSS | 20.78 MiB | 17.04 MiB |
| 全负载进程峰值 max(RSS, HWM) | 24.77 MiB | 20.36 MiB |
| 10 MiB→1 GiB 文件窗口峰值 RSS 增量 | 1.87 MiB | 2.04 MiB |
| 最大采样 HeapAlloc / HeapSys | 3.59 / 11.13 MiB | 3.55 / 11.06 MiB |
| memory.max，所有有效采样 | 134,217,728 字节 | 134,217,728 字节 |
| memory.current 最大原值 | 134,475,776 字节 | 134,221,824 字节 |
| memory.peak 最大原值 | 134,332,416 字节 | 134,336,512 字节 |
| anon / file 分别的峰值 | 16.64 / 111.65 MiB | 10.23 / 117.13 MiB |
| memory.events.max / oom / oom_kill | 12669 / 0 / 0 | 11814 / 0 / 0 |
| Docker OOMKilled | false | false |
| 30 分钟稳定性实际秒数 | 1800.475 | 1800.394 |
| 稳定性 goroutine 起点→终点 | 16→9 | 16→9 |
| 稳定性 FD 起点→终点 | 14→10 | 14→10 |
| 稳定性状态库起点→终点 | 4 MiB→4 MiB | 4 MiB→4 MiB |
| 顺序完成任务 / 初始历史任务 | 100 / 1000 | 100 / 1000 |
| 被识别并有限重试的控制 BUSY | 502 | 519 |
| 样本数 | 2215 | 2254 |

两架构各有一个 current 样本临时超出限制，分别为 252 KiB、4 KiB；原始值完整保留。max 事件说明负载实际触及回收压力，OOM 事件为零；配置上限未改变。anon/file 的峰值并非同一时刻，不能相加当作总量。八项资源门槛均通过；arm64 报告中的 linux_amd64_environment=false 只是平台标识，不属于其资源失败。

| 合成文件 | amd64 文件窗口峰值 RSS / 秒 | arm64 文件窗口峰值 RSS / 秒 | 完整传输 SHA-256 |
| --- | --- | --- | --- |
| 10 MiB | 20.87 MiB / 2.291 | 17.25 MiB / 2.283 | cd9d9c2363d910750382669a6cfba1d564833656fdb642e8ba91c2f283432478 |
| 100 MiB | 21.09 MiB / 3.385 | 17.47 MiB / 3.491 | 68bde2618546fd394e1e4b457540812fce6b709c451ad347eddc88c9aa1438cc |
| 1 GiB | 22.74 MiB / 16.817 | 19.29 MiB / 19.454 | 4af6527398eb533c34e7a08e6d0c3cd6c494f9f622e55de0036974cde395dffd |

同一受限容器验证了停滞 PUT 取消、部分上传后强制崩溃、32 任务积压与第 33 个拒绝、两个文件许可占用时立即拒绝额外 GET、未知提交重启后只查库不重传，五项全部通过。稳定性首分钟连接回收后，FD 保持 10，goroutine 以 9 为基线，仅有短暂请求波动，没有持续增长；活跃定时器没有独立数量指标，其释放通过固定 worker ticker、取消回归和传输结束关闭验证。

原始报告：[amd64](linux-amd64-acebc97.json)、[arm64](linux-arm64-acebc97.json)。这是本服务的受控流式负载；真实 CWA 的上传/转换另见 [验证记录](../validation.md)，目标 NAS/MCPHub/Agent/书源账号和 Cloudflare 通路尚未验收。

```sh
go run ./tools/check-report docs/benchmarks/linux-amd64-acebc97.json
go run ./tools/check-report docs/benchmarks/linux-arm64-acebc97.json
```

## Windows 开发预检

Windows amd64 开发预检：Go 1.27.2、普通 `-trimpath -ldflags='-s -w'` 构建、
GOMEMLIMIT=64MiB、1000 条保留任务、服务与测试端分进程；5 秒空闲、5 秒短稳定性、两个顺序任务。Windows GetProcessMemoryInfo 采集 WorkingSet/PeakWorkingSet 和 handles，文件窗口 100 ms、其他阶段 1 秒。

| 文件 | 峰值 WorkingSet | 完整传输与 SHA-256 |
| --- | --- | --- |
| 10 MiB | 59,879,424 字节 | 通过 |
| 100 MiB | 61,874,176 字节 | 通过 |
| 1 GiB | 61,808,640 字节 | 通过 |

10 MiB→1 GiB 峰值差约 1.84 MiB。原始数据见 [JSON](windows-amd64-preview.json)。
这不是 Linux RSS/FD/cgroup，**不构成 Linux/容器、5 分钟空闲或 30 分钟稳定性门槛通过**。
