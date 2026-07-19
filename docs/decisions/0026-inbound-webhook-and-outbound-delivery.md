# ADR 0026：收件 Webhook 与统一出站投递底座

状态：提议中（草案，未接受）
日期：2026-07-19

## 背景

ADR 0024 把 Webhook 列为工作台 P1 能力（「先做有界投递，再考虑复杂自动化」）。docs/product/feature-dossiers.md 第 2 章完成了竞品与规范调研：测试类工具（Mailpit、MoeMail）只发元数据或全文但零签名、零或弱重试；商业收件服务（Postmark、Mailgun、SendGrid、ForwardEmail）发全文载荷加 HMAC 与固定退避；签名事实标准已收敛到 Standard Webhooks（webhook-id / webhook-timestamp / webhook-signature 三头，Svix、Stripe、GitHub 同构可互证）。本 ADR 采纳该章推荐规格，具体先例与出处以档案为依据，此处不再复制。

MailWisp 既有约束决定了实现形状：

- ADR 0004 明确「Durable Persistence 前禁止执行 Webhook」，触发点只能在持久化与 Parse 终态之后；
- ADR 0009 的 PostgreSQL 队列模式（`FOR UPDATE SKIP LOCKED` + 随机 Lease Token Fencing + Attempt + Backoff + 容量 1 的 Wake Channel）已被 Parser 与 Content Deletion 两次生产验证，无需新队列设施；
- ADR 0005 预留了 `wisp_whsec_v1_<kid>_<secret>` 语法，但明确记下欠账：whsec 是发送方必须取回的 Key Material，不能用不可逆 Digest 存储，正式 Webhook 前必须由独立 ADR 决策存储或派生方案；
- ADR 0013 的网络矩阵目前只有四个内部网络，没有任何受控出站通道；Webhook 是第一个主动向用户提供的 URL 发起出站 HTTP 的能力，SSRF 与出站资源上限是新攻击面；
- 档案第 10 章（v0.4 通知渠道）的结论是：通知与 Webhook 同属「内置模板的出站投递」，必须共用同一队列、Worker 与 Egress 防护。因此投递底座第一天就必须通用化，webhook 只是第一种渠道。

## 决策总则

1. 事件契约完全采用 Standard Webhooks：三个固定请求头加 HMAC-SHA256 对 `id.timestamp.body` 签名，用户可直接用官方 standardwebhooks 库验签。
2. 载荷走 thin 路线：元数据加有界摘要加拉取 URL，冻结字节串 ≤16384 字节；HTML、附件内容与 Raw MIME 永不出站，全文经 Canonical API 用 Capability 拉取。
3. 队列复用 ADR 0009 模式建 Transactional Outbox：新表 `webhook_endpoints`（渠道配置）、`outbound_deliveries` 与 `outbound_delivery_attempts`（通用投递底座，`channel_type` 列从第一天存在，本版本仅 `webhook`）。入队在 Parse 终态事务与 LMTP 收件事务两点原子完成。
4. whsec Key Material 采用部署主密钥 HKDF 派生：新增一份 32 字节 Compose Secret，PostgreSQL 零密钥材料，清偿 ADR 0005 欠账。
5. SSRF 防线放在 `net.Dialer.Control`（DNS 解析后、连接前逐 IP 判定，默认全拒），并以本 ADR 声明对 ADR 0013 网络矩阵的一项修订：新增仅 app 附加的 outbound-only `egress` 网络。
6. 一切有界：Worker 数、每端点并发、HTTP 超时、响应体读取、退避窗口、队列深度、Retention、每 Inbox 端点数全部有硬上限；投递子系统任何故障不得反压 LMTP 收件主路径。

## 事件契约与签名

请求为 `POST`，`Content-Type: application/json`，`User-Agent: MailWisp-Webhook/1`。

请求头（与 Standard Webhooks 1.0.0 逐项对齐）：

