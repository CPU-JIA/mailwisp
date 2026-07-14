# MailWisp 开发与协作规范

本文件适用于整个仓库，是所有开发者和自动化 Agent 必须遵守的最高级别工程约束。子目录可以通过更具体的 `AGENTS.md` 增加规则，但不得削弱、绕过或与本文件冲突。

## 1. 核心原则

MailWisp 是面向单台个人 Linux 服务器的生产级临时邮箱服务。

产品目标按优先级排序：

1. 正确性与数据安全。
2. 低空闲资源占用。
3. 可预测的低延迟。
4. 通过有界并发与背压实现高并发。
5. 部署、恢复与维护简单可靠。
6. 一名维护者也能长期理解和演进代码。

品牌文案：

> Fast mail. Zero trace.
> 来信即现，过时即逝。

MailWisp 采用模块化单体架构，不为假设性的规模引入分布式系统复杂度。

## 2. 不跳过、不越权

这是仓库的强制规则，没有“临时先跳过”的默认例外。

### 不跳过

- 不跳过需求核对、现状检查、备份检查、迁移验证、测试、静态分析、安全扫描、恢复验证和Git审查。
- 不得使用 `--no-verify` 绕过Git Hook。
- 不得为了让CI变绿而删除、禁用、跳过或弱化测试。
- 不得把失败检查描述为通过，也不得以“没有发现问题”代替覆盖充分的验证。
- 不得在未完成数据恢复演练前声称迁移安全。
- 不得在未完成端到端验证前声称SMTP收件链路可用。
- 工具不可用时必须明确记录“未验证”，并继续完成所有仍可执行的检查。

### 不越权

- 只修改用户明确授权的仓库、分支、文件、服务和环境。
- 默认只操作本地开发环境，不连接或修改生产服务器。
- 未经明确授权，不部署、不重启服务、不修改DNS、不开放端口、不轮换生产密钥、不发送外部消息。
- 未经明确授权，不创建公开仓库；新远程仓库默认使用 `private`。
- 未经明确授权，不修改远程仓库权限、Branch Protection、Secrets、Deploy Keys或组织设置。
- 不删除或覆盖用户已有提交、分支、标签、远程仓库和生产数据。
- 不使用 `git reset --hard`、强制覆盖Checkout或其他破坏性命令处理用户改动。
- 发现任务需要扩大权限或范围时，先停止相关动作并说明原因。

## 3. 默认语言

- 面向用户的交流、项目说明、ADR、Issue、PR描述、运维说明和普通注释默认使用中文。
- 代码标识符、Go Doc、API字段、协议名称、标准名称、行业术语和必须保持原文的专业文案使用行业惯用英文。
- Git提交格式使用 Conventional Commits 英文类型前缀，主题默认中文，例如：`feat: 增加LMTP会话解析器`。
- 不对已有标准术语进行生硬翻译，例如 HTTP、LMTP、Postfix、PostgreSQL、Redis、Backpressure、Context、Middleware。

## 4. 仓库边界

- 本仓库是全新的 MailWisp 实现。
- `../tempmail` 仅作为现有行为、数据库结构和数据兼容性的只读参考。
- 除非用户明确要求，不得编辑、删除、格式化、迁移或提交 `../tempmail` 中的任何文件。
- 旧实现只提供行为证据，不作为新架构骨架；禁止直接复制巨型旧文件。
- 所有现有生产数据必须通过明确、可测试的迁移兼容。
- 本地兼容性、备份恢复和回滚验证完成前，不得修改生产服务器。

## 5. 目标运行架构

本节是当前研究假设，不是不可修改的既定结论。正式业务实现前必须通过竞品、生产约束、基准测试、维护状态和故障模型研究重新验证。证据证明存在更优方案时，应通过ADR更新架构；收益不足的组件可以舍弃。

```text
Host Nginx -> 静态前端 / Go HTTP API
Postfix -> LMTP -> Go Application
Go Application -> PostgreSQL
Go Application -> Redis（仅限流和短期缓存）
```

架构不变量：

- 只有一个Go应用二进制。
- PostgreSQL是唯一持久化事实来源。
- Redis不得承载恢复用户数据所必需的唯一副本。
- Postfix负责公网SMTP兼容、持久队列和失败重试。
- 默认不使用PgBouncer，只有生产测量证明需要时才重新评估。
- 不引入Kafka、RabbitMQ、Kubernetes、Service Mesh或内部RPC层。
- 后台任务在进程内运行，必须支持取消、有界并发和必要的单实例协调。

