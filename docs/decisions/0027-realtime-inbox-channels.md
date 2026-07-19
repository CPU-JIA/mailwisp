# ADR 0027：实时收件通道采用原子领取长轮询与 SSE 双通道

状态：提议中（草案，未接受）
日期：2026-07-19

## 背景

Canonical 收件目前只有 Cursor 列表拉取（ADR 0020）：Vue 控制台以 10 秒 `setTimeout` 轮询第一页（`web/src/useMailbox.ts`），自动化客户端只能自行循环列表、挑选未读、再 PATCH 标已读——两个并发消费者会拿到同一封邮件，验证码场景的端到端延迟也被轮询间隔垫高。

横纵分析报告 §4.2 的既定立场是：Web 实时能力以 Long Poll 与 Cursor 为正确性基线，SSE 只作界面优化；事件只负责唤醒，PostgreSQL 才是唯一事实源。业界样本同向：mail.tm Mercure 与 JMAP RFC 8620 的推送都只携带状态变化触发客户端重拉；YYDS 的 `/messages/next` 以单事务原子领取证明了自动化取信的正确形态；DuckMail 则是「保留 SSE 路径但实际靠轮询」的反面教材——存在端点不等于实时，必须有生产 E2E 证明真实事件到达。

落地存在两个必须显式处理的平台陷阱：全局 `http.Server` WriteTimeout 默认 15 秒（`internal/config/config.go:167`）会杀死任何长寿命响应；Compose Nginx 对 `/api` 的通用 regex location 只有 30 秒 `proxy_read_timeout` 且默认 `proxy_buffering on`，会缓冲吞掉 SSE 并切断 30 秒长轮询。

服务端出站推送（Webhook）由 ADR 0026 另行定义；本 ADR 只覆盖客户端拉取与浏览器事件两条入站通道。调研依据与竞品取值见 `docs/product/feature-dossiers.md` 第 3 章与第 6 章（`/messages/next` 部分），本 ADR 只固化决策。

## 决策

新增两条实时通道，共享同一个进程内 Wake Hub。事件只做唤醒提示，事实判定永远来自数据库查询；前端轮询兜底保留，任何实时通道故障只增加延迟、不丢邮件。

### 通道 A：POST /api/v1/inboxes/me/messages/next（原子领取长轮询）

- 方法为 `POST`：领取会把消息标为已读，是有副作用的状态修改，不得伪装成安全 `GET`。
- 认证仅接受 `Authorization: Bearer` Capability（`wisp_cap_v1_*`，ADR 0005），不接受 Session Cookie。Bearer 请求按 ADR 0012 不使用 Cookie 身份、不要求 CSRF，因此该端点没有 CSRF 面；浏览器不调用本端点。
- Query 参数 `wait` 为整数秒，缺省 0；合法域为 `0..min(30, MAILWISP_HTTP_LONGPOLL_MAX_WAIT)`。非整数或越域返回 `400`，稳定码 `invalid_wait`，不做静默钳制。
- 命中返回 `200`，Body 为统一信封 `{"data":{"message":<与 GET /api/v1/inboxes/me/messages/{id} 相同的消息详情对象>}}`；等待窗口耗尽仍无未读返回 `204` 且无 Body，客户端不得对 204 按 JSON 信封解析。认证失败与 Inbox 失效沿用既有默认拒绝语义，不泄露存在性。
- `wait` 窗口与 204 语义对齐 YYDS 以便脚本迁移；方法与认证遵循 Canonical 规范，不复制第三方形状（ADR 0002）。

### 原子领取事务（FOR UPDATE SKIP LOCKED）

单个短事务（Read Committed 即可，SKIP LOCKED 即 PostgreSQL 为多消费者队列表设计的原语），事务内不等待、不做文件或网络 I/O：

```sql
WITH next AS (
    SELECT m.id
    FROM messages m
    WHERE m.inbox_id = $1
      AND m.seen_at IS NULL
      AND EXISTS (
          SELECT 1 FROM inboxes i
          WHERE i.id = m.inbox_id
            AND i.status = 'active'
            AND (i.expires_at IS NULL OR i.expires_at > now())
      )
    ORDER BY m.received_at ASC, m.id ASC
    LIMIT 1
    FOR UPDATE OF m SKIP LOCKED
)
UPDATE messages msg
SET seen_at = $2
FROM next
WHERE msg.id = next.id
RETURNING msg.id, msg.envelope_sender, msg.received_at, msg.content_key;
```

