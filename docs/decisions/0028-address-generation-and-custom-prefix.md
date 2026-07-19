# ADR 0028：自定义地址前缀采用拒绝式校验、编译期保留字与墓碑冷却

状态：提议中（草案，未接受）
日期：2026-07-19

## 背景

v0.2 需要在 Canonical API 上开放自定义 Local Part。服务层能力已经存在：`internal/message/address.go` 的 `ValidInboxLocalPart` 限定 `[a-z0-9._-]`、长度 1..64、首尾禁分隔符、禁 `..`；LMTP 入口对整个 RCPT 地址小写化后精确匹配活跃 Inbox；`mailbox.Service.Create` 已支持指定 localPart 单次尝试。但 `POST /api/v1/inboxes` 尚未暴露 localPart，也没有针对自定义地址的滥用防护。ADR 0015/0016/0017 已经建立投递配额、磁盘水位与创建日配额底座，本决策在其上补齐地址命名面。

竞品调研（docs/product/feature-dossiers.md 第 4 节）显示三类真实风险必须与开放同时解决：

- 角色地址抢注：CA/B Baseline Requirements §3.2.2.4.4 允许 CA 向 `admin@` / `administrator@` / `webmaster@` / `hostmaster@` / `postmaster@` 发送验证邮件为该域签发 TLS 证书，公共域上放任注册等于交出证书签发权；`autoconfig` / `autodiscover` / `wpad` 被占用会劫持邮件客户端与代理的自动发现信任链。
- 删除后复用：临时地址曾用于注册第三方站点，过期或删除后被他人重建即可接收密码重置邮件完成账号接管。SimpleLogin 与 addy.io 均以墓碑（DeletedAlias / null-UUID）阻断。
- 冲突枚举：自定义创建天然暴露「该地址存在过」的 409 oracle，探测成本必须由配额约束。

## 决策

采纳档案第 4 节推荐规格。执行顺序是契约的一部分：`trim + 小写化 → ValidInboxLocalPart（含后缀预留收窄）→ 保留字 → ADR 0017 配额消费 → 墓碑检查与唯一索引冲突`。静态拒绝发生在配额消费之前，冲突消耗配额不退款；顺序颠倒会产生免费枚举通道或配额白耗。

### API 契约

`POST /api/v1/inboxes` 新增两个可选字段：

- `localPart`（string）：期望的地址前缀；省略或为空时完全走既有随机路径，零行为变化。
- `localPartSuffix`（`"none" | "random"`，默认 `"none"`）：仅在提供 `localPart` 时有意义；未提供 `localPart` 时必须省略或为 `"none"`。

### 校验契约（拒绝式，不清洗）

- 服务端只做两种规范化：trim 与小写化（与 LMTP 入口小写化对偶，`"Alice"` 规范化为 `"alice"` 而非拒绝）。除此之外一律拒绝，不删除、不替换字符；清洗式入口只存在于 Cloudflare Temp Email Adapter 的投影层（ADR 0002 边界）。
- 规范化后必须通过既有 `ValidInboxLocalPart`，不新写规则：等价于正则 `^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$` 且禁 `..`；上限 64 为 RFC 5321 §4.5.3.1.1 硬上限，宽于 SimpleLogin（40）、addy.io（50）与 Cloudflare Temp Email（30）但完全合法。
- `localPartSuffix="random"` 时为 `"-" + 4` 字符预留 5 字节，用户段上限收为 59。
- 相邻混合分隔符（如 `a.-b`）现行校验放行，保持现状不收紧，避免破坏 YYDS/CF 既有客户端；单字符与纯数字均合法。

### 保留字（编译期内置）

匹配算法：`blocked(local) = exact(local) OR any(token ∈ split(local, [._-])) ∈ ReservedSet`，作用于用户提供的 localPart（随机后缀不检查）。`admin`、`admin.zhang`、`billing-paypal` 拒绝；`admin2024`、`supportive` 放行。命中返回 `422 local_part_reserved`，发生在配额消费之前，不消耗配额。RFC 2142 要求角色名大小写不敏感识别，创建侧小写化已保证。

