# MailWisp

> Fast mail. Zero trace.
> 来信即现，过时即逝。

MailWisp 是面向自托管场景的生产级临时邮箱服务，目标是高质量、安全、低延迟、有界高并发和可靠恢复。单台Linux服务器是Reference Deployment Profile，但项目不会为了适配现有机器规格牺牲工程与用户体验质量。

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
- TCP LMTP → Content Store → PostgreSQL 18.4真实Integration Test。

仍未完成Postfix真实队列重试、断电故障注入、旧数据迁移、邮件解析、业务API和正式Vue控制台，不能把当前阶段当作可发布产品。

## 本地验证

```powershell
./scripts/verify.ps1
```

完整门禁要求Docker Engine可用，并会启动固定Digest的PostgreSQL 18.4临时容器。
门禁还会在固定Linux/amd64 Go 1.26.5 Bookworm镜像中重复执行Test与Race，并验证固定版本的govulncheck、gosec和Gitleaks。

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

默认监听：

- HTTP：`:8080`
- LMTP：`127.0.0.1:2525`

Postfix接入配置尚未定稿，当前LMTP入口用于协议与持久化闭环验证，不应直接宣称生产部署完成。

## 配置

所有环境变量统一使用 `MAILWISP_` 前缀。安全示例参见 `.env.example`。