## 6. 标准目录

```text
cmd/                 可执行程序入口，只做启动与依赖组装
internal/app/        应用组合与生命周期
internal/config/     类型化配置与校验
internal/httpapi/    HTTP传输与Middleware
internal/lmtp/       LMTP协议与有界收件入口
internal/mail/       MIME解析与邮件领域逻辑
internal/auth/       身份认证与权限控制
internal/account/    账户领域
internal/domain/     邮件域名领域
internal/mailbox/    邮箱领域
internal/message/    收件消息领域
internal/jobs/       定时维护任务
internal/postgres/   PostgreSQL适配器
internal/rediscache/ Redis适配器
internal/telemetry/  日志、健康检查与指标
migrations/          不可变的版本化SQL迁移
web/                 前端源代码与构建产物
deploy/              容器和宿主机部署资源
scripts/             可重复执行的开发与验证脚本
docs/decisions/      Architecture Decision Record
```

只有形成明确职责边界时才创建Package，禁止无意义增加目录层级。

## 7. Go工程规范

- 使用 `go.mod` 与构建镜像中声明的同一Go版本。
- 标准库清晰且足够时优先使用标准库。
- HTTP路由默认使用现代 `net/http` 路由模式，只有测量证明需要时才引入Router依赖。
- 使用 `log/slog` 输出结构化日志。
- 所有请求级和I/O操作以 `context.Context` 作为第一个参数。
- 不得把 `context.Context` 保存在长期存活的Struct中。
- 禁止启动无人负责的Goroutine。
- 每个Goroutine必须有Owner、取消路径和关闭行为。
- 预期内的运行错误不得使用 `panic`。
- 避免全局可变状态。
- 除不可避免的静态注册外，不使用 `init()`。
- Constructor必须显式且尽量不产生隐藏副作用。
- 在消费方定义Interface；不要只为Mock制造Interface。
- 接收Interface，能返回具体类型时返回具体类型。
- 使用 `%w` 包装错误，并补充操作上下文。
- 使用 `errors.Is` 和 `errors.As` 判断错误。
- Transport需要稳定映射时使用类型化或Sentinel领域错误。
- 注释解释设计意图和不变量，不复述语法。
- 所有导出声明必须提供有效Go Doc。

## 8. 命名规范

- Package名称必须简短、小写、单数且表达明确。
- 禁止使用 `utils`、`helpers`、`common`、`base`、`core`、`misc`、`manager` 等垃圾桶式Package名。
- 避免 `mailbox.MailboxServiceManager` 一类重复命名。
- 优先使用领域动作：`CreateMailbox`、`ReceiveMessage`、`RotateToken`。
- Boolean名称使用 `is`、`has`、`can`、`should` 或同等明确的谓词。
- 时间使用 `time.Duration`；无法使用时名称必须包含单位。
- 字节限制名称必须包含 `Bytes`。
- 环境变量统一使用 `MAILWISP_` 前缀。
- 数据库标识符使用小写 `snake_case`。
- JSON字段为兼容API使用小写 `snake_case`。
- HTTP路径使用复数资源名；只有普通CRUD无法表达时才增加动作子资源。

## 9. 依赖方向

- 领域Package不得依赖HTTP、PostgreSQL、Redis、Docker或前端实现。
- Transport只负责输入输出转换，不得包含SQL。
- Infrastructure实现应用层或领域层消费的Interface。
- `cmd/` 与 `internal/app/` 是Composition Root，可以连接具体实现。
- 不得向Handler传递原始数据库连接池。
- Redis Client不得泄漏到Redis适配器之外。
- 禁止循环依赖和共享垃圾桶Package。

## 10. 配置与秘密

- 配置必须类型化，只在启动时加载和校验，之后保持不可变。
- 每项配置只有一个Canonical环境变量。
- 必填配置缺失时以简洁错误停止启动。
- 默认值必须适合资源有限的个人服务器。
- Secret不得提供硬编码默认值。
- 禁止记录密码、DSN、API Token、Redis凭据、完整Authorization Header和完整邮件内容。
- `.env`、Secret文件和生产配置必须被Git忽略。
- 示例配置只能包含Placeholder。