- `webhook-id`：等于 `outbound_deliveries.id`（UUIDv7 文本）。重试与手动重发均不变，是消费端幂等键；
- `webhook-timestamp`：本次尝试的 Unix 秒，每次尝试刷新；
- `webhook-signature`：`v1,` + Base64(HMAC-SHA256(key, webhook_id + "." + webhook_timestamp + "." + body_bytes))。轮换重叠期内旧新两代密钥各出一个签名，空格分隔并列。`id` 与 `timestamp` 由服务端生成且不含 `.`。

载荷 v1（在入队事务内构造并冻结为最终字节串）：

- 顶层：`type`（v1 仅 `message.received`，事件名遵循 `[a-z0-9_.]` 点分语法预留扩展）、`timestamp`（事件构造时刻 UTC ISO8601，冻结后不随重试变化）、`data`；
- `data`：`message_id`、`inbox_id`（均 UUIDv7）、`inbox_address`、`envelope_sender`（≤320 字节）、`received_at`（ISO8601）、`parse_status`（`parsed|failed`）、`subject`（≤998 字节，`failed` 时为 null）、`from`、`to`（`{name,address}` 数组，取自 `mail_content_parses`，各 ≤10 项）、`text_snippet`（有界 Text Preview 前 2048 字节，UTF-8 安全截断）、`attachments`（仅 `{filename,content_type,size_bytes}` 元数据，≤100 项）、`api_url`；
- `api_url` 指向既有 Canonical 路由 `https://<host>/api/v1/inboxes/me/messages/<message_id>`，消费端持该 Inbox 的 Capability 拉取全文；
- `parse_status=failed`（恶意或超限 MIME）时载荷降级为 thin 元数据（subject 为 null、无 snippet），仍必须投递——Parser 失败不得静默吞掉通知。

接收端验证要求写入用户文档并提供指向 standardwebhooks 官方库的示例：常量时间比较、时间戳容差 300 秒、`webhook-id` 短期去重、双方保持 NTP 同步。

## 数据模型与 Migration

新增单调 Migration（版本号以实际合入顺序为准）创建三张表，不修改任何既有表；升级测试必须使用带真实既有数据的数据库，不得只验证空库建表（ADR 0009 纪律）。

`webhook_endpoints`（沿用既有 Capability 表风格）：

- `id` UUIDv7 主键；`inbox_id` 外键 `ON DELETE CASCADE`；
- `url` text，CHECK 前缀 `^https?://` 且 `octet_length(url) <= 2048`；`description` text 默认空串；
- `status` ∈ {`active`,`disabled_manual`,`disabled_auto`}；`disabled_reason` 与 `status` 互锁（active 时必须为 NULL，非 active 时必须非 NULL）；
- `secret_kid` 24 位小写 Hex UNIQUE；`secret_generation` integer > 0，默认 1；`prev_generation_valid_until` timestamptz（轮换重叠窗口）；
- `event_mask` integer 默认 1，CHECK `event_mask > 0 AND (event_mask & ~1) = 0`（v1 只允许 bit0 = message.received，扩展位由未来 Migration 放开）；
- `consecutive_failures` integer ≥ 0；`created_at` / `updated_at`；
- 索引 `(inbox_id, created_at DESC, id DESC)`。

`outbound_deliveries`（通用出站投递底座，复刻 ADR 0009 已验证列组）：

- `id` UUIDv7 主键（即 `webhook-id`）；
- `channel_type` text NOT NULL 默认 `webhook`，CHECK 本版本仅 `('webhook')`——v0.4 通知渠道通过新增单调 Migration 扩宽枚举并增加对应渠道绑定列与互斥约束，共用本表、本 Worker 与本 Egress 防线，不另起炉灶；
- `endpoint_id` 外键 `webhook_endpoints ON DELETE CASCADE`（本版本 NOT NULL）；`message_id` 外键 `messages ON DELETE CASCADE`；
- `event_type` text NOT NULL 默认 `message.received`；
- `payload_body` text NOT NULL，CHECK `octet_length(payload_body) <= 16384`。必须用 text 而非 jsonb：jsonb 的键序正规化会破坏「签名字节 = 发送字节 = 存储字节」不变式，这是 Standard Webhooks 点名的最常见验签故障模式；
- `status` ∈ {`pending`,`processing`,`delivered`,`failed`}；`attempts` ≥ 0；`available_at`；`lease_token` / `lease_until`（processing 时必须同时非空，否则必须同时为空）；`last_status_code`；`last_error_code`（CHECK `^[a-z][a-z0-9_]{0,63}$`）；`delivered_at`（delivered 时必须非空）；`created_at` / `updated_at`；
- `UNIQUE (endpoint_id, message_id, event_type)` 兜双入队点与 Postfix 重投幂等；
- 部分索引 `(available_at, created_at, id) WHERE status IN ('pending','processing')`。

