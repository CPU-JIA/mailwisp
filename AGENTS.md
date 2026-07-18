# MailWisp 开发与协作规范

本文件适用于整个仓库。它只保留当前有效的工程约束；架构论证、历史候选和验收证据分别以已接受 ADR、版本锁及测试为准。

## 1. 产品与边界

MailWisp 是面向自托管个人服务器的生产级临时邮箱服务。

> Fast mail. Zero trace.
> 来信即现，过时即逝。

优先级依次为：正确性与数据安全、安全与可维护性、完整用户体验、低延迟、有界高并发、资源效率、部署与恢复简单可靠。

- 后端只使用 Go，不引入 Rust。
- MailWisp 是独立新产品，不读取、迁移、复用或修改 `../tempmail` 及其代码、数据、配置和 Secret。
- DuckMail、YYDS、Cloudflare Temp Email 仅是可关闭的外部 API Adapter，不构成旧产品继承关系。
- 未明确支持的能力必须写入 Compatibility 的 Partially Supported 或 Unsupported，禁止伪装兼容。

## 2. 当前架构事实

Canonical Reference Profile 是单台 Linux 服务器上的模块化单体：

```text
Nginx -> Vue 静态控制台 / Go HTTP API
Postfix -> LMTP -> Go Application
Go Application -> PostgreSQL 18 + Local Content Store
```

- Docker Compose 是唯一主推荐部署方式；Host-native 只作辅助 Profile。
- PostgreSQL 是结构化状态的唯一事实源；Raw MIME 存入内容寻址文件系统。
- 一个部署只允许一个 Go `serve` 进程。PostgreSQL Advisory Lease 强制 Singleton；高并发在进程内通过有界 Admission、Worker 和 Backpressure 实现。
- 不使用 Redis、Broker、PgBouncer、Kubernetes、Service Mesh 或内部 RPC。只有真实需求、测量和 ADR 同时成立时才能引入。
- 公网只开放 Nginx HTTP(S) 与 Postfix SMTP；PostgreSQL、LMTP、Metrics 和内部网络不得直接暴露。
- 架构变化必须更新 ADR、故障模型、部署、测试和恢复 Contract。

## 3. 授权与安全

- 用户已授权本仓库内的日常研发闭环：调研、分支、实现、测试、Commit、Push、PR、Review 修复、Actions、合并和任务分支清理。
- 该授权不包含生产部署、DNS、真实 Secret、仓库可见性、计费、权限、Branch Protection、Deploy Key 或外部消息。
- 不使用 `git reset --hard`、Force Push、`--no-verify` 或破坏性 Checkout 覆盖他人改动。
- 不跳过测试、扫描、迁移、恢复、Review 或失败检查；工具不可用时只能标记“未验证”。
- 不记录或提交密码、Token、Authorization Header、DSN、完整邮件内容、生产配置或用户数据。
- 新远程仓库默认 Private；未经授权不得改变远程治理设置。

## 4. 语言与命名

- 用户沟通、README、ADR、PR、运维说明默认中文；协议名、代码标识符和专业术语使用惯用英文。
- Commit 使用 Conventional Commits 英文类型前缀和中文主题，例如 `fix: 修复内容删除重试竞态`。
- Go Package 简短、小写、单数；禁止 `utils`、`helpers`、`common`、`core`、`misc` 等垃圾桶名称。
- 环境变量统一使用 `MAILWISP_` 前缀；数据库使用 `snake_case`；JSON 遵循所属 Contract。
- 时间优先使用 `time.Duration`；字节和数量限制必须在名称中表达单位或语义。

## 5. 目录与依赖

```text
cmd/                   可执行入口与参数解析
internal/app/          Composition Root 与生命周期
internal/config/       类型化配置和启动校验
internal/httpapi/      Canonical 与兼容 HTTP Transport
internal/lmtp/         有界 LMTP 协议入口
internal/mail/         MIME 解析
internal/auth/         Token、Capability、Session、Scope
internal/mailbox/      Inbox 与消息应用语义
internal/message/      投递领域与 Raw Content 引用
internal/contentstore/ 文件内容存储、恢复、校验
internal/jobs/         Parser 与 Retention Job
internal/postgres/     pgx Repository、Migration、维护租约
internal/telemetry/    日志与低基数 Metrics
migrations/            不可变 Goose SQL Migration
web/                   Vue 生产控制台
deploy/compose/        Canonical 部署与运维
deploy/reference/      Host-native 辅助 Profile
scripts/               可复现门禁、Release、E2E、DR、Benchmark
docs/decisions/        已接受架构决策
```

