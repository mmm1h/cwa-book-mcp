# 资源报告

门槛唯一详细定义在 [内存预算](../../plan/02-stack-and-memory.md)。本目录只记录实际测量，不将目标冒充成绩。

Windows amd64 开发预检：Go 1.27.2、普通 `-trimpath -ldflags='-s -w'` 构建、
GOMEMLIMIT=64MiB、1000 条保留任务、服务与测试端分进程；5 秒空闲、5 秒短稳定性、两个顺序任务。Windows GetProcessMemoryInfo 采集 WorkingSet/PeakWorkingSet 和 handles，文件窗口 100 ms、其他阶段 1 秒。

| 文件 | 峰值 WorkingSet | 完整传输与 SHA-256 |
| --- | --- | --- |
| 10 MiB | 59,879,424 字节 | 通过 |
| 100 MiB | 61,874,176 字节 | 通过 |
| 1 GiB | 61,808,640 字节 | 通过 |

10 MiB→1 GiB 峰值差约 1.84 MiB。原始数据见 [JSON](windows-amd64-preview.json)。
这不是 Linux RSS/FD/cgroup，**不构成 Linux/容器、5 分钟空闲或 30 分钟稳定性门槛通过**。

隔离 Linux amd64/arm64 acceptance 使用非 root、128 MiB 容器，分别运行完整
5 分钟空闲、三档混合传输、100 顺序任务和 30 分钟稳定性。原始报告和运行 URL 待执行结束后补入。