## 11. HTTP与API规范

- 在有意做出版本化兼容决策前，必须保留旧API行为。
- 新公共API使用 `/api/v1`。
- `/livez` 只检查进程存活。
- `/readyz` 使用短Deadline检查必要依赖。
- HTTP Server必须设置ReadHeader、Read、Write、Idle和Shutdown Timeout。
- Request Body、Header、Query、Upload和Pagination必须有明确上限。
- 错误响应使用统一JSON Envelope，包含稳定错误码和Request ID。
- 内部错误记录上下文，但不得直接返回客户端。
- Authentication与Authorization集中实现。
- 所有账户资源都必须检查Object Ownership。
- 内部接口使用独立Listener或密码学鉴权，并由公网Proxy显式拒绝。
- 生产CORS使用Allowlist；通配符必须有明确、可审查的原因。

## 12. Authentication与权限

- Token使用 `crypto/rand` 生成，熵不得低于256 bit。
- 数据库只保存Token Hash和非秘密展示前缀。
- 适用时使用Constant-time Comparison。
- Scope检查默认拒绝。
- Token Rotation必须在一个原子操作中使旧Token失效。
- 管理Secret不得出现在日志中。
- Web Console优先使用 Secure、HttpOnly、SameSite Cookie，降低XSS窃取风险。
- 登录、注册、域名提交和高成本查询必须限流。

## 13. PostgreSQL规范

- 使用 `pgx/v5`，不引入ORM。
- 只使用参数化SQL。
- Transaction必须短小、显式，并限定在一个业务操作内。
- Query必须继承调用方Context Deadline。
- List必须有确定排序和有界Pagination。
- Index来自实际Query Pattern，不凭猜测添加。
- 发布后的Migration不可修改。
- 每个Migration使用单调递增版本和清晰名称。
- 启动Migration使用PostgreSQL Advisory Lock。
- 破坏性Migration必须经过兼容阶段和备份恢复验证。
- Cleanup使用有界Batch，避免长锁、WAL尖峰和表膨胀。
- pgx Pool限制必须与服务器容量一致，并通过压测校准。

## 14. Redis规范

- Redis只存放限流计数、短期缓存和临时协调状态。
- Key统一使用版本化 `mailwisp:` Namespace。
- 非永久Key必须有TTL。
- 需要原子性的多步修改使用Lua或Transaction。
- 每种Redis故障必须明确是Fail-open还是Fail-closed。
- Redis故障不得破坏PostgreSQL持久状态。
- Cache必须具有Invalidation策略或有界Staleness。

## 15. 邮件入口规范

- 公网SMTP由Postfix负责。
- Go应用通过LMTP接收Postfix投递。
- LMTP准入、解析和持久化使用有界Queue。
- 过载时返回临时投递失败，让Postfix重试。
- Durable Persistence完成前不得确认LMTP成功。
- 限制Message Bytes、Header Bytes/Count、MIME Nesting、Part Count、Decoded Body和Attachment Size。
- 所有Header、Body、HTML、Link和Filename均视为不可信输入。
- 解析失败不得导致进程崩溃。
- Raw Message必须有明确TTL和删除路径。
- 删除Message时必须一致清理关联的外部或Raw Storage。

## 16. 并发与性能

- 使用有界并发，不追求无限并发。
- 每个Queue都必须定义Capacity、Overload行为和可观测Depth。
- 每个Worker Pool都必须说明Size和取消行为。
- Transaction期间不得执行DNS、Redis、Filesystem或外部Network调用。
- LMTP/MIME Hot Path避免重复Allocation与Copy。
- 没有Benchmark或生产等价测量，不做性能结论。
- Benchmark必须记录输入、并发、Payload和机器条件。
- 性能改动必须通过 `go test -race`。
- 空闲Polling使用保守Interval、必要的Jitter和Context取消。

## 17. 后台任务

- Job实现小而明确的Interface，并接收Context。
- 单轮失败只记录并重试，不终止无关服务。
- Job使用有界Batch和Deadline。
- 必须单实例运行的Job使用PostgreSQL Advisory Lock。
- Schedule与Business Logic分离。
- 时间相关逻辑在有价值时通过注入Clock进行测试。

## 18. 前端规范