`outbound_delivery_attempts`（投递日志，行数被 max_attempts 硬界定）：

- 主键 `(delivery_id, attempt)`；`delivery_id` 外键 `ON DELETE CASCADE`；
- `attempted_at`、`duration_ms`、`outcome` ∈ {`delivered`,`http_error`,`timeout`,`connect_error`,`tls_error`,`blocked_ip`,`body_too_large`,`gone`}、`status_code`、`error_code`；
- 绝不存储请求体或响应体。

## 入队与投递语义

入队点两个，均在既有事务内原子完成，不新增进程或队列设施，满足 ADR 0004「持久化前禁止触发」：

1. Parser Worker 提交 Parse 终态（`parsed` 或 `failed`）的同一 PostgreSQL 事务内，`INSERT ... SELECT` 为所有引用该 `content_key` 且其 Inbox 拥有 `active` 端点的 Message 各生成一条 Delivery，载荷此刻构造并冻结；
2. LMTP 收件事务内，若该 Content 已处于终态（Postfix 重投或同内容再次投递），直接为新 Message 入队。

同一 Content 多 Recipient 或重投形成多条 Message 时，每条 Message 独立 Delivery，保留 ADR 0004 的重复投递语义；消费端以 `webhook-id` 幂等。进程内 Wake Channel 容量 1 唤醒投递 Worker，PostgreSQL 是唯一事实源，启动扫描与保守 Polling 保证恢复（ADR 0009 同款）。

投递 Worker 参数（Reference Profile 固定值）：

- Worker：2，独立于 Parser Worker 池，连续排空可领取任务；空闲 Poll 1 秒 + 最多 20% Jitter；
- 领取：`FOR UPDATE SKIP LOCKED` 选取 `available_at` 已到期的 pending 行与租约已过期的 processing 行，附加 `NOT EXISTS`（同 endpoint 的有效 processing 行）实现每端点并发 1；每次领取生成新的随机 UUID Lease Token、`attempts+1`、`lease_until = now() + 60s`；
- 领取后实时读取端点当前行（URL、status、secret generation）与 Inbox 有效性，不用陈旧快照：端点非 `active` 则本 Delivery 置 `failed(endpoint_disabled)`；Inbox 已过期或已删除则置 `failed(inbox_expired)`——过期 Inbox 的全部 pending 投递就此终态化，不为已过期邮箱出站；Inbox 与端点删除由外键 CASCADE 直接清除；
- HTTP：总超时 15 秒（连接 5 秒、TLS 5 秒、响应头 10 秒细分），Lease 60 秒必须长于总超时；响应体经 `io.LimitReader` 最多读 8 KiB 后丢弃，只记录状态码；
- 退避：首发立即；第 n 次尝试失败后（n = 1..7）`available_at = now() + backoff[n]`，backoff = (30s, 2m, 10m, 30m, 2h, 6h, 12h)，每步 ±20% Jitter；max_attempts = 8，总窗口约 20.7 小时；429/503 携带合法 `Retry-After` 时取 `max(序列值, min(Retry-After, 1h))`，恶意超大值被 clamp 到 1 小时；
- 状态码语义：2xx 成功；3xx 一律失败且 `CheckRedirect` 拒绝跟随；410 Gone 使本 Delivery 终态 `failed(gone)` 且端点 `disabled_auto(reason=gone)`；其余 4xx/5xx/超时/连接错误按序退避——v1 不给其他 4xx 特权停止语义，避免消费端误触发静默丢失；
- 熔断：投递尝试耗尽（max_attempts 用完或 410）进入终态 `failed` 时端点 `consecutive_failures + 1`，`delivered` 清零；达到 20 次转 `disabled_auto(reason=persistent_failure)`；管理性终态（`queue_overflow`、`inbox_expired`、`endpoint_disabled`）不计入熔断计数；控制台可一键重新启用并重放；
- Fencing：完成、失败、释放都必须同时匹配 `id + lease_token`，旧 Worker 不能覆盖新 Owner；优雅停机主动释放租约并回退 Attempt，强杀依赖租约到期恢复；语义为 at-least-once，不承诺 exactly-once；
- 有界收尾：终态 Delivery 与其 Attempts 保留 7 天，由既有清理 Worker 收割；每端点未完成（pending + processing）深度上限 1000，超限时最旧 pending 置 `failed(queue_overflow)`；
- 手动重发：重置为 pending、`attempts` 归零、复用冻结的 `payload_body`、发送时生成新 timestamp 与新签名、`webhook-id` 不变；允许对已 `delivered` 的投递重发（消费端可能丢数据），幂等键语义保持。

