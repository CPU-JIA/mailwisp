# ADR 0009：采用Content级持久Parser Queue与Fenced Lease

- 状态：Accepted
- 日期：2026-07-15

## 背景

MailWisp在LMTP确认前已经把Raw MIME写入Content Store，并在PostgreSQL中创建一条或多条Message。纯Go有界MIME Parser也已经独立验证，但如果解析只靠进程内Channel，进程崩溃或重启会丢失任务；如果按Message解析，相同Raw MIME因多个Recipient或Postfix重投形成多条Message时，会重复消耗CPU、内存和数据库空间。

解析结果只由Raw MIME Bytes与Parser Revision决定，不属于某一条Recipient Message。因此队列、租约、状态和结果都必须以`mail_contents.content_key`为Canonical Key；Message继续保留独立投递语义，通过`content_key`共享一份解析结果。

## 决策

使用PostgreSQL作为持久Parser Queue，不引入Redis、RabbitMQ或独立Broker。Migration 000002把早期`messages.parse_status`占位字段提升到`mail_contents`，并新增一对一的`mail_content_parses`结果表。

Content解析状态为：

- `pending`：可在`parse_available_at`到达后领取；
- `processing`：由一个有效Lease Token持有；
- `parsed`：结果与状态已经在同一Transaction提交；
- `failed`：保存稳定、无敏感内容的错误码，Raw MIME仍保留。

领取使用`FOR UPDATE SKIP LOCKED`，每次领取生成新的随机UUID Lease Token、递增Attempt并设置`parse_lease_until`。完成、重试、失败和主动释放都必须同时匹配`content_key`与Lease Token；租约过期后新Worker会取得新Token，旧Worker不能覆盖新Owner的结果。

## 结果模型

`mail_content_parses`每个Content最多一行，保存：

- Parser Revision、Subject、Header Message-ID与Sent Date；
- From、To、Cc Address数组；
- 有界Text Preview与不可信`html_source`；
- Attachment Metadata与Recoverable Warning数组；
- UTC解析完成时间。

Subject、Text、HTML、Address、Attachment和Warning均由Parser边界限制，并在数据库使用第二层Size、Array Type与Array Count约束。Address、Attachment和Warning选择JSONB，是因为它们是有界、随Content整体读写的解析文档，不需要为每封邮件制造最多数百条关系行；Subject与时间等高价值查询字段仍使用独立列。任何未来需要独立Attachment生命周期或索引的能力必须通过新Migration演进，不能无界扩大JSON。

## Worker与资源边界

Reference Profile默认配置：

- Worker：2；
- 空闲Poll：1秒，并增加最多20% Jitter；
- Parse Timeout：30秒；
- Lease：1分钟，必须长于Parse Timeout；
- 最大Attempt：5；
- Retry Backoff：5秒指数增长，封顶5分钟；
- 进程内Wake Channel容量：1。

Wake只负责降低同进程新邮件的等待时间，允许合并和丢失；PostgreSQL才是持久事实源，启动扫描与保守Polling保证恢复。一个Worker会连续排空可领取任务，不为每条邮件创建Goroutine。`serve`拥有Worker生命周期、取消和Shutdown等待；Parser故障不撤销已经成功的LMTP持久化。

## 完整性与失败语义

Worker在同一次流式解析读取中计算实际Size与SHA-256。只有读取完成、Size匹配、Digest匹配且MIME Parser成功时，才允许提交`parsed`，不会为了校验再读取第二遍Raw MIME。

失败分类：

- MIME资源越界、结构错误和Content Digest不匹配是确定性失败，记录稳定错误码并进入`failed`；
- Content暂时无法打开、读取I/O错误和Parse Timeout按有界Backoff重试；
- 达到最大Attempt后进入`failed`；
- 进程优雅取消会主动释放Lease并回退Attempt，不把正常部署或Shutdown消耗为失败次数；
- 进程强杀依赖Lease到期恢复；
- 结果写入与`parsed`状态更新在同一PostgreSQL Transaction，任何Constraint、连接或Commit失败都会整体回滚。

数据库不保存原始Parser错误文本、Header或Body，只保存最长64字节的稳定小写错误码。日志成功事件为Debug，Retry和Terminal Failure使用稳定Code，不记录邮件正文、地址或Secret。

## Migration影响

- 已存在的`mail_contents`在升级后进入`pending`，由Worker补解析；
- 已存在Message、Content Reference和Raw MIME不重写、不删除；
- `messages.parse_status`被删除，查询方后续通过Message的`content_key`连接Content状态与结果；
- Migration必须使用真实v1数据完成Upgrade Test，不能只验证空数据库建表。

## 未采用方案

- 按Message解析：重复Recipient与Postfix重投会重复解析相同Bytes。
- 纯进程内Queue：崩溃和重启会丢任务，也不能支撑未来多Role进程。
- Redis List/Stream：Reference Profile增加服务与恢复语义，但当前吞吐没有证明收益。
- RabbitMQ、Kafka：超出个人服务器维护与当前故障模型所需。
- 无Token的时间租约：旧Worker可能在租约过期后覆盖新Worker结果。
- 先标记`parsed`再写结果：会产生状态可见但结果不存在的窗口。

## 验证要求

- 同Content多Recipient与重复投递只形成一个Queue Item和一份Parse Result；
- 并发Worker通过`SKIP LOCKED`领取不同Content，不重复领取；
- Lease过期后可重新领取，旧Token完成必须返回Stale Claim；
- Retry在`parse_available_at`前不可领取，Attempt与Terminal Failure正确；
- 结果Constraint失败时状态仍保持原Lease，不出现半提交；
- v1数据库带现有Message与Content升级到v2后数据不丢，旧Content进入`pending`；
- Worker从Content Store读取真实Raw MIME，完成Digest校验、MIME解析与PostgreSQL持久化；
- Unit、Race、固定PostgreSQL 18.4 Integration、全仓安全扫描与GitHub Actions完整门禁通过。

首次修复后完整远端门禁已由GitHub Actions Run [29366898352](https://github.com/CPU-JIA/mailwisp/actions/runs/29366898352)验证通过，覆盖固定PostgreSQL 18.4真实Migration、v1→v2带数据升级、并发领取、Lease过期与Fencing、Worker到Content Store和Parse Result的端到端闭环、普通与Race Integration，以及全仓Fuzz和安全扫描。该Run验证提交`a2d708d`；证据同步提交本身仍必须重新通过PR门禁。
