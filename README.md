# MailWisp

> Fast mail. Zero trace.
> 来信即现，过时即逝。

MailWisp 是面向自托管场景的生产级临时邮箱服务，目标是高质量、安全、低延迟、有界高并发和可靠恢复。Docker Compose是单台Linux服务器的主推荐Reference Deployment，Host-native为辅助Profile；项目不会为了适配现有机器规格牺牲工程与用户体验质量。

项目正在以生产级Go模块化单体重新实现。旧TempMail项目保留在本仓库之外，在数据迁移与行为兼容得到验证前只作为只读参考。

项目已经从Research-first进入核心实现阶段。任何架构判断仍可以被更强证据推翻，但不会用Scaffold或文档代替真实链路。

## 当前进度

当前已经实现：

- 类型化环境配置；
- 结构化JSON日志；
- 优雅关闭；
- 独立的Liveness与Readiness接口；
- 具有生产级Timeout的标准库HTTP Server；
- 完整工程、权限和Git协作规范；
- 中英文与Design Token主题系统要求；
- Cloudflare Temp Email、215.im与DuckMail兼容边界研究；
- SHA-256内容寻址Raw Message Store，包含流式限额、`fsync`、并发去重、校验和Staging恢复；
- PostgreSQL 18 Migration、Advisory Lock、UUIDv7 Inbox/Message与多Recipient事务；
- 有界LMTP Server，支持逐Recipient状态、Dot unstuffing、SIZE、背压和Graceful Shutdown；
- TCP LMTP → Content Store → PostgreSQL 18.4真实Integration Test；
- 有界Content Reconciliation与独占维护锁；
- Write、Fsync、Hard Link及DB Commit前后的真实进程强杀恢复测试；
- 严格校验的一致性Backup Bundle、空目标Restore与PostgreSQL 18.4官方工具适配；
- 固定Postfix 3.11.5真实SMTP/LMTP Integration，覆盖持久队列、进程重启、4xx重投、确认丢失重复投递和未知Recipient永久失败；
- 纯Go有界流式MIME Parser，覆盖Raw、Header、Part、Depth、Decoded Bytes、正文预览和附件Metadata边界；
- Content级持久Parser Queue与有界Worker，覆盖`SKIP LOCKED`领取、Fenced Lease、重试、流式Digest校验和解析结果原子持久化；
- Canonical Opaque Token Grammar与Inbox Capability认证底座，覆盖Digest-only持久化、Scope、到期、撤销和原子Rotation。
- Canonical `/api/v1`匿名Inbox闭环，覆盖随机地址、一次性Capability签发、Ownership、消息读删、统一Error Envelope、Request ID和可信Proxy限流。
- 可关闭的`/compat/duckmail` Adapter，覆盖Address/Password、Argon2id、Hydra分页、Seen、Raw Source和独立错误Envelope。
- `web/`正式Vue 3控制台首个生产切片，覆盖真实API Client、中文默认/英文切换、Light/Dark/System/Mist、令牌一次性保存、轮询、邮件详情与Sandbox HTML。
- Capability到AES-256-GCM HttpOnly Browser Session的安全交换，覆盖短生命周期、`__Host-` Cookie与状态修改请求CSRF验证。
- 有界Retention Cleanup，使用短事务、`SKIP LOCKED`与PostgreSQL Advisory Lock清理过期Inbox及失去最后引用的Raw MIME。
- 单机Linux Host-native Reference Profile，固定Nginx、Certbot、Postfix、PostgreSQL与systemd运行边界，并提供可复现Linux amd64 Release Bundle构建。
- Docker Compose主推荐Profile，固定生产镜像Digest、Secret、Healthcheck、Migration依赖顺序、内部Network、资源上限与证书共享。
- Canonical附件下载API，按拥有权校验后从Raw MIME按PartPath有界流式解码，控制台支持Bearer/HttpOnly Session下载。
- 内部低基数Prometheus Metrics，覆盖HTTP、LMTP、Parser、Retention与PostgreSQL Pool；Nginx公网明确拒绝`/metrics`。

备份恢复、Postfix持久队列、纯Go有界MIME Parser、Content级Parser Worker、Inbox Capability、Canonical API、DuckMail Adapter与正式Vue控制台均已通过对应GitHub Actions固定门禁。Reference Profile仍需在真实目标Linux完成ACME、SMTP STARTTLS、备份恢复和断电演练；旧数据迁移及尚未声明支持的第三方API也必须按Contract逐项验证，不能用本地构建通过替代生产验收。

## 本地验证

```powershell
./scripts/verify.ps1
```

完整门禁要求Docker Engine以及官方`pg_dump`/`pg_restore` 18.4客户端可用，并会启动固定Digest的PostgreSQL 18.4临时容器。
门禁还会构建固定Alpine Digest与Postfix `3.11.5-r0`的Integration Image，在真实SMTP/LMTP链路验证持久Queue和重投，并在固定Linux/amd64 Go 1.26.5 Bookworm镜像中重复执行Test与Race。
GitHub Actions使用固定Commit SHA的Docker Buildx复用可审查Build Cache；Postfix测试失败时会上传Queue、有效配置和Container Log Artifact。govulncheck、gosec和Gitleaks同样固定版本执行。