- 前端使用TypeScript与ES Module。
- Vite只用于开发和构建，生产服务器不运行Node.js。
- 面向用户的界面必须支持中文与英文切换，中文为默认语言。
- i18n Message使用稳定Key，不得把显示文案当作Key。
- 日期、时间、数字、复数和相对时间使用Locale-aware格式化。
- 建立Design Token驱动的主题系统，颜色、字体、圆角、阴影和动效不得散落硬编码。
- 必须提供Light、Dark与Follow System基础模式。
- 扩展主题只在具有清晰视觉定位、完整状态覆盖、可访问对比度和维护价值时增加；不得为数量堆砌重复主题。
- Theme与Language选择在本地持久化，并避免首屏闪烁。
- API Client、State、View、Component与Utility分层。
- 禁止重新形成单一巨型JavaScript或CSS文件。
- 不使用 `innerHTML` 注入不可信字符串。
- 不使用Inline Event Handler。
- 必须兼容严格Content Security Policy。
- 邮件HTML只能在强Sandbox、无Credential环境中渲染。
- 默认阻止邮件外部图片，避免Tracking Pixel。
- UI覆盖Loading、Empty、Partial、Error、Unauthorized和Offline状态。
- Accessibility与Keyboard Navigation是发布要求。
- 生产Asset使用Content Hash与Compression。

## 19. 容器与部署

- 生产Image使用固定Version Tag，Release Lock可进一步固定Digest。
- 禁止使用 `latest`、未限定Major的浮动依赖和无法复现的远程安装脚本。
- Go、Module、Base Image、Node、Package Manager、Frontend Dependency、CI Action和开发工具必须固定明确版本。
- 版本选择必须记录发布日期、维护状态、安全支持、升级成本和与目标平台的兼容性。
- 使用Lockfile并提交Lockfile；自动升级只能创建可审查PR，不得自动部署生产。
- 依赖升级必须经过测试、安全扫描、Migration兼容和必要的Benchmark，不得只因“版本更新”而合并。
- Go使用 `-trimpath` 构建且不泄漏VCS信息。
- 技术上可行时Runtime Container必须Non-root。
- 兼容的服务增加 `no-new-privileges`、Capability限制、Read-only Filesystem和日志上限。
- PostgreSQL与Redis端口不得公开。
- 公网只开放Nginx HTTP(S)和Postfix SMTP。
- Compose Healthcheck使用Readiness，不使用单纯进程存在判断。
- 部署必须支持Graceful Shutdown与Rollback。
- Host专用配置放在 `deploy/`，不得包含Secret。

## 20. 测试与质量门禁

- 新行为必须在同一Change中增加测试。
- 领域规则和Parser优先使用Table-driven Test。
- Unit Test不得依赖公网。
- Integration Test覆盖PostgreSQL Migration、Repository和Redis原子行为。
- End-to-end Test覆盖SMTP/LMTP收件直到API读取。
- Security Test覆盖Authentication、Scope、Ownership、Rate Limit、恶意MIME和HTML隔离。
- Migration Test必须恢复一份旧数据库副本并核对Count和Invariant。
- 完成Go变更前必须执行：

```text
gofmt
go test ./...
go test -race ./...
go vet ./...
govulncheck ./...
```

- 前端变更必须额外执行Type Check、Unit Test、Lint和Production Build。
- 部署变更必须额外执行Compose Render和Container Build。
- 窄范围测试通过不能证明广范围兼容。

## 21. 外部API兼容

- 内部Domain Model与Canonical API由MailWisp自身约束定义，不直接复制任何第三方API的数据结构。
- Cloudflare Temp Email、215.im、DuckMail等兼容能力通过独立Adapter实现。
- Adapter只负责Authentication、Path、Field、Status Code和Error Envelope转换，不得把第三方命名渗透到领域层。
- 每个兼容目标必须有来源可追溯的Contract Fixture与Contract Test。
- 文档不清、行为不稳定或安全模型冲突时，可以明确声明不兼容，不以牺牲核心架构为代价追求表面兼容。
- 兼容范围必须版本化，文档明确列出Supported、Partially Supported与Unsupported行为。
- 不得冒用第三方商标，也不得声称官方兼容关系。

## 22. 可观测性

