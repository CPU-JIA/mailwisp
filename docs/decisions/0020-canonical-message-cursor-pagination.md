# ADR 0020：Canonical消息列表使用不透明Keyset Cursor

状态：已接受
日期：2026-07-16

## 背景

Inbox默认允许500条Message，而Canonical消息列表原先只返回最新100条且没有后续页入口。已经成功接收的第101至500条邮件因此无法从Canonical API或正式Vue控制台发现，违反数据可达性与有界Pagination要求。

Offset适合需要兼容既有Contract的Adapter，但Canonical列表会与新邮件并发写入。继续使用Offset会在列表前端插入新消息时产生重复或跳项，而且越靠后的页扫描成本越高。

## 决策

- `GET /api/v1/inboxes/me/messages`保留原有顶层`data`数组，并新增`pagination.next_cursor`，旧客户端可以安全忽略新字段。
- Cursor是版本化Base64URL二进制值，只包含版本、`received_at`微秒值与Message UUID；客户端必须把它视为不透明字符串。
- Cursor不承担认证或授权。每一页仍先验证Capability或Browser Session，并把查询限定到当前Inbox。
- PostgreSQL按`(received_at DESC, id DESC)`稳定排序，下一页使用严格小于前页最后一项的Tuple边界；现有`messages_inbox_received_idx`直接覆盖该Query Pattern。
- Service读取`limit + 1`项判断是否存在下一页；公开`limit`继续限制在1至100。
- Cursor自包含排序边界，因此前一页最后一条Message在继续翻页前被删除时，后续页仍然有效。
- 并发到达且排序在Cursor之前的新邮件不会混入旧页；下一次刷新第一页时可见。排序在Cursor之后的历史补写可能出现在后续页，这是Live Listing而非Snapshot Transaction的明确语义。
- Vue控制台展开历史后继续轮询最新页，以Message ID更新并前置最新结果，同时保留已加载历史与旧页Cursor；浏览历史不能静默关闭实时收信。
- DuckMail、YYDS与Cloudflare Temp Email Adapter继续保持各自已声明的Offset/Hydra Contract，不被Canonical Cursor反向污染。

## 安全与失败语义

- Cursor只接受固定版本、固定字节长度、PostgreSQL安全时间范围与UUIDv7；非法值返回`400 invalid_pagination`。
- Cursor内容不是Secret，篡改不能越过Inbox Ownership，只能改变当前Inbox内部的遍历边界。
- Cursor不进入日志Label、Metric Label或持久状态。

## 验证

- Codec覆盖微秒精度Round-trip、版本、长度、Base64与UUID版本拒绝。
- HTTP测试覆盖原`data`数组兼容、`next_cursor`与非法Cursor错误Envelope。
- PostgreSQL Integration覆盖500条Message、重复`received_at`、多页无重复/无遗漏，以及第一页之后并发插入更新Message不会泄漏到旧Cursor页。
- Vue Client与Playwright覆盖Cursor Query、增量追加、跨页去重、末页按钮消失、历史展开后的实时合并、失败重试与Abort竞态。
