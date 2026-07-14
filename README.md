# MailWisp

> Fast mail. Zero trace.
> 来信即现，过时即逝。

MailWisp 是为单台个人服务器设计的临时邮箱服务，目标是低空闲占用、可预测的低延迟、有界高并发，以及简单可靠的恢复流程。

项目正在以生产级Go模块化单体重新实现。旧TempMail项目保留在本仓库之外，在数据迁移与行为兼容得到验证前只作为只读参考。

当前处于Research-first阶段：先重新验证架构、工具链、固定版本策略与第三方API兼容性，再进入业务实现。任何现有架构判断都可以被更强证据推翻。

## 当前进度

新应用骨架已经包含：

- 类型化环境配置；
- 结构化JSON日志；
- 优雅关闭；
- 独立的Liveness与Readiness接口；
- 具有生产级Timeout的标准库HTTP Server；
- 完整工程、权限和Git协作规范。
- 中英文与Design Token主题系统要求；
- Cloudflare Temp Email、215.im与DuckMail兼容层研究计划。

## 本地验证

```powershell
./scripts/verify.ps1
```

也可以分别执行Go检查：

```text
gofmt -w .
go test ./...
go test -race ./...
go vet ./...
govulncheck ./...
```

## 配置

所有环境变量统一使用 `MAILWISP_` 前缀。安全示例参见 `.env.example`。