- 日志必须结构化，并在适用时包含Request ID或Job ID。
- 预期客户端错误不得制造大量Stack Trace噪声。
- Health响应不得暴露Secret和内部拓扑。
- 至少监控HTTP延迟/错误、LMTP Queue Depth、投递延迟/失败、数据库Pool、Redis错误、Cleanup数量和Postfix Queue。
- Metrics Label必须有界，禁止使用Email Address、Token ID、Message ID或任意Domain作为Label。

## 23. 完整Git工作流

### 分支模型

- `main` 始终代表可构建、可测试、可发布的稳定状态。
- 初始化仓库的Bootstrap Commit可以直接建立 `main`；之后功能开发不得直接在 `main` 上进行。
- 开发使用短生命周期分支：
  - `feat/<name>`：新功能。
  - `fix/<name>`：缺陷修复。
  - `refactor/<name>`：行为保持的重构。
  - `perf/<name>`：有Benchmark证据的性能优化。
  - `security/<name>`：安全修复。
  - `test/<name>`：测试体系。
  - `docs/<name>`：文档与ADR。
  - `chore/<name>`：工具链和维护。
- 分支名使用小写英文与连字符，简洁表达目标。

### 开始开发

1. 确认当前目录、仓库和远程地址正确。
2. 执行 `git status --short`，识别并保护已有改动。
3. 获取远程更新时使用非破坏性命令。
4. 从最新稳定 `main` 创建任务分支。
5. 开始修改前明确本次Change的范围和验收证据。

### 提交前

1. 检查 `git diff --check`。
2. 检查 `git status --short`，确认没有Secret、Dump、Raw Mail或构建产物。
3. 阅读完整Diff，不能只看文件名。
4. 执行与改动范围匹配的全部质量门禁。
5. 验证README、示例配置和ADR是否与实现一致。
6. 任何失败或未执行检查都必须在交付说明中明确列出。

### Commit规范

- 使用Conventional Commits：
  - `feat:`、`fix:`、`refactor:`、`perf:`、`security:`、`test:`、`docs:`、`chore:`、`ci:`、`build:`。
- 主题使用祈使语气，简洁说明结果，默认中文。
- 一个Commit只表达一个完整意图，并保持可构建、可测试。
- 不把无关格式化、重命名和行为修改混在同一个Commit。
- 不提交Secret、生产数据、Raw Mail、Dump和本地产物。
- 不修改或Amend不属于自己的已有Commit，除非用户明确要求。

### Push规范

- Push前再次执行 `git status` 和必要验证。
- 首次Push使用 `git push -u origin <branch>` 建立Upstream。
- 只Push当前任务相关分支和明确授权的Tag。
- 禁止 `git push --force`；确有必要且用户明确授权时只能使用 `--force-with-lease`。
- 禁止删除远程Branch、Tag或Release，除非用户明确授权。
- 未经授权不得Push到非本项目Remote。

### Pull Request规范

- 除仓库初始化外，变更通过PR合并到 `main`。
- PR标题遵循Conventional Commits风格。
- PR描述默认中文，至少包含：目标、主要改动、风险、数据/迁移影响、验证命令与结果、回滚方式。
- CI未通过、Review未完成或迁移未验证时不得合并。
- 合并策略优先Squash Merge，保持 `main` 历史清晰；需要保留独立Commit语义时再使用Merge Commit。
- 合并后才删除已完成的远程任务分支，并遵守仓库设置。

### Release规范

- 使用Semantic Versioning：`vMAJOR.MINOR.PATCH`。
- Release前必须从干净Checkout完成测试、漏洞扫描、前端构建、镜像构建、Migration与恢复验证。
- Tag必须指向 `main` 上已验证的Commit。
- Release Notes默认中文，明确Breaking Change、Migration、配置变化和Rollback步骤。
- 不发布来源不明或无法通过Git Commit复现的镜像。

## 24. 完成定义

只有满足以下条件，任务才算完成：

- 请求的行为已真实实现，而非只有Scaffold。
- 命名、Package边界和依赖方向符合本文件。
- Error、Cancellation、Overload和Shutdown路径已处理。
- 相关测试存在且通过。
- Security和Data Lifecycle影响已审查。
- 文档、示例与代码一致。
- Git中没有Secret和生产数据。
- Compatibility声明有旧系统行为或数据作为权威证据。
- Git流程、Commit、Push和PR步骤没有被跳过。
- 所有操作在用户授权范围内，没有越权。
- 最终仓库仍适合一台个人服务器长期运行和维护。
