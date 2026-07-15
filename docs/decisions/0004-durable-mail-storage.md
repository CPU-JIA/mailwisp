# ADR 0004：可靠LMTP接收与Content Storage

状态：提议中，等待断电与恢复Technical Spike
日期：2026-07-14

## 背景

LMTP只有在邮件已经可靠持久化后才能返回成功。将无上限Raw MIME全部写入PostgreSQL大BLOB会放大WAL、备份、Vacuum和Cache成本；先解析再持久化又会让恶意或复杂MIME阻塞投递确认。

## 提议

Reference Profile采用PostgreSQL元数据加本地内容寻址文件存储：

1. LMTP将Raw Source流式写入有大小上限的Staging文件。
2. 计算Content Hash，`fsync`文件并在同一文件系统原子Rename到Content Path。
3. PostgreSQL Transaction创建Message、Recipient与Content Reference。
4. Transaction提交后才向Postfix返回成功。
5. Parser Worker异步生成Header、Text、Sanitized HTML与Attachment Metadata。
6. Webhook与通知在持久化后通过Transactional Outbox触发。

数据库失败可能留下Orphan File，由有界Scanner清理；文件持久化失败时不得写入Message或确认LMTP成功。删除操作先在数据库建立可恢复的删除状态，再由Worker删除Content，最终完成Tombstone或物理清理。

Content Storage定义Port：

- Reference实现：本地文件系统。
- Extended实现：S3-compatible Object Storage。

Content Hash用于物理去重，但每次SMTP投递仍创建独立Message Record，不能因原文相同而吞掉重复投递语义。

## 必须通过的Spike

- 进程在Write、Fsync、Rename、DB Commit各阶段终止后的恢复Invariant。
- 同Hash并发写入与重复投递。
- Orphan与Missing Content扫描。
- 备份时数据库与Content Snapshot一致性。
- Raw MIME大小、文件数量和目录分层Benchmark。
- Parser失败、超时和恶意嵌套不影响已持久化原文。
- LMTP临时失败触发Postfix重试且不会产生不可控重复。

## 暂不采用

- PostgreSQL无上限Raw MIME大BLOB。
- Reference Profile强制部署MinIO。
- 在Durable Persistence前执行Webhook、Telegram、AI或验证码提取。
- 解析完成前才保存Raw Source。

## 接受条件

上述Spike、备份恢复与LMTP E2E全部通过后，将状态改为“已接受”；任一核心Invariant无法证明时重新选择存储协议。