集合为编译期 Go 内置（进程内存精确匹配，无 DB 查询），随二进制版本固定。默认清单固定为以下 106 词（分组仅为审阅便利，运行时是单一集合）：

- RFC 2142 全集（15）：info marketing sales support abuse noc security postmaster hostmaster usenet news webmaster www uucp ftp
- CA/B §3.2.2.4.4 硬保留（5，独立硬清单，任何开关不可关闭）：admin administrator webmaster hostmaster postmaster
- MTA 与退信惯例（11）：mailer-daemon bounce bounces mail smtp imap pop pop3 mx webmail email
- 自动发现与基础设施（8）：autoconfig autodiscover wpad localhost dns ns ssl tls
- 管理惯例（18）：root sysadmin system staff office contact help helpdesk service services team hello hi me all everyone owner moderator
- 业务角色（20）：billing invoice invoices payment payments pay finance accounting legal privacy dpo gdpr compliance hr jobs careers press media partners feedback
- 自动邮件（12）：noreply no-reply no_reply donotreply do-not-reply notification notifications alert alerts newsletter unsubscribe reply
- 技术与占位（18）：api dev developer test demo example user username guest anonymous unknown undefined null nil none true false void
- 品牌（2）：mailwisp wisp

部署者调整（配置在启动时类型化加载与校验，非法值拒绝启动）：

- `MAILWISP_LOCAL_PART_RESERVED_EXTRA`：逗号分隔追加词条，词条必须为小写且落在地址字符集内；
- `MAILWISP_LOCAL_PART_RESERVED_ENFORCED`：默认 `true`；`false` 关闭一般清单（私有实例），CA/B 五词硬清单不受该开关影响。

保留字只约束创建时点：清单随版本演进只影响其后的创建，不回收已存在 Inbox，也不影响投递。

### 地址墓碑与冷却

新增单调 Migration（版本号以实际合入顺序为准）两项：

1. `inboxes` 增加 `custom_local_part boolean NOT NULL DEFAULT false`；创建时提供了 localPart（含带随机后缀）的 Inbox 置 `true`。存量地址全部是 20 字符随机串，默认值语义正确，无需回填。
2. `CREATE TABLE address_tombstones (address text PRIMARY KEY, released_at timestamptz NOT NULL)`。

语义：

- 仅当被删除的 Inbox `custom_local_part = true` 时写墓碑；所有删除路径一致——DELETE API、TTL 过期清理与任何其他删除路径都必须在删除 Inbox 的同一事务内写入完整地址与释放时间。随机 20 字符地址不写（96 bit 空间无复用风险，避免表无界增长）。
- 创建自定义地址时在同一事务内检查 `released_at > now() - 冷却期`，命中返回与活跃冲突完全相同的 `409 address_conflict`，不提供区分活跃与墓碑的 oracle；唯一索引兜底活跃冲突。
- 冷却期 `MAILWISP_ADDRESS_TOMBSTONE_DAYS` 默认 90，范围 0..3650；`0` 为禁用——创建不检查、删除不写入，存量墓碑行视为过期并由有界清理移除。
- 清理复刻 ADR 0017 已生产验证的模式：每次成功的自定义地址创建在同一事务内最多删除 100 条已过冷却期的墓碑；无后台任务、无新常驻组件。墓碑只由自定义流量产生，也只由自定义流量清理，随机路径保持零新增成本。
- 匿名产品没有账号身份：冷却期内原持有者同样无法重建同名地址，文档必须如实声明。
- LMTP 不感知墓碑表：RCPT 到墓碑地址走既有 `550 5.1.1`（Inbox 不存在）。
- `address_tombstones` 是地址生命周期的唯一墓碑底座；v0.4 别名与地址数据模型（档案第 9 节）在其上扩展，不另建第二套复用防护。

### 可选随机后缀

- `localPartSuffix="random"` 时最终地址为 `localPart + "-" + 4` 字符，取样自既有随机路径同一 RFC 4648 base32 小写字母表（a-z、2-7），使用 `crypto/rand`，4×5 = 20 bit（约 104 万组合）。
- 冲突时仅重新生成后缀重试，复用既有 `maxAddressGenerationAttempts = 5`；5 次仍冲突返回 `409 address_conflict`。
- 保留字检查作用于用户段，后缀不参与。
- `MAILWISP_LOCAL_PART_SUFFIX_REQUIRED` 默认 `false`；`true` 时所有自定义创建必须显式 `localPartSuffix="random"`，否则 `422 invalid_local_part`（拒绝式，不静默改写）。语义对齐 SimpleLogin 共享域强制后缀的防抢注理由，公共实例建议开启。