## whsec Key Material：部署主密钥 HKDF 派生

本节清偿 ADR 0005 记下的 whsec 存储欠账。

- 新增第 5 份 Compose Secret 文件 `mailwisp_webhook_master_key`：32 字节 CSPRNG；Linux 文件 0444、父目录 0700，与既有 DB、Session、Quota Secret 同一生成脚本与注入通道（ADR 0013 模式），不进环境变量、进程参数或 Compose 配置；`serve` 缺失该文件拒绝启动（fail-closed，与 ADR 0017 HMAC Key 同待遇），`migrate`、Backup、Restore 等离线 Role 不要求；
- 派生：`secret_bytes = HKDF-SHA256(IKM = master_key, salt = 空, info = "mailwisp-whsec-v1\x00" || endpoint_kid || "\x00" || uint32_be(generation))`，取 32 字节，落在 Standard Webhooks 推荐的 24–64 字节区间；
- PostgreSQL 只存 `secret_kid`（24 Hex）与 `secret_generation`，数据库零密钥材料：泄库对 Webhook 签名零暴露，与 ADR 0005「Token 泄库不可用」同强度；ADR 0006 备份包天然不含密钥；
- 展示：控制台在创建与轮换时一次性并列展示同一 32 字节的两种编码——Canonical `wisp_whsec_v1_<kid>_<secret>`（`secret` 为 43 字符无 Padding Base64URL，已有 Gitleaks 规则覆盖）与 Standard Webhooks 兼容形式 `whsec_<base64>`（需追加 Gitleaks 扫描规则）；遗失只能通过轮换取得新代；
- 轮换：`generation + 1` 派生新密钥，端点行设 `prev_generation_valid_until = now() + 24h`，重叠期内每次投递并列发出新旧双签名，到期旧代自动失效；撤销 = 删除端点或轮换，立即生效；
- 进程内 `kid -> key` 使用有界 LRU 缓存（256 项），避免每次投递重复 HKDF；
- 代价如实披露并写入运维文档与 ADR 0022 灾备演练清单：主密钥文件丢失或轮换意味着全部 whsec 失效，用户必须重新获取密钥；Restore 到新机必须连同 Secret 文件恢复，否则 Webhook 签名全部不可用而系统其余功能正常；
- 对比后拒绝 Svix 式加密落库（XChaCha20-Poly1305/AES-256-GCM 信封）：同样依赖主密钥文件，却多出密文列、信封逻辑与主密钥轮换时的全表重加密，且其「未配置则明文」回退违反 MailWisp 默认拒绝纪律；该方案仅在「必须支持用户自带 secret」时有优势，而本 ADR 明确不做用户自带 secret。需要真实存储的渠道凭据（Telegram Bot Token 等）的 AEAD 加密方案留给 v0.4 通知渠道 ADR，不在本 ADR 决策。

## SSRF 防护与受控 Egress（对 ADR 0013 的修订）

出站目标是用户输入的任意 URL，防线分层如下，默认全拒。

URL 解析层（创建与更新时校验）：