- 领域层不得依赖 HTTP、PostgreSQL、Docker 或前端。
- Transport 只做认证、校验、映射和响应，不写 SQL。
- Infrastructure 实现消费方定义的 Interface；`cmd/` 与 `internal/app/` 负责组装。
- 只在职责和重复复杂度真实存在时增加抽象或目录。

## 6. Go 规则

- 使用 `go.mod`、`.go-version` 和版本锁声明的同一 Go 版本；优先标准库。
- I/O 和请求操作以 `context.Context` 为首参，不把 Context 存入长生命周期 Struct。
- Goroutine 必须有 Owner、取消、错误回传与关闭路径；禁止无人负责的后台任务。
- 预期错误不用 `panic`；使用 `%w`、`errors.Is`/`errors.As` 和稳定领域错误。
- Constructor 显式依赖，不产生隐藏网络或持久化副作用。
- Interface 定义在消费方，不为 Mock 制造空抽象。
- 导出声明有有效 Go Doc；注释解释不变量，不复述语法。
- 任何共享状态都必须证明并发语义，并通过 Race Test。

## 7. 数据与生命周期

- 使用 `pgx/v5` 和参数化 SQL，不引入 ORM。
- Transaction 短小明确；Transaction 内不做文件、DNS 或外部网络 I/O。
- List 必须有确定排序与有界 Pagination；Index 必须对应实际 Query Pattern。
- 已进入共享历史的 Migration 不可修改，只能新增单调版本。
- 运行二进制要求数据库 Schema 精确等于 `migrations.LatestVersion`。
- 破坏性变更必须有备份恢复与前一受支持版本验证。
- Inbox 永久到期被禁止；删除数据库引用时必须原子生成持久 Content Deletion Task。
- 文件删除失败必须可重试；删除前在生命周期 Fence 内重新核对数据库引用，并用 Generation 防止旧确认吞掉新任务。
- Reconcile、Backup、Restore 和一次性 Cleanup 必须取得独占维护租约。

## 8. SMTP、LMTP 与 MIME

- Postfix 负责公网 SMTP、持久 Queue、TLS 和重投；Go 只接收内部 LMTP。
- 未知 Recipient 在 SMTP RCPT 阶段拒绝，避免 Backscatter。
- Durable Content 和全部 Recipient 元数据提交前不得确认 LMTP 成功。
- 所有 Session、Recipient、Message Bytes、Header、MIME Depth、Part、Decoded Bytes、附件和 Parser Worker 必须有上限。
- 过载返回临时错误让 Postfix 重试；永久业务错误使用明确 5xx。
- Header、Body、HTML、URL、Filename 和 MIME 元数据全部视为不可信输入。
- Raw Source 始终可按 Ownership 下载；解析失败是可检查的终态，不得伪装为持续处理中。

## 9. HTTP、认证与隐私

- Canonical API 使用 `/api/v1`，统一 Error Envelope、稳定 Code 和 Request ID。
- `/livez` 只表示进程存活；`/readyz` 用短 Deadline 验证必要依赖与精确 Schema。
- Body、Header、Query、Pagination、下载和高成本读取都有硬上限或并发 Admission。
- 内部错误记录根因，不返回客户端；日志不得包含 Token、Query Credential 或完整邮件。
- Canonical Token 遵循 ADR 0005：`wisp_<type>_v1_<kid>_<secret>`，熵不少于 256 bit，明文只展示一次，数据库只保存 Domain-separated Digest。
- Authorization 默认拒绝；每个对象检查 Inbox Ownership 和 Scope。
- 浏览器使用 Secure、HttpOnly、SameSite `__Host-` Cookie 与内存 CSRF Proof；Session 不得写入 Local Storage。
- Stateless Session 的撤销边界必须在安全文档中如实披露；删除 Inbox 才是全部访问权的权威失效点。
- Metrics Label 必须低基数，不使用邮箱、Token、Message ID、Content Key 或任意用户输入。

## 10. 前端

- 正式前端位于 `web/`，使用固定版本 Vue、TypeScript、Vite 与 vue-i18n；生产不运行 Node.js。
- 默认中文并支持英文；支持 System、Light、Dark 和经过完整状态验证的扩展主题。
- Design Token、响应式布局、键盘操作、ARIA、Loading/Empty/Error/Retry 状态均为发布要求。
- 不可信邮件 HTML 只能经 Sanitization 后进入无凭据、强 Sandbox iframe；默认阻止远程图片。
- 不使用 `innerHTML` 注入未净化内容，不把 Capability 放入 URL、Storage 或可读 Cookie。
- UI 调用必须覆盖 Session 恢复、退出失败、Pagination 竞态、解析失败、Raw Source、附件和删除确认。