- `SKIP LOCKED` 保证并发领取者各拿不同行、无人重复消费（与 ADR 0009 Parser 领取同型）；`$2` 为领取时刻 UTC。
- Inbox 存活守卫允许 `expires_at IS NULL`：Owner 持久 Inbox 可无固定到期（AGENTS §7），不得被判为不可领取。
- 同事务内 JOIN `mail_content_parses` 组装详情后提交。Parser 尚未完成时 `parse_status` 如实返回 `pending`，领取判定只看 `seen_at`，不得按解析状态过滤、等待或伪装解析完成。
- 排序 `received_at ASC, id ASC` 由既有 `messages_inbox_received_idx (inbox_id, received_at DESC, id DESC)` 反向扫描覆盖，单 Inbox 上限 500 条（ADR 0015）。本决策零 Schema 变更、零 Migration（`seen_at` 已由既有 Migration 提供）。仅当未来基准证明退化时，以新增单调 Migration（版本号以实际合入顺序为准）加 `(inbox_id, received_at, id) WHERE seen_at IS NULL` 部分索引。

### 等待循环（事务外等待，先订阅后查库）

1. 向 Wake Hub 注册本 Inbox 的 waiter channel（cap=1，lossy）。
2. 执行一次领取事务；命中即注销 waiter 并返回 200。
3. 未命中且剩余窗口大于 0 时 `select { <-waiter; <-timer(剩余窗口); <-ctx.Done() }`；被唤醒回到第 2 步；窗口尽头返回 204。

先订阅后查库消除「查空与来信之间」的丢唤醒竞态；唤醒只是提示，事实判定永远是第 2 步的事务。`ctx.Done()`（客户端断开或 Shutdown）立即注销并放弃领取，未领取的邮件保持未读，无需补偿。被 `NotifyGone` 唤醒或领取事务发现 Inbox 非 active 时立即按统一失败语义返回，不等窗口。等待期间以 `http.ResponseController.SetWriteDeadline(now + wait + 10s)` 按连接覆盖全局 WriteTimeout。

### 通道 B：GET /api/v1/inboxes/me/events（SSE）

- 认证仅接受 `__Host-mailwisp_session` Cookie（ADR 0012）：EventSource 无法设置 Authorization Header，而 Query 携带凭据被禁止；纯只读 GET 按 ADR 0012 免 CSRF；不开 CORS、不设 `Access-Control-Allow-Credentials`。Bearer 自动化一律走通道 A。未配置 `MAILWISP_BROWSER_SESSION_KEY` 的部署随 Session 路由一并关闭本端点，前端停留在轮询兜底。
- 响应头：`Content-Type: text/event-stream; charset=utf-8`、`Cache-Control: no-store`、`X-Accel-Buffering: no`；连接建立立即发送 `retry: 5000` 与首个 `ping`。
- 事件集固定为三种，不设置 `id` 字段：
  - `message-new`，data `{"message_id":"<uuid>","received_at":"<RFC3339 UTC>"}`——不含 subject、sender、正文或 cursor；
  - `ping`，data `{"interval":25}`，按心跳间隔发送；
  - `inbox-gone`，Inbox 删除或过期时发送后服务端关流，客户端停止重连并走失效流程。
- 不实现 Last-Event-ID 重放：重连语义 = `onopen` 后立即重拉 ADR 0020 Cursor 列表第一页并按 Message ID 去重合并，Cursor 列表就是补课通道。
- 服务端写路径：每次写事件前 `rc.SetWriteDeadline(now+10s)`，写后 `rc.Flush()`；写失败（慢客户端、断链）立即注销订阅并返回。订阅者缓冲恒为 1 帧唤醒信号，慢客户端只会少收唤醒，不产生无界队列。
- 连接硬关闭死线在 accept 时一次算定：`min(Session 到期, Inbox expires_at（如有）, now + MAILWISP_HTTP_SSE_MAX_AGE)`，流内不轮询数据库；到龄发注释帧后主动关流，浏览器自动重连并重新通过认证与到期检查。即使 `inbox-gone` 广播丢失，连接也会按死线收敛。

### 进程内 Wake Hub（双通道共享，零新增常驻 Goroutine）

