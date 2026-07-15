# 架构、工具链与固定版本候选

状态：阶段性验证完成，Go与前端基线已锁定，其余组件继续Technical Spike
调研日期：2026-07-14

本文件记录候选与证据，不代表所有技术已经最终采用。正式锁定前仍需Technical Spike、兼容矩阵、Benchmark与恢复测试。

## 版本选择原则

- 禁止 `latest`。
- Runtime、Library、Base Image、Build Tool与CI Action使用明确版本。
- 稳定版不等于适合立即采用；刚发布的Major必须经过生态兼容验证。
- 应用依赖提交Lockfile，Container Release进一步记录Digest。
- 每月安排安全升级评估，每季度评估功能升级。
- 自动化只创建Upgrade PR，不自动部署生产。
- Upgrade必须通过测试、漏洞扫描、Migration、恢复、Benchmark和Rollback门禁。

## 当前稳定版本证据

| 组件 | 2026-07-14候选版本 | 初步意见 | 一手来源 |
|---|---:|---|---|
| Go | 1.26.5 | 已通过编译、测试、Race、Vet与固定版漏洞扫描，锁定为工程基线 | https://go.dev/dl/?mode=json |
| chi | v5.3.0 | 候选HTTP Router，保持 `net/http` 兼容与较小依赖面 | https://github.com/go-chi/chi/releases/tag/v5.3.0 |
| Gin | v1.12.0 | 功能完整，但本项目暂不优先，需与chi做实现复杂度对比 | https://github.com/gin-gonic/gin/releases/tag/v1.12.0 |
| pgx | v5.10.0 | PostgreSQL Driver与Pool首选候选 | https://api.github.com/repos/jackc/pgx/tags |
| PostgreSQL | 18.4 | 绿地Schema与唯一持久事实源；只验证MailWisp自身版本Migration | https://www.postgresql.org/versions.json |
| PgBouncer | 1.25.2 | 默认不进入Reference链路，多副本或连接压力出现后再评估 | https://www.pgbouncer.org/changelog.html |
| Redis | 8.8.0 | 限流、短期Cache与临时协调候选，不保存唯一持久数据 | https://github.com/redis/redis/releases/tag/8.8.0 |
| Postfix | 3.11.5 | 公网SMTP与持久重试首选，不自研公网SMTP | https://www.postfix.org/announcements/postfix-3.11.5.html |
| Nginx Stable | 1.30.3 | Host Reverse Proxy候选，固定Stable Patch | https://nginx.org/en/download.html |
| Caddy | 2.11.4 | 从零部署时的自动TLS备选，需与Nginx做运维对比 | https://github.com/caddyserver/caddy/releases/tag/v2.11.4 |
| Vite | 8.1.4 | 前端Build候选，不在生产运行Node.js | https://registry.npmjs.org/vite/latest |
| Vue | 3.5.39 | 前端Framework优先候选，仍需React/Svelte/原生方案矩阵 | https://github.com/vuejs/core/releases |
| vue-i18n | 11.4.6 | Vue方案下的i18n候选 | https://registry.npmjs.org/vue-i18n/latest |
| Pinia | 3.0.4 | 只在状态复杂度证明需要时使用，不默认滥用Global Store | https://registry.npmjs.org/pinia/latest |
| TypeScript | 6.0.3 | 首轮稳妥候选；7.0.2发布仅约6天，先做非阻断兼容试跑 | https://www.typescriptlang.org/docs/handbook/release-notes/typescript-6-0.html |

## 初步后端组合

候选主路径：

```text
HTTP: net/http + chi
SMTP: Postfix 3.11.5
Delivery: LMTP -> Go
Persistence: pgx v5.10.0 -> PostgreSQL 18.4
Ephemeral state: Redis 8.8.0
Migration: Goose v3.27.2候选
```

### Go 1.26.5 Technical Spike

2026-07-14在Windows开发环境完成以下验证：

```text
GOTOOLCHAIN=go1.26.5 go test ./...
GOTOOLCHAIN=go1.26.5 go test -race ./...
GOTOOLCHAIN=go1.26.5 go vet ./...
govulncheck v1.6.0（使用Go 1.26.5构建）
```

全部通过，`govulncheck`报告0个可达漏洞。首次验证暴露出原有Scanner由Go 1.25.4构建，无法加载Go 1.26标准库；在保持Scanner版本为v1.6.0的前提下，用Go 1.26.5重新构建后通过。基于这条完整证据链，仓库通过 `go.mod`、`.go-version` 与验证脚本同时固定Go 1.26.5，不再保留1.25作为默认回退。

### 为什么暂不采用Gin

当前判断不是“chi跑分必然更快”，而是chi保持 `net/http` 模型、Middleware组合和依赖面更小，更容易维持Transport边界。必须通过一个真实Endpoint Vertical Slice比较：