也可以分别执行Go检查：

```text
gofmt -w .
go test ./...
go test -race ./...
go vet ./...
govulncheck ./...
```

## 本地启动

准备PostgreSQL并复制 `.env.example` 中的配置到本地环境后，先执行Migration：

```powershell
go run ./cmd/mailwisp migrate
```

再启动组合Role：

```powershell
go run ./cmd/mailwisp serve
```

内容一致性检查必须在MailWisp服务停止后执行。普通检查只报告问题：

```powershell
go run ./cmd/mailwisp reconcile
```

确认需要清理无数据库引用的对象时，显式启用Orphan修复：

```powershell
go run ./cmd/mailwisp reconcile --repair-orphans
```

`reconcile`通过PostgreSQL独占维护锁阻止并发收件，扫描过程使用有界批次。Orphan可以安全删除；Missing或Corrupt不会自动删除数据库记录，命令会以非零状态退出，等待人工恢复Content或从备份修复。

手动执行一次完整的过期Inbox清理：

```powershell
go run ./cmd/mailwisp cleanup
```

每个数据库事务最多处理`MAILWISP_CLEANUP_BATCH_SIZE`个Inbox；Reference Profile通过systemd Timer每五分钟触发一次命令。

创建一致性Backup Bundle时，服务必须已经停止；命令会再次通过独占维护锁验证，并在备份前要求Content Reconciliation为零异常：

```powershell
go run ./cmd/mailwisp backup ./backups/mailwisp-20260715
```

恢复只允许目标PostgreSQL数据库为空且`MAILWISP_CONTENT_ROOT`不存在：

```powershell
go run ./cmd/mailwisp restore ./backups/mailwisp-20260715
```

Bundle固定包含`manifest.json`、`database.dump`和`content.tar.gz`。恢复会在任何写入前验证Manifest、文件大小与SHA-256，并在完成后运行跨数据库/Content Store一致性检查。

默认监听：

- HTTP：`:8080`
- LMTP：`127.0.0.1:2525`

## Canonical API

匿名创建Inbox：

```http
POST /api/v1/inboxes
Content-Type: application/json

{"domain":"mailwisp.example.com","ttl_seconds":86400}
```

响应仅在创建时返回一次完整Capability。后续请求必须使用Header，禁止把Token放入URL：

```http
Authorization: Bearer wisp_cap_v1_<kid>_<secret>
```

当前Canonical路由：

- `POST /api/v1/inboxes`
- `GET /api/v1/inboxes/me`
- `DELETE /api/v1/inboxes/me`
- `GET /api/v1/inboxes/me/messages?limit=50`
- `GET /api/v1/inboxes/me/messages/{id}`
- `GET /api/v1/inboxes/me/messages/{id}/attachments/{part_path}`
- `DELETE /api/v1/inboxes/me/messages/{id}`

浏览器控制台在服务器配置`MAILWISP_BROWSER_SESSION_KEY`后使用：

- `POST /api/v1/session`：用Authorization Header中的Capability交换HttpOnly Session；
- `GET /api/v1/session`：刷新页面后恢复当前Inbox；
- `DELETE /api/v1/session`：携带`X-MailWisp-CSRF`退出会话。

Session不包含Capability明文，最长不超过原Capability到期时间。未配置Session Key时这些路由保持关闭，CLI与自动化仍可直接使用Canonical Bearer API。

DuckMail Adapter默认关闭。设置`MAILWISP_DUCKMAIL_ENABLED=true`后，在`/compat/duckmail`命名空间提供兼容路由；支持范围与明确差异见[DuckMail兼容边界](docs/compatibility/duckmail.md)。永久匿名Account和根路径伪装不会启用。

YYDS Adapter默认关闭。设置`MAILWISP_YYDS_ENABLED=true`后，在`/compat/yyds/v1`提供Passwordless Temporary Inbox核心Contract；JWT、`AC-` API Key、Webhook与WebSocket不会被虚假声明为已兼容，详见[YYDS兼容边界](docs/compatibility/yyds.md)。

所有错误使用包含稳定`code`、可读`message`与`request_id`的JSON Envelope。Capability只能访问自身Inbox，删除最后一条内容引用时会同步清理Raw MIME；异常残留由Content Reconciliation兜底。

当前`main`已用真实Integration覆盖Postfix与Go LMTP的可靠队列边界；`deploy/postfix-test`是隔离测试资源，不是生产配置。公网域名、TLS、反滥用、DNS和生产资源限制完成前，不应直接宣称生产SMTP部署完成。

主推荐安装路径见[Docker Compose Deployment](deploy/compose/README.md)；需要systemd深度集成时使用[Host-native辅助Profile](deploy/reference/README.md)。本地构建固定版本的Linux amd64发布包：

```powershell
./scripts/build-release.ps1
```

## 配置

所有环境变量统一使用 `MAILWISP_` 前缀。安全示例参见 `.env.example`。