- 拒绝含 userinfo（`user@host`）的 URL；scheme 默认仅 `https`；
- 端口白名单默认 `{443}`，`MAILWISP_WEBHOOK_ALLOWED_PORTS` 只允许从 `{80, 443, 8443}` 中追加；
- URL 总长 ≤2048 字节（列约束同款）。

拨号层（唯一权威判定点）：

- 不做任何预解析检查，全部判定放在 `net.Dialer.Control`：标准库在 DNS 解析后、connect 前对每个候选地址（含 Happy Eyeballs 的每一次）回调；`network` 仅接受 `tcp4/tcp6`；`net.SplitHostPort` 取目的 IP，`netip.Addr.Unmap()` 后用预编译 `netip.Prefix` 表 `Contains` 判定，命中拒绝清单即返回 error 中止拨号。判定对象是解析产物而非 URL 文本，天然免疫 DNS Rebinding（首答公网、次答内网）与十进制/八进制/0x 混淆 IP；任何「先解析、自查、再交 http.Client 二次解析」的两段式都会被恶意 DNS 击穿，明确禁止；
- IPv4 拒绝清单：`0.0.0.0/8`、`127.0.0.0/8`、`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`100.64.0.0/10`（CGNAT）、`169.254.0.0/16`（整段 Link-local，含 169.254.169.254 云 Metadata）、`192.0.0.0/24`、`192.0.2.0/24`、`198.51.100.0/24`、`203.0.113.0/24`（TEST-NET）、`198.18.0.0/15`（Benchmark）、`224.0.0.0/4`（组播）、`240.0.0.0/4`（保留）、`255.255.255.255/32`；点名云 Metadata：`169.254.169.254`（AWS/GCP/Azure）、`168.63.129.16`（Azure Wireserver）、`100.100.100.200`（阿里云，已被 CGNAT 段覆盖）；
- IPv6 拒绝清单：`::/128`、`::1/128`、`fe80::/10`、`fc00::/7`（ULA，覆盖 AWS IMDS IPv6 `fd00:ec2::254`）、`ff00::/8`、`64:ff9b::/96`（NAT64）、`2001:db8::/32`；`::ffff:0:0/96`（IPv4-mapped）必须 Unmap 成 IPv4 后按 IPv4 清单复查；
- 权威依据 IANA Special-Purpose Address Registry 与 OWASP SSRF Cheat Sheet 最小集。

HTTP 客户端层：

- `http.Client.CheckRedirect` 直接返回错误：重定向一律失败不跟随（302 到 `http://169.254.169.254/` 即经典逃逸）；
- TLS `ServerName` 保持原始域名，MinVersion TLS 1.2，系统 CA；绝不提供 `InsecureSkipVerify`。

自托管逃生阀（两个配置拆分，默认全关）：

- `MAILWISP_WEBHOOK_ALLOW_PRIVATE_CIDRS`（默认空）：管理员显式豁免的 CIDR 清单（如内网 Home Assistant），豁免只在 Control 层生效，启动时打印豁免清单留审计痕迹；
- `MAILWISP_WEBHOOK_ALLOW_HTTP_FOR_PRIVATE`（默认 false）：仅当本次连接的每个目的 IP 都命中已豁免 CIDR 时允许 `http` scheme；公网目标恒定 `https`。

Compose Egress（本 ADR 声明的 ADR 0013 网络矩阵修订）：

- 新增第 5 个网络 `egress`（bridge，`internal: false`，部署内唯一可出公网的网络），只有 app 一个服务附加，不发布任何新 Host 端口；出站 DNS 走该网络的 Docker 内嵌解析器；
- `database`、`lmtp`、`frontend`、`smtp_ingress` 四个既有内部网络矩阵不变；postgres、edge、postfix 均不接入 `egress`；
- 纵深三层：第一层 Docker 网络拓扑（`egress` 上没有任何内部服务可达）；第二层进程内 Control 钩子 IP 清单（Docker 网段 172.16.0.0/12、Host Gateway 与 `host.docker.internal` 解析产物全部落在 RFC1918 清单内被拒）；第三层可选，文档提供宿主 `DOCKER-USER` nftables 片段对 egress 网桥的私网目的地 drop，不强制；
- 该网络与共享的 Egress HTTP 客户端、SSRF Guard 一起构成通用出站底座，v0.4 通知渠道直接复用。

