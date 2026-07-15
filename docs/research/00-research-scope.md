# MailWisp 前期研究范围与决策门槛

状态：阶段性完成；旧产品研究范围已由ADR 0018取代
开始日期：2026-07-14

## 研究目的

在业务代码大规模实现前，重新验证MailWisp的产品边界、架构、前后端技术、存储、邮件入口、工具链、固定版本策略和第三方API兼容方式。

当前文档与ADR中的架构均属于候选假设，不是为了维护已写Scaffold而必须保留的结论。研究证明存在更优方案时，可以替换现有Scaffold；无法满足核心目标的功能、兼容层或组件可以舍弃。

## 核心约束

- 目标是高质量自托管产品；现有服务器不是架构和资源上限。
- 单台Linux服务器作为Reference Deployment Profile，同时评估更高容量档位。
- 正确性、安全性、可维护性和用户体验优先于极端压缩资源。
- 业务目标仍包括合理空闲占用、低负载、低延迟和有界高并发。
- 必须支持可靠SMTP收件与失败重试。
- MailWisp是独立新产品，不承担任何旧TempMail数据、API、Token或配置迁移。
- 默认中文，支持中英文切换。
- 前端需要Light、Dark、Follow System和可扩展主题系统。
- Runtime与Build Tool必须版本明确、可复现、可审计。
- 生产架构应由一名维护者独立部署、恢复和排障。

## 候选技术问题

### 后端

- Go标准库 `net/http`、Chi、Gin分别带来多少复杂度、依赖和性能收益？
- 单Go Binary是否仍是最适合的边界？HTTP、LMTP与Job应如何隔离故障？
- Postfix + LMTP是否优于Go直接实现公网SMTP？
- pgx直连与PgBouncer在单实例场景的真实差异是什么？
- Redis是否值得保留？哪些能力可用进程内状态替代？
- Raw Message应存PostgreSQL、压缩文件、对象存储，还是只短期保存？
- Migration采用自研最小执行器、Goose、Atlas或其他工具？
- API兼容采用独立Adapter、独立路由组还是协议网关？

### 前端

- 原生TypeScript、Web Components、Vue、React、Svelte中哪种最符合资源与维护目标？
- Framework Runtime、Bundle、Hydration、生态、i18n、Accessibility和长期维护成本如何？
- 是否需要SPA Router，还是多页面/服务端静态Shell更合适？
- i18n使用框架生态库还是轻量自有Message层？
- Theme Token如何覆盖颜色、Typography、Spacing、Radius、Shadow、Motion和邮件内容隔离？
- 扩展主题应提供多少套，如何保证每套完整、可访问且不过度增加CSS？

### 部署与工具链

- Host Nginx、Caddy或其他Web Server的空闲资源、自动TLS、配置和维护成本。
- Container与Host-native部署的边界。
- Go、Node、Package Manager、Base Image、CI Action和Scanner的固定版本。
- Test、Race、Fuzz、Benchmark、Load Test、SAST、Dependency Scan、Image Scan与Secret Scan工具组合。
- 自动更新只允许创建可审查PR，不允许自动部署生产。

## 外部兼容研究对象

### Cloudflare Temp Email

- 官方仓库：https://github.com/dreamhunter2333/cloudflare_temp_email
- 研究架构、部署、API、数据模型、功能演进、维护状态、Issue痛点与Cloudflare平台绑定。

### 215.im

- 官方文档：https://vip.215.im/docs
- 研究Authentication、Endpoint、Request/Response、Error、Mailbox Lifecycle、Quota与实时能力。

### DuckMail

- 官方文档：https://www.duckmail.sbs/en/api-docs
- 研究Authentication、Endpoint、Request/Response、Error、Mailbox Lifecycle、Quota与实时能力。

## 决策评分模型

每个关键候选使用同一评分标准，满分100：

| 维度 | 权重 | 说明 |
|---|---:|---|
| 正确性与安全 | 25 | 故障模型、数据安全、攻击面、协议成熟度 |
| 工程与用户体验质量 | 20 | 领域边界、类型安全、可访问性、i18n、交互完整性 |
| 自托管运维成本 | 15 | 部署、备份、恢复、升级、排障、依赖数量 |
| 资源效率 | 10 | 常驻内存、空闲CPU、后台连接与Polling |
| 延迟与吞吐 | 15 | 热路径延迟、有界并发、背压和过载行为 |
| 可维护性 | 15 | 代码规模、测试能力、生态稳定性、长期演进 |
| 版本可控性 | 5 | 固定版本、Lockfile、发布节奏和安全支持 |
| 兼容能力 | 5 | 第三方API Adapter的Contract完整性与隔离成本 |

任何方案即使总分较高，只要违反以下硬门槛也直接淘汰：

- 需要浮动版本或无法复现构建。
- 需要把生产Secret写入仓库。
- 过载时无法背压，只能丢信或崩溃。
- 无法完成MailWisp自身版本升级、备份恢复或可靠回滚。
- 维护状态不明且没有可控替代方案。
- 为表面兼容污染核心Domain Model。

## 研究交付物

- Cloudflare Temp Email纵向演进与架构分析。
- 215.im、DuckMail API Contract与兼容矩阵。
- 前端Framework与主题/i18n方案对比。
- 后端、邮件、数据库、Redis、Web Server与工具链对比。
- 明确版本与升级策略。
- 最终架构ADR和被舍弃方案清单。
- 完整横纵分析Markdown与PDF报告。