- 结构：Mutex 保护的 `map[inboxID][]subscriber`，`subscriber = {ch chan struct{} (cap=1), kind sse|longpoll}`；`Notify(inboxID)` 对每个 ch 非阻塞发送、满即合并丢弃——与 `internal/jobs/parser.go` 的 `Notify`/wake(cap=1) 完全同型（ADR 0009）。
- 发布点：扩展 `internal/app/app.go` 既有 `wakingReceiver`（当前只调 `parserWorker.Notify`），在 LMTP 投递持久化成功后对每个 Recipient Inbox 调 `hub.Notify`；Inbox 删除 Handler 与 Cleanup 批次对受影响 Inbox 调 `hub.NotifyGone`。
- 一个部署只有一个 `serve` 进程写入投递（AGENTS §2 Advisory Lease Singleton），进程内 Hub 可证明覆盖全部投递事件；因此不需要 LISTEN/NOTIFY、Redis、新表或事件持久化。
- Hub 自身不拥有 Goroutine：SSE 与长轮询都在各自 HTTP 请求 Goroutine 内 select，Owner 是 `http.Server`，取消路径是 `r.Context()`。
- Shutdown：应用先关闭 Hub（close 全部 subscriber），SSE 与长轮询 Handler 数拍内返回——长轮询 waiter 做最后一次领取尝试后按结果返回 200/204，不吊死优雅停机；随后 `http.Server.Shutdown` 正常收敛，不等待 max-age。两通道均无持久状态，进程强杀后客户端重连重拉即可，无恢复逻辑。

### 有界参数与 Admission

新增 7 个配置（`MAILWISP_` 前缀，`internal/config` 启动时类型化校验，时间使用 `time.Duration`）：

| 变量 | 默认 | 校验 |
| --- | --- | --- |
| `MAILWISP_HTTP_SSE_MAX_CONNECTIONS` | 200 | >0 |
| `MAILWISP_HTTP_SSE_MAX_PER_INBOX` | 2 | >0 且 ≤ 全局上限 |
| `MAILWISP_HTTP_SSE_HEARTBEAT` | 25s | >0 |
| `MAILWISP_HTTP_SSE_MAX_AGE` | 15m | >0 |
| `MAILWISP_HTTP_LONGPOLL_MAX_WAIT` | 30s | 0..30s；0 即部署者关闭等待语义（`wait>0` 一律 400），即查即走仍可用 |
| `MAILWISP_HTTP_LONGPOLL_MAX_WAITERS` | 256 | >0 |
| `MAILWISP_HTTP_LONGPOLL_MAX_WAITERS_PER_INBOX` | 4 | >0 且 ≤ 全局上限 |

- 心跳 25 秒的依据：低于常见 30 秒中间层空闲下限与 JMAP 互操作 ping 钳制下限，在 Nginx 对应 location 的 90 秒超时内有 3 拍余量（Mercure 默认 40 秒作对照）。
- Admission 在认证之后、注册订阅或 waiter 之前判定：超全局或超单 Inbox 上限返回 `503` + `Retry-After: 5`，稳定码 SSE 用 `realtime_saturated`、长轮询用 `longpoll_saturated`（区别于 429 的身份限速语义）；响应不泄露当前连接数。
- `wait=0` 请求不注册 waiter、不占槽位，饱和时即查即走路径仍可用；长轮询被拒的客户端应退化为 `wait=0` 轮询，浏览器超出单 Inbox 上限的标签退回列表轮询。
- 单 Capability 持有者最多占 2 个 SSE 连接加 4 个 waiter 槽位，无法独占全局池。

### 平台与部署细节

- 两个 Handler 都必须用 `http.ResponseController` 按连接覆盖全局 WriteTimeout=15s（`internal/config/config.go:167`），否则 SSE 流在 15 秒静默死亡、`wait=30` 长轮询写响应失败；`ReadHeaderTimeout 5s` 的 Slowloris 防护不受影响。必须有存活超过 15 秒的连接 Integration Test 兜底。
- `deploy/compose/nginx/default.conf.template`（`deploy/reference` 同步）追加两个精确匹配 location（精确匹配优先于既有 regex location，现有 30 秒通用超时不变）：
  - `location = /api/v1/inboxes/me/events`：通用四个 proxy header + `proxy_http_version 1.1` + `proxy_set_header Connection ""` + `proxy_buffering off` + `proxy_cache off` + `gzip off` + `proxy_read_timeout 90s` + `proxy_send_timeout 90s`；
  - `location = /api/v1/inboxes/me/messages/next`：通用 proxy header + `proxy_read_timeout 45s`（45 = wait 上限 30 + 服务端余量）。
