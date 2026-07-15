# ADR 0015：Inbox投递容量采用双阶段有界配额

状态：已接受
日期：2026-07-15

## 背景

临时邮箱不能依赖TTL清理作为唯一容量保护。攻击者或异常上游可以在Inbox到期前持续投递，使单个Inbox占用过多Message Row与Raw MIME逻辑容量。只在LMTP RCPT阶段读取当前用量会被并发投递穿透；只在DATA后检查又会浪费带宽与Content Store写入。

## 决策

- 每个Inbox默认最多保存500条Message与256 MiB逻辑存储；部署者可通过`MAILWISP_INBOX_MAX_MESSAGES`和`MAILWISP_INBOX_MAX_STORAGE_BYTES`调整。
- 逻辑存储按该Inbox每条Message引用的Raw MIME大小累计。同一Raw Content被多个Inbox或多条Message引用时，各自计入逻辑配额；这不是底层文件系统的物理去重大小。
- 客户端在SMTP `MAIL FROM`声明`SIZE`时，RCPT解析会读取当前用量并尽早返回`552 5.2.2`。未声明`SIZE`时仍检查Message数量和已经满额的Storage，但不能预测本次Raw MIME大小。
- DATA完成后的Commit事务才是权威边界。事务按UUID稳定排序锁定全部Recipient Inbox，再读取用量并复核；同一Inbox的并发投递被串行化，不同Inbox仍可并行。
- 多Recipient Delivery保持原子语义：任意一个Inbox失效或超额，整笔Delivery不创建任何Message Row或Content Metadata。
- Message数量和Storage超限均使用永久失败`552 5.2.2`，避免Postfix对不会自行恢复的同一收件人持续重投。
- Metrics只暴露`messages`与`storage_bytes`两个固定Reason，不包含地址、Inbox ID或其他高基数标签。

## 影响

- RCPT预检减少明显满额投递进入DATA的带宽与磁盘消耗，但它不是事务锁，也不承诺独立正确性。
- 极端并发或未声明`SIZE`时，Raw MIME可能已写入Content Store后才在Commit被拒绝；Content Reconciliation负责清理这类没有数据库引用的Orphan。系统不虚假宣称完全消除Orphan。
- 本配额保护单个Inbox，不等同于主机级磁盘水位保护。全局可用空间、Content Store总容量与紧急拒收策略需要独立实现和验收。
- 配额是全局部署配置，不为匿名用户提供任意放大容量的接口，保持个人服务器的配置与资源模型简单。
