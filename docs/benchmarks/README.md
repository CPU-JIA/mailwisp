# Compose容量Benchmark

Canonical入口是`scripts/benchmark-compose.ps1`。脚本使用临时Compose Project、临时Secret和临时Volume启动`app + PostgreSQL`，完成后只删除本次Project的容器与Volume，不连接生产环境。

默认场景：

| 场景 | 总操作 | 并发 | Payload |
| --- | ---: | --- | ---: |
| `http-inbox-read` | 10000 | 1/4/16/32 | Canonical Inbox JSON |
| `http-inbox-create` | 500 | 1/4/16/32 | Canonical创建请求 |
| `lmtp-delivery` | 500 | 1/4/16/32 | 8192-byte Raw MIME |

执行：

```powershell
./scripts/benchmark-compose.ps1
```

快速Smoke：

```powershell
./scripts/benchmark-compose.ps1 `
  -SkipBuild `
  -Concurrency 1,2 `
  -HTTPReadRequests 20 `
  -HTTPCreateRequests 10 `
  -LMTPRequests 10
```

默认Loopback端口是HTTP `18080`与LMTP `25250`。脚本会在启动前验证端口可绑定，并在容器启动后核对Docker的实际发布映射；如端口冲突或被操作系统保留，可通过`-HTTPPort`与`-LMTPPort`显式替换。

固定Digest的PostgreSQL镜像会在启动前执行最多5次有界指数退避拉取，以吸收Registry临时`429/5xx`与网络抖动；重试不改变Image Reference或Digest校验，持续失败仍终止Benchmark并保存诊断。

默认输出到被Git忽略的`artifacts/compose-benchmark/`：

- 每个场景/并发一份JSON；
- `environment.json`记录Git Commit/Dirty状态、Go/Compose/PowerShell版本、容器镜像ID、Docker OS、Architecture、CPU、Memory和端口；
- `docker-stats.ndjson`每秒记录App与PostgreSQL资源；
- `parser-drain.json`记录LMTP耐久接收结束后，持久化MIME Parser Queue排空耗时与最终状态；
- `metrics.prom`保存运行后的内部低基数Metrics。

输出目录必须位于仓库`artifacts/`的子目录内；重跑同一目录会覆盖已知结果文件，防止NDJSON或旧并发结果跨Run污染。带UTC时间戳的历史`diagnostics-*`目录会保留，便于比较失败现场。

启动或运行失败时，脚本会在清理隔离Stack前写入带UTC时间戳的`diagnostics-*`目录，其中包含失败位置、Compose状态、`postgres/migrate/app`日志与容器Inspect结果。

GitHub Actions在Linux共享Runner上执行缩短但同构的回归曲线（并发`1/4/16/32`，读取`1000`、创建`100`、LMTP`100`），无论成功或失败都上传14天Artifact。该Job检查可重复启动、零失败和Parser Queue排空，不设置易受共享宿主机噪声影响的绝对QPS阈值。

解读规则：

- 任一结果`failed > 0`都不形成容量结论；
- P99、错误率、CPU、Memory、PostgreSQL Pool和Parser积压必须一起判断，不能只看QPS；
- Docker Desktop与GitHub-hosted Runner只用于回归，不与专用Linux机器结果混用；
- LMTP场景绕过Postfix，HTTP场景绕过Nginx，结果名称和结论必须保留该边界；
- 每次LMTP投递都带唯一且定长的Benchmark ID，避免Content-addressed Store去重掩盖真实写入与Parser压力；LMTP吞吐只表示耐久接收，异步解析能力必须结合`parser-drain.json`判断；
- 资源建议至少保留30% CPU、30% Memory和足够磁盘/WAL余量。

## Reference Profile资源起点

以下是个人自托管的保守起点，不是SLA：

| Profile | CPU | Memory | SSD | 适用边界 |
| --- | ---: | ---: | ---: | --- |
| Low-traffic floor | 2 vCPU | 2 GiB | 20 GiB | 低频收件与少量并发；必须在目标Linux主机重跑Benchmark和恢复演练 |
| Recommended start | 4 vCPU | 4 GiB | 50 GiB | 为PostgreSQL、Postfix Queue、Content Store Page Cache、备份和突发流量保留余量 |

Reference默认使用`MAILWISP_LMTP_MAX_SESSIONS=64`、`MAILWISP_PARSER_WORKERS=2`和`MAILWISP_POSTGRES_MAX_CONNECTIONS=10`。64是Admission Ceiling，不是建议持续并发；默认曲线只压到32，为连接关闭与Postfix突发保留有界余量。核心曲线显示更高并发会明显增加尾延迟，而两名Parser Worker能够在8 KiB样本下追平耐久投递；复杂MIME、大附件、Postfix重投或更慢磁盘可能改变结论，因此不得仅凭该默认值扩大容量承诺。

磁盘至少按“保留期内Raw MIME峰值 + PostgreSQL/WAL + Postfix Queue + 一份本地Backup + `MAILWISP_CONTENT_MIN_FREE_BYTES`”计算。出现持续Parser积压、Postfix Queue增长、PostgreSQL Pool等待、P99恶化或磁盘水位接近阈值时，先在目标机器复测，再调整Worker、Pool或主机资源。
