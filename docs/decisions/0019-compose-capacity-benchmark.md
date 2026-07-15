# ADR 0019：容量结论使用有界Compose黑盒Benchmark

状态：已接受
日期：2026-07-15

## 决策

- Canonical容量验证使用Docker Compose的`app + PostgreSQL + Content Store`真实进程，不使用Mock Repository或内存数据库代替。
- 第一阶段固定三条核心路径：Canonical已认证Inbox读取、Canonical Inbox创建、LMTP耐久投递。
- 每次运行必须记录请求总数、并发、Raw MIME Payload、超时、Docker Engine的CPU/内存条件、吞吐、P50/P95/P99、结果码和失败数。
- Benchmark使用纯Go黑盒驱动，不引入常驻k6、Redis或额外Broker；结果写入机器可读JSON、Prometheus Snapshot和Docker Stats NDJSON。
- Benchmark Override只把HTTP与LMTP绑定到Host Loopback，不改变生产Compose的公网端口与安全边界。
- 每次LMTP投递加入唯一、定长、非秘密的Benchmark ID，防止Content-addressed Store去重掩盖写入与异步解析成本；吞吐阶段后必须等待持久化Parser Queue排空并记录最终状态。
- Reference Profile保留64个LMTP会话的有界Admission Ceiling，默认曲线压到32以保留连接关闭与Postfix突发余量；Parser Worker定为2，PostgreSQL Pool上限定为10，后续只有目标Linux机器上的曲线和故障证据支持时才调整。
- GitHub-hosted Runner和Docker Desktop结果只证明可重复性与回归趋势，不单独作为生产容量承诺。正式资源建议必须注明机器、平台、运行次数和保守余量。

## 暂不包含

- Nginx TLS与公网网络延迟；
- Postfix SMTP连接、Queue和重投吞吐；
- 多Replica、S3-compatible Storage与外部Collector；
- 依赖共享宿主机的绝对QPS SLA。

这些边界将在核心容量曲线稳定后分别增加，不能把核心LMTP或直连HTTP结果冒充完整公网端到端容量。
