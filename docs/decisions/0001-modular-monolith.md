# ADR 0001：采用可分Role的Go模块化单体

状态：已接受
日期：2026-07-14

## 背景

MailWisp面向自托管场景，由一名维护者优先完成Reference Deployment Profile。系统存在HTTP API、LMTP Ingress、邮件解析、Webhook、Retention和Cleanup等清晰职责，但当前没有独立团队、独立发布节奏或必须分别扩容的生产证据。

过早拆分微服务会立即引入内部RPC、Service Discovery、跨服务Tracing、部署排序和分布式失败；把全部职责混入无边界大文件同样会形成巨型文件和隐式耦合。

## 决策

MailWisp使用一个Go代码库和一个应用二进制，以模块化单体组织领域与适配器。

Reference Profile使用组合Role运行：

```text
mailwisp serve
├── HTTP API
├── LMTP ingress
├── parser worker
├── webhook worker
└── retention jobs
```

内部模块通过Go调用协作，不引入内部HTTP或gRPC。每个后台Worker必须有明确Owner、Context取消、有界并发、Queue Capacity和Overload行为。

同一二进制保留显式Role入口：

```text
mailwisp api
mailwisp ingress
mailwisp worker
mailwisp migrate
```

只有以下证据出现时，Extended Profile才按Role拆进程：

- MIME解析CPU或内存显著影响HTTP/LMTP尾延迟。
- Webhook与后台任务需要独立资源或发布窗口。
- 多Replica容量模型证明收益高于运维成本。
- 故障隔离测试证明组合Role无法满足恢复目标。

拆Role不改变Domain Model，也不提前引入微服务协议。

## Reference Profile边界

- Postfix负责公网SMTP、持久队列和重试。
- MailWisp通过LMTP接收投递。
- PostgreSQL保存全部业务事实。
- Raw MIME和大附件使用Content Storage Port；Reference实现为本地文件。
- Redis与PgBouncer默认不部署。
- 不使用Kafka、RabbitMQ、Kubernetes、Service Mesh或内部RPC。

## 影响

- 单机部署、备份、恢复和排障保持在一名维护者可掌握范围。
- 进程内调用降低延迟，并允许一个业务Transaction内保持一致性。
- 模块边界必须由Package依赖、Interface和测试保证，不能依赖网络隔离。
- 组合进程崩溃会同时短暂影响API与LMTP，但Postfix队列可以在应用恢复后重投；仍需通过E2E验证该恢复路径。
- Extended Profile需要共享Content Storage和明确的Job单实例协调，但不必重写核心业务。

## 被拒绝方案

### 从第一天拆微服务

拒绝。当前没有独立扩缩容和团队边界证据，新增复杂度无法带来可验收收益。

### 单进程但不划分模块

拒绝。HTTP、LMTP、DNS、SQL与Lifecycle属于不同责任边界，混合后无法独立测试、审查和演进。

### Go直接承担公网SMTP

拒绝。会重新承担MTA队列、Retry、协议兼容和安全维护，收益不足。

## 重新评估条件

- Reference Profile无法满足已定义的可用性或容量目标。
- 真实Benchmark证明某Role必须独立扩缩容。
- 安全边界要求进程级隔离且无法通过Sandbox或权限控制实现。