## 管理 API 与配额

管理端点挂在既有 Canonical Inbox 作用域下，认证接受 Bearer Capability 或 Browser Session（写路径要求 CSRF，ADR 0012），每个对象校验 Inbox Ownership 与 Scope，统一 Error Envelope 与 Request ID：

- `POST /api/v1/inboxes/me/webhook-endpoints`：创建（Body：`url`、可选 `description`）；响应一次性返回 whsec 明文的两种编码；
- `GET /api/v1/inboxes/me/webhook-endpoints`：列表，不含 secret，含 `status`、`disabled_reason`、`consecutive_failures`；
- `PATCH /api/v1/inboxes/me/webhook-endpoints/{id}`：更新 `url`、`description`，或在 `active` 与 `disabled_manual` 间切换（重新启用 `disabled_auto` 端点同走此路径并清零熔断计数）；
- `DELETE /api/v1/inboxes/me/webhook-endpoints/{id}`：删除，CASCADE 清除投递记录；
- `POST /api/v1/inboxes/me/webhook-endpoints/{id}/rotate-secret`：轮换（`generation + 1`），一次性返回新明文；
- `GET /api/v1/inboxes/me/webhook-endpoints/{id}/deliveries`：投递日志（状态、状态码、耗时、稳定错误码；无请求/响应体），确定排序加有界 Pagination（limit 1..100）；
- `POST /api/v1/inboxes/me/webhook-endpoints/{id}/deliveries/{delivery_id}/redeliver`：手动重发（语义见投递章节）。

配额：每 Inbox 最多 3 个 `active` 端点（Mailgun 同值先例），固定值不开配置；创建事务内锁定 Inbox 行并复核计数（与 ADR 0015 Commit 复核同思路），并发创建不可穿透。滥用面与 ADR 0015/0017 配额体系联动封顶：单身份每 UTC 日 ≤100 个 Inbox × 3 端点 × 每端点队列深度 1000 与 Worker 全局 2 并发，出站放大能力有硬性天花板。URL 校验（scheme、端口、userinfo、长度）在创建与更新时同步执行。

## 可观测性与日志

- Metrics 遵守 ADR 0014 低基数纪律：Counter `mailwisp_outbound_deliveries_total{channel_type, outcome}`，`channel_type` 本版本仅 `webhook`，`outcome` ∈ {`delivered`,`retryable_error`,`permanent_error`,`rate_limited`,`channel_disabled`} 固定 5 值（与档案第 10 章 v0.4 通知渠道共用同名指标与枚举）；Gauge `mailwisp_outbound_delivery_pending`（无 Label，队列积压）。禁止 `endpoint_id`、URL、Inbox、地址或错误文本 Label；
- `last_error_code` 与 Attempt 的 `error_code` 只入库，为 ≤64 字节稳定小写码（ADR 0009 惯例）；`subject`、`envelope_sender` 等攻击者可控字段只进 JSON 载荷（encoding/json 转义），绝不回写进错误码、日志或 Metrics；
- 日志：成功为 Debug，Retry 与 Terminal Failure 使用稳定 Code；不记录请求体、响应体、Secret、Token 或完整邮件内容。

## 影响

- ADR 0005 的 whsec Key Material 欠账由本 ADR 清偿；`wisp_whsec_v1` 从「仅预留语法」变为可签发；
- ADR 0013 网络矩阵增加 `egress` 网络一行（app -> internet:443），Compose Secret 从 4 份增至 5 份，生成脚本、Preflight 与 ADR 0022 灾备演练清单同步更新；
- Parse 终态事务内追加 `INSERT ... SELECT` 入队会小幅延长 Parser 事务，必须复跑 ADR 0019 Compose 容量基准确认无尾延迟回退；
- 「临时邮箱过时即逝」与 20.7 小时重试窗口的张力由「Inbox 过期即终态化 pending 投递」裁决：通知的生命周期严格短于邮箱本身；
- v0.4 通知渠道在 `outbound_deliveries` 上扩展 `channel_type` 枚举与渠道绑定列，复用同一 Worker、退避、熔断、Egress 客户端与 SSRF Guard，不再新建队列。