## 11. Adapter Contract

- Canonical Domain Model 不复制第三方结构；Adapter 只投影 Path、Auth、Field、Status 和 Error Envelope。
- 每个 Adapter 必须有固定 Commit/版本或内容 SHA-256 的一手 Contract Fixture。
- Contract Test 逐项固定来源身份、Endpoint、Envelope、Authentication、分页与安全上限，不能只检查文件存在或数组长度。
- 版本升级必须人工审查上游 Diff，并同时更新 Fixture、测试和 Compatibility 文档。
- 安全冲突优先保持 MailWisp 边界，例如拒绝 Query Token、永久匿名邮箱和不可撤销 JWT。

## 12. 配置、部署与 Release

- 配置只在启动时类型化加载和校验；一个语义只有一个 Canonical 变量。
- Secret 无默认值，通过 Compose Secret 文件注入；示例只能使用不会命中 Secret Scanner 的 Placeholder。
- 版本固定在 `go.mod`、Lockfile、Docker Digest、`versions.lock` 和 Action Commit SHA；禁止 `latest` 与浮动 Major。
- Runtime 尽可能 Non-root、`no-new-privileges`、只读文件系统、最小 Capability、内部 Network 和有界日志。
- Compose 必须先由幂等 `db-provision` 收敛 Runtime Role 及已有对象权限，再由 Owner 执行 Migration；不得依赖只在空 Volume 运行一次的 `docker-entrypoint-initdb.d` 承担升级。
- Compose 用户部署前必须通过精确版本 Preflight、配置渲染、Migration、Healthcheck 和备份基线。
- Release 从干净 Checkout 运行完整门禁、可复现双构建、SBOM、Trivy、Checksum 和 Production E2E。
- 正式 Tag 必须属于 `main`、满足 SemVer、具有中文 Release Notes 和 Attestation；平台不支持时 Fail Closed，不降级发布。

## 13. 测试门禁

新行为与回归测试必须在同一 Change。验证范围与风险相称，窄测不能证明全仓完成。

Go 基础门禁：

```text
gofmt
go test ./...
go test -race ./...
go vet ./...
govulncheck ./...
```

此外：

- Parser、Token、Cursor 等攻击面运行固定时长 Fuzz。
- PostgreSQL、Crash、Postfix 使用真实 Integration Test，不以 Mock 替代协议证据。
- 前端运行 Type Check、Oxlint、Unit、Production Build 和 Playwright。
- 部署运行 Compose Render、Image Build、Production E2E、Backup Verify、原卷删除后的 DR 与零残留检查。
- 安全运行 Gosec、Gitleaks 工作树与历史、GitGuardian、npm audit、Trivy Image/IaC。
- `scripts/verify.ps1` 是本地全量门禁入口；CI Workflow 不能弱于它覆盖的关键 Contract。

## 14. Git 与 PR 闭环

- 开始前确认 `main` 与 `origin/main` 同步、工作树状态和已有用户改动。
- 每个任务使用语义分支；Commit 原子、可审查，不混入无关格式化或生成物。
- Push 后创建 Draft PR，写清目标、风险、迁移、测试与回滚；检查全绿并完成 Review 后转 Ready。
- 禁止 Force Push；Base 漂移时正常合并或 Rebase，并重新运行受影响门禁。
- 合并默认使用 Squash Merge；合并后确认 `main` Push Workflow 全绿，再删除远程任务分支。
- 生产、DNS、真实 Secret 或平台权限变化即使代码完成也必须等待用户单独授权。

## 15. 完成定义

只有同时满足以下事实，任务才完成：

- 请求行为已真实实现，不是 Scaffold、文档承诺或 Mock 演示。
- Error、Cancellation、Overload、Crash、Shutdown、Migration、Deletion 和 Recovery 路径有证据。
- 代码、配置、示例、ADR、Compatibility 与运维文档一致。
- 相关 Unit、Race、Integration、E2E、安全和 Release 门禁全部通过。
- 工作树无 Secret、生产数据、临时容器、孤立 Volume 和未解释 Artifact。
- Commit、Push、PR、Actions、Review、Squash Merge 与 `main` 复验完整闭环。
- 所有操作均在授权范围内；外部平台限制必须准确披露，不得伪造完成。