### 随机路径保持不变

省略 localPart 的路径维持现行算法：`crypto/rand` 读 12 字节 → RFC 4648 base32 无填充 → 小写 → 20 字符，96 bit 熵，冲突重试 5 次；熵与随机源均优于全部竞品（Cloudflare Temp Email 使用非密码学 `Math.random`，addy.io 约 41 bit，DuckMail 约 58 bit）。该路径的行为、错误映射（含重试耗尽后既有的 `503 address_unavailable`）与性能零变化，不写墓碑、不触发墓碑清理。

### 错误契约与可观测性

新增错误全部使用既有统一 Error Envelope（`error.code` / `error.message` / `error.request_id`），稳定 code 如下：

- `422 invalid_local_part`：字符、长度或结构非法；后缀模式下用户段超过 59（message 说明预留 5 字节）；`localPartSuffix` 取值非法、与 `localPart` 缺省矛盾或未满足强制后缀配置。
- `422 local_part_reserved`：保留字命中；不消耗配额。
- `409 address_conflict`：活跃冲突与墓碑冷却命中统一同一响应体；消耗配额且不退款（ADR 0017 语义延续）。
- `429 daily_quota_exceeded`：沿用 ADR 0017 及 `RateLimit-Limit` / `RateLimit-Remaining` / `RateLimit-Reset` / `Retry-After` 头。

Metrics 新增有界计数器 `mailwisp_inbox_create_rejections_total{reason}`，`reason` 固定 4 值：`invalid_local_part` | `reserved` | `conflict` | `tombstone`（符合 ADR 0014 低基数约束；`conflict` 与 `tombstone` 仅内部区分，对外响应无差别）。被拒 localPart 原文不写日志，防止运维日志成为他人尝试记录的枚举面；日志与 Metric Label 均不携带地址。

### Adapter 与入口一致性

- Cloudflare Temp Email Adapter：`normalizeAddressName` 保持清洗式（小写并仅保留 `[a-z0-9]`）再进 Canonical 创建；保留字与墓碑拒绝映射回上游纯文本错误（上游本有 blocklist 错误路径，语义对齐）；上游 Custom Regex、PREFIX、MIN/MAX_ADDRESS_LEN 配置面继续列为 Unsupported。
- DuckMail Adapter：`POST /accounts` 精确地址路径开始受保留字与墓碑约束，错误保持 `{error,message}` Envelope 的 422/409。
- YYDS Adapter：已通过 `ValidInboxLocalPart` 校验 localPart，行为自动收敛，无契约变化。
- 三入口继续共享 ADR 0017 同一日配额，Adapter 不建立独立计数。

## 影响

- 自定义创建每请求新增一次进程内静态检查、一次墓碑 SELECT 与一次有界墓碑 DELETE；均不在随机路径与消息读写 Hot Path 上。
- 409 泄露「该地址存在过」是自定义 UX 的必然代价，探测成本由 ADR 0017 每身份每日 100 次与冲突不退款约束；无差别响应不泄露地址处于活跃还是冷却状态。
- 防复用以「事务内冷却检查 + 唯一索引兜底」实现，不承诺可序列化隔离级别的复用免疫；与并发删除之间的极窄竞态窗口在日配额约束下不构成实用攻击面，如实披露而非虚假承诺。
- 保留字 token 规则有已知误伤（`admin.zhang` 类真实姓名前缀被拒）与已知放行（`admin2024`、`supp0rt` 类 ASCII 视觉近似），均如实记录：前者可由私有实例关闭一般清单缓解（硬五词除外），后者是接受的残余钓鱼面。
- Compatibility 与运维文档随本变更同步：CF 上游配置面 Unsupported 说明、DuckMail 补充「上游（mail.tm 语义）未定义字符集，MailWisp 按 Canonical 规则收紧」的显式声明、新增 4 个 `MAILWISP_` 配置项及冷却语义（含原持有者冷却期内亦无法重建）。

