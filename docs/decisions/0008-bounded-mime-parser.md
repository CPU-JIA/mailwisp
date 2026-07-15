# ADR 0008：采用纯Go有界流式MIME解析器

- 状态：Accepted
- 日期：2026-07-15

## 背景

MailWisp在LMTP确认前只持久化Raw MIME与Message Metadata，复杂邮件解析必须异步执行。邮件内容完全不可信，可能包含超大Header、深层Multipart、海量Part、畸形Transfer Encoding、未知Charset、路径型Filename和追踪HTML。Parser既不能阻塞可靠收件，也不能在个人服务器上无界占用内存。

常见高层MIME实现会把完整Raw Message或Decoded Part物化到内存，并缺少MailWisp要求的Header Count、Header Bytes、Part Count、Depth、Total Decoded Bytes和Attachment Size联合不变量，因此需要独立设计有界流式边界。

## 决策

采用`github.com/emersion/go-message v0.18.2`作为MIME、Transfer Encoding和Charset的流式协议原语，固定Commit为`6a718fa6214f9f35d3398c82b3602ca1f32cf274`。MailWisp在`internal/mail`中负责领域模型、资源限制、错误分类、预览截断、Filename规范化和安全命名。

选择该库的原因：

- Leaf Body通过`io.Reader`流式消费，不要求把完整Part Tree Content全部物化到内存；
- 支持Multipart、Base64、Quoted-printable和常见Charset；
- 顶层Header提供显式`MaxHeaderBytes`；
- API足够低层，MailWisp可以掌控Part、Depth、Decoded Bytes和输出集合上限；
- 依赖面小于高层Envelope Parser，符合Reference Profile的资源目标。

库的每个嵌套Part Header在返回Entity前已被解析，因此嵌套Header上限属于“Raw Message全局上限内的解析后强制拒绝”，不是读取前硬截断。该风险被25 MiB Raw上限封顶；若未来Corpus或内存测量证明仍不足，必须替换或贡献可配置的Per-part Header Reader，不能删除上限断言。

## 默认不变量

Reference Profile默认限制：

- Raw MIME：25 MiB；
- 单个Header Block：64 KiB；
- 全部Header：1 MiB、1000 Fields；
- MIME Entities：100；
- Multipart Depth：10；
- 全部Decoded Leaf Content：32 MiB；
- 单个Decoded Leaf：25 MiB；
- Text Preview：512 KiB；
- HTML Source：1 MiB；
- Subject：998 Bytes；
- Filename：255 Bytes；
- 每类Address Header：100个Address；
- Recoverable Warning：100条。

所有Limits必须显式为正数，零值不会解释为Unlimited。Parser实例只保存不可变Limits，可以安全并发复用；每次Parse的计数与结果保持调用内局部状态。

## 输出与安全边界

- Raw MIME仍是唯一完整、可恢复的原始事实；解析失败不会删除Raw Source。
- Text与HTML只保存有界预览；超限产生稳定Warning，不会继续扩大内存。
- HTML类型命名为`UnsafeHTML`/`HTMLSource`，明确表示尚未Sanitize，不允许直接进入可信DOM。
- Attachment当前只生成Part Path、规范化Filename、Content Type、Disposition、Content ID与Decoded Size Metadata；Bytes仍从Raw MIME恢复。
- Filename会移除Control Character、替换路径分隔符、清理危险点路径并保持UTF-8边界。
- 未知Charset或Transfer Encoding是可恢复Warning；结构损坏和资源越界是Parse Failure。
- Warning不保存Raw Body，Detail使用固定文案并有长度上限，避免形成第二份内容泄漏面。

## 验证

Unit、Race和Fuzz Seed覆盖：

- Encoded Header、Address、Date、Quoted-printable和ISO-8859-1；
- Nested Multipart Alternative、HTML与Base64 Attachment；
- Raw、Header、Header Count、Part、Depth、Part Bytes和Total Decoded Bytes越界；
- Text/HTML有界截断；
- 未知Charset恢复、Context取消、并发复用与Filename路径规范化；
- 任意Fuzz Input不得Panic，所有返回集合不得突破Limit。

GitHub Actions固定执行10秒随机Fuzz。首次本地Windows Benchmark样本约48 KiB，包含4 KiB Text与32 KiB Base64 Attachment；Intel i7-14650HX、Go 1.26.5条件下三轮结果为272.94–315.41 MiB/s、约615.7 KiB/op、122 allocs/op。该数据只作为回归基线，不代表生产容量结论。

首次完整远端门禁已由GitHub Actions Run [29363817390](https://github.com/CPU-JIA/mailwisp/actions/runs/29363817390)验证通过，包含固定Linux全量门禁、Windows原生Unit/Race/Vet与GitGuardian。该Run验证的是提交`ff60d8c`；证据同步提交本身仍必须重新通过PR门禁。

## 暂不包含

- Sanitized HTML Policy与Sandboxed iframe；
- Attachment独立Content Object与下载API；
- Parsed Message查询API与重新解析管理命令；
- 真实邮件Corpus兼容矩阵和Linux峰值RSS测量。

这些能力必须在后续Change中复用本Parser边界，不得重新引入第二套无界MIME实现。

Parser Worker、Content级领取、重试和结果持久化已在后续开发分支实现并通过完整远端门禁，决策、故障语义和证据见ADR 0009。

## 未采用方案

- enmime v2.4.1：功能完整且维护活跃，但默认把每个Decoded Part保存为`[]byte`并构建完整Part Tree，不符合当前低内存目标。
- 只用`net/mail`和`mime/multipart`：依赖最少，但需要自行维护Charset、错误恢复和大量真实世界MIME兼容细节，收益不足。
- Rust或WASM Parser：项目统一Go，跨语言构建与运行成本没有被当前证据证明必要。
