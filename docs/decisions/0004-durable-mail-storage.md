# ADR 0004：可靠LMTP接收与Content Storage

状态：提议中，等待断电与恢复Technical Spike
日期：2026-07-14

## 背景

LMTP只有在邮件已经可靠持久化后才能返回成功。将无上限Raw MIME全部写入PostgreSQL大BLOB会放大WAL、备份、Vacuum和Cache成本；先解析再持久化又会让恶意或复杂MIME阻塞投递确认。

## 提议

Reference Profile采用PostgreSQL元数据加本地内容寻址文件存储：

1. LMTP将Raw Source流式写入有大小上限的Staging文件。
2. 计算Content Hash，`fsync`文件并在同一文件系统原子Hard Link到Content Path，再删除Staging Link。
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
- 有界Content Reconciliation扫描已实现：数据库引用使用Keyset Pagination，文件对象按固定批次比对，不把全量索引加载到内存。
- `serve`持有独立PostgreSQL Session的共享维护锁；`reconcile`必须取得独占锁，避免“文件已落盘、数据库尚未提交”的重复投递窗口被误删。
- Orphan支持显式`--repair-orphans`修复；Missing与Corrupt只报告并以非零状态退出，不自动删除业务记录。
- 普通与Race Integration已覆盖Content Catalog分页、维护锁互斥和Orphan修复。
- 真实子进程在Raw写入中、文件`fsync`后与Object Hard Link后被强制终止；重启后分别证明Staging可裁剪、未产生半Object，或Object可作为Orphan安全修复。
- 固定Linux/amd64环境中，Content Store强杀恢复普通测试10轮与Race测试3轮通过。
- PostgreSQL Transaction在Commit前被强制终止后自动回滚，数据库保持0 Content/0 Message，已落盘Object由Reconciliation清理。
- PostgreSQL Commit成功后、外部确认前被强制终止时，数据库与Object保持一致；模拟重投后复用1份Content并形成2条Message，保留重复投递语义。
- 严格三文件Backup Bundle与空目标Restore已经通过PostgreSQL 18.4普通及Race Integration；恢复前验证Manifest、大小与SHA-256，恢复后执行数据库/Content一致性检查。
- 固定Postfix 3.11.5真实验证LMTP不可达排队、Queue Volume跨重启、4xx重投、确认丢失重复Message和未知Recipient永久失败。
- 纯Go有界流式MIME Parser已经实现Raw、Header、Part、Depth、Decoded Bytes、Text/HTML Preview和Attachment Metadata边界；Parser Worker持久化尚未接入。

仍未完成，因此ADR保持“提议中”：

- 真实Linux生产文件系统或VM在断电/硬重启下的目录与文件持久性演练；进程强杀不能替代掉电证明。
- Linux生产文件系统上的容量、目录数量与尾延迟Benchmark。
- Parser Worker领取、重试、结果持久化与恶意邮件Corpus峰值RSS验证。