## 暂不采用与明确不做

- `+tag` 子地址（RFC 5233）：创建字符集不含 `+`，无抢注面；LMTP 保持小写化精确匹配。接收侧归一化留待 v0.3 单独设计，届时 `MAILWISP_SUBADDRESS_DELIMITER` 默认空（与 Postfix `recipient_delimiter` 默认一致，安全默认拒绝），tag 邮件计入 base Inbox 的 ADR 0015 投递配额。
- SMTPUTF8 / EAI（RFC 6531）Unicode local part：不引入。纯 ASCII 字符集叠加拒绝 SMTPUTF8，对 Unicode 同形攻击（UTS #39）结构性免疫。
- Quoted-string、RFC 5321 全 atext 特殊字符与大小写敏感邮箱：RFC 5321 §4.5.3.1.1 本身劝阻，不支持。
- leet 折叠保留字变体（0→o、1→i/l 双变体查表）：竞品均不做，YAGNI；残余面已在影响中披露。
- 随机字母表切换 31 字符无混淆集（16 字符 / 79.3 bit）：v0.3 候选，与本决策零耦合，两代地址正则兼容。
- 自定义创建独立子配额：共享 ADR 0017 单一日配额（KISS），部署者可整体调低上限。
- 永久墓碑：SimpleLogin / addy.io 对共享域采用永久墓碑，但违反「一切有界」；以默认 90 天、上限 3650 天的冷却替代，公共实例建议调大。
- 保留字迁移种子表：清单是版本化行为而非运行时数据，选择编译期内置随二进制固定（供应链固定原则），不建 SQL 种子表。
- postmaster / abuse 的真实投递（运维别名到指定 Inbox）：不在本 ADR 范围；RFC 5321 §4.5.1 的 postmaster 接收义务由部署层 Postfix 别名策略决定，列为 v0.3 议题。

## 验证要求

- Unit：校验复用与边界（长度 1/64、后缀模式 59/60、`..`、首尾分隔符、`"Alice"` 规范化、单字符与纯数字合法、`a.-b` 维持放行）；保留字匹配（整体精确、token 命中 `admin.zhang` / `billing-paypal`、放行 `admin2024` / `supportive`、EXTRA 追加生效、ENFORCED=false 时硬五词仍拒绝）；后缀字母表、长度与 `crypto/rand` 来源。
- 保留字分词匹配作为新增用户输入攻击面，运行固定时长 Fuzz。
- HTTP Contract：新字段解析；三个稳定错误码的统一 Error Envelope 与 `request_id`；保留字拒绝不消耗配额（`RateLimit-Remaining` 不变）；冲突与墓碑拒绝消耗配额不退款；省略 localPart 时随机路径契约零变化（地址仍为 20 字符、既有错误映射不变）；`MAILWISP_LOCAL_PART_SUFFIX_REQUIRED=true` 的拒绝行为。
- PostgreSQL Integration（固定版本真实库）：并发同名创建恰一个成功、另一个 409 且配额已消耗；DELETE API 与 TTL 过期清理两条路径都在同一事务写墓碑；冷却期内重建返回与活跃冲突相同响应、过期后可重建；`MAILWISP_ADDRESS_TOMBSTONE_DAYS=0` 禁用行为；每次成功自定义创建最多清理 100 条过期墓碑；随机地址永不写墓碑；带既有数据升级验证 `custom_local_part DEFAULT false` 语义。
- LMTP Integration：RCPT 到墓碑地址返回既有 `550 5.1.1`，投递路径不查询墓碑表。
- Adapter Contract：CF 清洗（`"Ad-Min!"` → `admin`）命中保留字映射为上游文本错误、全清洗为空维持既有错误路径；DuckMail `{error,message}` Envelope 422/409；YYDS 无契约变化。
- 可观测性：`reason` 固定 4 值、无地址 Label、被拒 localPart 不出现在日志。
- AGENTS 第 13 节基础门禁（gofmt、`go test ./...`、`-race`、`go vet`、`govulncheck`）与全仓安全扫描随变更通过。
