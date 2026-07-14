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

## 2026-07-14阶段性实现证据

已经完成：

- Raw Source流式写入Staging，限制总字节和单行字节。
- SHA-256 Content Key、文件`fsync`与同文件系统原子Hard Link安装。
- 32路并发同内容写入只形成一个Object。
- 写入取消、来源错误、超限、损坏Object与Staging Prune测试。
- PostgreSQL 18.4固定Digest Integration Test。
- Goose 3.27.2嵌入式Migration与并发Advisory Lock测试。
- 多Recipient Message在同一Transaction提交；任一Inbox失效则全部回滚。
- TCP LMTP到Content Store和PostgreSQL的真实端到端测试。
- 普通与Race Integration均通过。
- 固定Digest的Linux/amd64 Go 1.26.5环境中，全仓Test与Race通过，目录权限分支已真实执行。
- Gosec v2.27.1零Issue，未使用 `#nosec`；govulncheck与Gitleaks通过。

仍未完成，因此ADR保持“提议中”：

- Linux进程在Write、Fsync、Link和DB Commit各阶段被强制终止后的恢复测试。
- 真实Postfix队列重投与应用重启验证。
- 数据库与Content Store一致性备份、恢复和Orphan/Missing扫描。
- Linux生产文件系统上的容量、目录数量与尾延迟Benchmark。