- 路由与Middleware可读性。
- Validation与Error Mapping工作量。
- OpenAPI集成。
- Allocation与Latency。
- 测试便利性。
- 团队维护成本。

如果Gin在完整实现中显著降低复杂度且不破坏边界，可以重新选择。

### 为什么保留Postfix候选

Postfix已经解决公网SMTP兼容、队列、Retry、Backoff和大量协议边界。Go专注LMTP后的领域处理，避免重新实现公开SMTP Server。任何自研公网SMTP提案都必须证明能够达到同等级别的协议、安全和队列可靠性，否则直接淘汰。

### pgx直连与PgBouncer

Reference Profile优先由pgx Pool直连PostgreSQL，减少一层Network Hop与配置。PgBouncer不永久排除，但只有以下证据出现时才加入：

- 多个Application Replica造成连接总量压力。
- PostgreSQL连接建立成本成为可测瓶颈。
- Transaction Pooling不会破坏Prepared Statement、Session State和Migration行为。

## 邮件内容存储候选

不建议把无上限Raw MIME长期作为大BLOB全部堆入PostgreSQL。

候选方案：

- PostgreSQL保存Message Metadata、Indexable Field、Retention与Content Reference。
- Raw MIME使用按Content Hash分层的本地文件对象存储。
- 设计兼容S3的Storage Interface，但Reference Profile不强制部署S3。
- Raw MIME、Attachment和Parsed Body分别设置Quota与TTL。
- 删除流程可验证，Orphan可扫描，Backup与Restore有一致性策略。

是否最终采用文件对象存储，需要用真实邮件样本验证小文件数量、Filesystem开销、Backup速度与恢复一致性。

## 前端候选

当前优先验证Vue 3 + TypeScript + Vite，但尚未最终决定。

必须与以下方案做同功能Vertical Slice比较：

- 原生TypeScript + Web Components。
- Vue 3。
- React。
- Svelte。

比较场景固定为：登录、邮箱列表、邮件详情、API Error、中文/英文切换、Light/Dark/Follow System切换和一套扩展主题。

评价内容包括Bundle、Runtime Allocation、开发代码量、类型安全、i18n、Accessibility、CSP、测试生态和长期维护状态。

## 主题系统初步边界

- 使用Semantic Design Token，不允许页面直接依赖具体色值。
- 基础模式：Light、Dark、Follow System。
- Theme Preset只覆盖Token，不复制整套Component CSS。
- 每套Theme必须覆盖Success、Warning、Danger、Focus、Disabled、Chart与Email Sandbox边界。
- 使用WCAG对比度自动检查。
- 不以“24套”作为KPI；如果Token系统成熟，可以低成本提供多套高质量Preset，但数量服从质量。

## 工具链候选

| 能力 | 候选工具与版本 | 说明 |
|---|---|---|
| SQL Migration | Goose v3.27.2 | SQL-first；与golang-migrate二选一 |
| Integration Test | testcontainers-go v0.43.0 | PostgreSQL、Redis真实集成测试 |
| HTTP/SMTP Load Test | k6 v2.1.0 + Go Benchmark | 场景和Payload必须入库 |
| Go Reachability Scan | govulncheck | 必须执行，不接受跳过 |
| Go SAST | gosec v2.27.1 | 与人工Threat Review配合 |
| Image/Filesystem Scan | Trivy v0.72.0 | 固定版本与Policy |
| Secret Scan | Gitleaks v8.30.1 | Pre-commit与CI双层 |
| Frontend Test | Vitest v4.1.10 | 版本待Framework最终选择确认 |
| Browser E2E | Playwright v1.61.1 | 覆盖i18n、Theme与Mail Flow |
| Frontend Lint | ESLint v10.7.0 | 版本需与Framework Plugin兼容验证 |
| Format | Prettier v3.9.5 | 只做机械格式，不替代Lint |

## Resource Profile策略

项目不绑定当前服务器。最终通过压测提供至少三个Profile：

- Development：本地功能与集成测试。
- Reference：单节点生产部署与明确容量范围。
- Extended：更高吞吐或多Replica所需的共享Storage、PgBouncer和Job协调能力。

Profile必须来自真实Benchmark与容量模型，不使用虚构QPS。

## 尚未完成

- 前端四类方案的同功能Technical Spike。
- Nginx与Caddy从零部署体验和资源实测。
- PostgreSQL 18的MailWisp Schema Migration、查询计划与恢复验证。
- Local Object Store与PostgreSQL Raw MIME的真实样本Benchmark。
- Redis存在与无Redis两种Rate Limit实现的复杂度和故障测试。
- Cloudflare Temp Email、215.im与DuckMail兼容层对核心架构的影响。