- 应用层始终返回 `X-Accel-Buffering: no` 作为双保险：Nginx 按响应粒度关缓冲，并兼顾用户自带的前置反代。
- 可选防护：`limit_conn_zone $binary_remote_addr zone=mailwisp_sse:10m` 并在 events location 加 `limit_conn mailwisp_sse 8`，兜住认证前连接洪泛。
- Compose 服务拓扑零变更：无新容器、无新卷、无新端口。Host-native 辅助 Profile 直连 `:8080` 为 HTTP/1.1 明文时受浏览器同域 6 连接上限影响，必须在部署文档单独警示（Compose 主路径 `http2 on` 无此问题）。

### 前端集成（web/src/useMailbox.ts）

- 进入 Inbox 态后 `new EventSource('/api/v1/inboxes/me/events')`，同源自动携带 `__Host-` Cookie，无需 `withCredentials`。
- `onopen`：立即刷新消息列表（补课），并把安全轮询从 10 秒放宽到 60 秒——轮询兜底永不删除，这是「丢事件不丢邮件」的浏览器端保证。
- `message-new`：250ms 防抖合并连发后重拉 Cursor 第一页，按 Message ID 去重前置（ADR 0020 已实现该合并语义）。
- 看门狗：90 秒内无任何事件（含 `ping`）即关流重建并立即刷新。
- `error` 且连接进入 CLOSED（如 admission 503）：恢复 10 秒轮询；SSE 重试使用指数退避，5 秒起步、30 秒封顶、全抖动。
- `inbox-gone`：关流并走既有 Inbox 失效 UI 流程；组件卸载时 `close()`。

### Metrics 与日志（遵守 ADR 0014 低基数）

- `mailwisp_sse_connections`（Gauge）、`mailwisp_sse_events_total{type=message-new|ping|inbox-gone}`、`mailwisp_sse_rejected_total{reason=global|inbox}`、`mailwisp_sse_disconnects_total{reason=client|max_age|slow_write|shutdown|gone}`；
- `mailwisp_longpoll_waiters`（Gauge）、`mailwisp_longpoll_requests_total{outcome=claimed|empty|saturated}`。
- 禁止 Inbox 地址、Message ID、Cursor 进 Label；连接建立与关闭仅 Debug 级日志携带 Request ID；SSE 帧内容不进日志。

### 兼容后续路径

`docs/compatibility/yyds.md` 当前把 `/messages/next` 列为 Unsupported。Canonical 端点落地后，`/compat/yyds/v1` 可按上游 GET 契约以独立 Presenter 投影同一 Application Service 翻转为 Supported；上游 `MessageDetail.verificationCode` 在 MailWisp 具备 OTP 提取能力（另行决策）之前缺失，翻转时必须写入 Partially Supported，禁止伪装兼容（AGENTS §1/§11）。该翻转连同 Fixture 与 Contract Test 更新是独立后续 Change，不属于本 ADR 的交付边界。`mailwisp mcp` 的等待工具（档案第 6 章）同样以本端点为唯一服务端地基，不需要额外服务端能力。

## 安全与失败语义

- `seen_at` 双重语义必须在 API 文档明示：`/next` 领取即标已读，浏览器列表会看到该邮件变为已读——这是防重复消费的代价，与 YYDS 语义一致；Temporary Capability 无恢复未读能力。
- Postfix 重投产生的多条 Message 行（不同 UUID）会被各领取一次、SSE 各发一次事件；客户端按 `message_id` 去重，属既定 Live Listing 语义（ADR 0020）。
- 唤醒丢失（cap=1 合并丢弃）不影响正确性：长轮询每次醒来都重查数据库，SSE 侧有 60 秒安全轮询与 90 秒看门狗，两侧均不依赖事件完备性——丢事件最多加延迟，绝不丢邮件。
- 事件载荷最小化：`message-new` 只含 `message_id` 与 `received_at`，前置代理日志、浏览器扩展或内存转储都拿不到邮件内容；UUID 不是 Secret，泄露不越过 Inbox Ownership（与 ADR 0020 Cursor 安全模型同构）。`ping` 不含任何服务端状态指纹。
- 撤销与生命周期：连接不缓存授权决定超过必要范围——关闭死线取 `min(Session 到期, Inbox 到期, max-age)`，Inbox 删除即广播 `inbox-gone` 关流，符合「删除 Inbox 是全部访问权的权威失效点」的披露边界。
- 资源耗尽防护：认证后 Admission 限定 FD 与 Goroutine 总量；认证前由 Nginx `limit_conn` 兜底；每写 10 秒 deadline 清理只连不读的死连接；max-age 防僵尸连接。
- 部署重启的重连风暴由 `retry: 5000`、UA 自带退避、前端全抖动封顶 30 秒与 admission 503 背压共同吸收；被拒客户端退回轮询而非热循环重试。
- 默认拒绝：未认证统一 401；503 不泄露连接计数；`/metrics` 在 Nginx 公网继续 404；不开任何新端口。
- 路由遮蔽：`/messages/next` 是字面段，依赖 Go 1.22+ ServeMux 字面段优先于 `{id}` 通配的规则，必须有路由测试钉死，防止未来更换 Router 时把 `next` 当 UUID 解析。