## 暂不采用/明确不做

- 端点创建时的 URL 验证握手（`endpoint.verify` 事件要求 2xx）：Svix、Stripe 与各收件服务均无强制验证，以「首条真实投递失败可见」替代（YAGNI）；
- 事件类型扩展：v1 仅 `message.received`；`event_mask` 预留位但 CHECK 锁死为 1，扩展必须走新 ADR 与新 Migration；
- 用户自带 secret（BYO whsec）：破坏零落库派生模型；
- 非对称 `v1a` 签名（ed25519，`whsk_`/`whpk_`）；
- 除 410 外的 4xx 特权停止语义（不采纳 Mailgun 406 与 Postmark 403 先例）；
- HTML 源码、附件内容、Raw MIME 出站；不可信内容最大出站面是 2 KiB 文本摘要加元数据；
- WebSocket 或长轮询型实时推送：实时通道由 ADR 0027 的 `POST /api/v1/inboxes/me/messages/next`（原子领取长轮询）与 SSE 承担，不属于 Webhook；
- smokescreen 出站代理容器：Reference Profile 不加常驻服务，保留为 Extended 纵深选项写入部署文档；
- 静态出口 IP 声明与 IP 白名单发布；
- Svix 式 whsec 加密落库与「未配置则明文」回退；需存储的渠道凭据 AEAD 加密留给 v0.4 通知渠道 ADR；
- `wh_` 前缀展示层 Delivery ID：`webhook-id` 直接使用 Delivery UUIDv7，不引入新 ID 语法；
- Redis、Broker 或独立投递服务（硬约束沿用 AGENTS §2）。

## 验证要求

本 ADR 为草案，以下为接受前必须完成的验证清单，未验证项不得声称完成：

- 签名互操作：官方 standardwebhooks 库对 MailWisp 发出的 `id`/`timestamp`/`body`/签名验证通过；轮换重叠期双签名任一代可验，旧代到期后不可验；HKDF 固定向量测试（固定 master/kid/generation 派生固定 key）；数据库断言零密钥材料列；
- 载荷字节稳定性：入队冻结字节、重试字节、手动重发字节逐字节一致；≤16384 字节上限与 `failed` 降级载荷分支覆盖；
- 队列 Integration（固定版本真实 PostgreSQL）：并发 Worker `SKIP LOCKED` 不重复领取；每端点并发 1；Lease 过期重领取与旧 Token Stale Claim 拒绝；退避序列与 `available_at`、Retry-After clamp；`UNIQUE` 幂等覆盖双入队点与 Postfix 重投；`queue_overflow`、7 天 Retention、Inbox 过期与端点禁用在领取时终态化；优雅停机释放租约并回退 Attempt；
- SSRF 表测：IPv4/IPv6/IPv4-mapped/NAT64 全清单命中拒绝；DNS Rebinding（首答公网、次答内网）在 Control 层被拒；重定向到内网判失败；userinfo、scheme、端口、超长 URL 创建被拒；豁免 CIDR 放行且启动日志可见、http 仅在豁免加显式开关下可用；
- HTTP 行为：2xx/3xx/410/429/5xx/超时/慢响应/响应体 8 KiB 截断各分支；410 自动停用端点；连续 20 次熔断与管理性终态不计数；redeliver 重置语义；
- 管理 API：Ownership、Scope、CSRF、统一 Error Envelope；每 Inbox 3 端点并发创建不可穿透；
- 部署 Contract：Compose 渲染含 `egress` 网络且仅 app 附加；postgres/edge/postfix 无出站路径；Secret 文件缺失时 `serve` 拒绝启动而离线 Role 不受影响；Gitleaks 新增 `whsec_` 兼容形式规则并通过工作树与历史扫描；
- Metrics 单测：固定名称与 Label 枚举，无高基数 Label；
- 基准：复跑 ADR 0019 Compose 容量基准，确认 Parse 终态事务入队无尾延迟回退；
- 全仓门禁：gofmt、`go test ./...`、`-race`、`go vet`、govulncheck、安全扫描与既有 Integration/E2E 全绿。