## 暂不采用/明确不做

- 不做 WebSocket：无双向交互需求，连接规模未证明收益（AGENTS 既定；横纵报告 §4.2）。
- 不在事件里携带全量邮件数据：拒绝 Mailpit 路线——多租户 Capability 场景扩大内容泄露面并造成客户端状态分叉。
- 不实现 Last-Event-ID 重放与事件历史存储：Mercure 需要 BoltDB 有界历史才成立，Cursor 列表重拉已是等价补课通道。
- 不用 LISTEN/NOTIFY、Redis 或新表做事件分发：单 `serve` 进程内 Hub 已可证明完备。
- 不给 `/messages/next` 开 Cookie 认证：副作用端点接受 Cookie 会引入 CSRF 面，Bearer-only 从根上消除；也保持「自动化用 next、浏览器用 SSE」的通道分工。
- 不新增 Migration 与索引：`seen_at` 与 `messages_inbox_received_idx` 已覆盖；仅当基准证明退化时按新增单调 Migration 演进部分索引。
- 不删除前端轮询兜底；SSE 必须由生产 E2E 证明真实 `message-new` 到达后才能宣称实时（DuckMail 教训）。
- 不扩充事件集（message-deleted、stats 等）：删除由下次列表刷新自然收敛（YAGNI）。
- 不做跨 Inbox 领取：Capability 严格单 Inbox 绑定；未来 PAT 出现多 Inbox Principal 时需新的 Scope 设计与新 ADR。
- 不实现 JMAP `closeafter=state` 降级模式：记为观察项；企业代理吞流场景由看门狗退化到轮询兜住。
- 单 Inbox 第 3 个 SSE 连接采用拒绝（503）而非踢掉最旧连接：避免跨连接协调复杂度，等真实多标签反馈再议。

## 验证要求

- PostgreSQL Integration：并发多个 `/next` 对同一 Inbox 领取互斥（恰一个 200，其余窗口后 204）；`wait` 窗口内投递被唤醒 1 秒内返回；`wait=0` 即查即走；Inbox 过期、删除竞态按统一失败语义返回，不伪装 204；持久 Inbox（`expires_at IS NULL`）可正常领取。
- 连接存活 Integration：SSE 与 `wait=30` 长轮询存活超过 15 秒，证明 ResponseController 覆盖全局 WriteTimeout 生效。
- Admission：超全局与超单 Inbox 上限分别返回 503 + `Retry-After`，稳定码正确；`wait=0` 不受 waiter 上限影响。
- 参数与协议：`wait` 非整数、越域、超配置上限均 400 `invalid_wait`；`MAILWISP_HTTP_LONGPOLL_MAX_WAIT=0` 时 `wait>0` 被拒；204 响应无 Body；SSE 响应头与 `retry`、三种事件帧形状逐字段断言。
- 路由测试：`POST /messages/next` 与 `/messages/{id}` 无遮蔽。
- Shutdown：关闭 Hub 后全部流与 waiter 数秒内收敛，`http.Server.Shutdown` 不等待 max-age；`go test -race` 全绿。
- 生产 E2E（`scripts/` 既有闭环扩展）：经 Postfix→LMTP 投递真实邮件，断言 SSE 在 90 秒内收到 `message-new`；并发两个 `/next` 恰一个领取；全链经 Compose Nginx 验证缓冲关闭与超时配置生效。
- 前端 Playwright：SSE 刷新合并与去重、看门狗重建、503 降级回轮询、`inbox-gone` 走失效流程、组件卸载关流。
- Metrics：名称、固定 Label 与低基数断言（ADR 0014 模式）；Nginx 公网 `/metrics` 仍 404。
- 文档同步：OPERATIONS 记录新 Nginx location 与调参指引；API 文档写明 204 语义与已读副作用；Host-native HTTP/1.1 连接上限警示；`docs/compatibility/yyds.md` 的翻转留待后续 Change 并保持 Partially Supported 如实披露。