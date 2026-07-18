# ADR 0024：MailWisp 统一邮件工作台与邮箱 Profile

状态：已接受（产品边界与 UI 信息架构）

## 背景

MailWisp 最初只验证了匿名临时收件：创建一个 Inbox、用 Capability 恢复、通过 LMTP 收信并查看邮件。这个垂直切片已经具备真实 API、PostgreSQL、Content Store、Parser、Session 和 Vue 工作台，但它不能覆盖自托管用户的另一种真实用法：长期保留一个或多个邮箱，并在需要时发信。

YYDS Mail 的 Dashboard、邮箱管理、API Key、域名、Webhook 和文档导航证明了工作台壳层的必要性；Cloudflare Temp Email 的发送、DKIM、附件、转发和 OAuth/Passkey 证明了扩展方向；DuckMail 的账号生命周期、API Key、Bearer Token 和收件边界证明了兼容接入不能被假设成“所有服务都能发信”。这些能力不能直接照搬：MailWisp 是个人自托管产品，不引入余额、套餐、广告或无法验证的第三方能力。

## 决策

MailWisp 定义为**自托管邮件工作台**，临时邮箱是其中一个 Profile，而不是产品的全部身份。Canonical UI 使用统一的 Mailbox、Identity、Credential、Message 和 Delivery 模型；第三方服务只通过 Adapter 投影，不污染 Canonical Domain Model。

### 邮箱 Profile

| Profile | 接收 | 发送 | 生命周期 | 访问方式 |
| --- | --- | --- | --- | --- |
| `temporary` | 是 | 否 | 有界 TTL，自动清理 | 一次性 Capability / Browser Session |
| `persistent_receive` | 是 | 否 | 无固定到期，受保留与容量策略约束 | 账户 Session / API Key |
| `persistent_full` | 是 | 是 | 无固定到期，受保留、容量与发送配额约束 | 账户 Session / API Key |

`persistent_full` 只有在 Sender Identity 完成域名与发信验证后才允许提交发送；未验证时 UI 必须显示阻断原因，而不是提供一个必然失败的“发送”按钮。发信需要持久 Outbox、Delivery Attempt、退信/重试状态和明确的限流、滥用与日志隐私边界。

### Canonical 工作台导航

登录或恢复长期工作台后使用以下信息架构：

1. **概览**：当前邮箱数量、未读、即将到期、发送队列和系统健康；只显示可解释的事实，不做虚假 KPI。
2. **邮箱**：创建、恢复、切换和删除 Mailbox；按 Profile、状态、域名筛选。
3. **收件箱**：双栏列表/详情、搜索、未读、星标、附件、Sandbox HTML、Raw Source 和删除；临时 Profile 隐藏不适用的归档/长期标签。
4. **写信**：仅对 `persistent_full` 和已验证 Identity 开放；支持草稿、附件、发送中、成功、退信、重试和取消。
5. **域名与身份**：域名、地址、别名、MX、SPF、DKIM、DMARC、发信状态和验证记录。
6. **密钥与会话**：Capability、Browser Session、Personal Access Token、Webhook Secret 的创建、Scope、最后使用、轮换和撤销。明文密钥只展示一次。
7. **兼容中心**：Cloudflare Temp Email、YYDS Mail、DuckMail Adapter 的启用状态、Endpoint、认证、连通测试、固定上游版本和 Supported/Partially Supported/Unsupported 能力矩阵。Adapter 是 MailWisp 对外提供的协议投影，不伪装成外部邮箱聚合 Connector。
8. **Webhook 与规则**：事件订阅、签名、重试、过滤和转发；先做有界投递，再考虑复杂自动化。
9. **API 文档**：Canonical API、Adapter API、认证、错误码、分页、示例和 OpenAPI 下载。
10. **设置**：中英文、System/Light/Dark/Mist、保留策略、轮询、隐私和本地部署信息。

匿名临时模式仍使用低摩擦入口，不强制出现完整账户侧栏；创建后可通过 Browser Session 进入工作台。长期 Profile 需要显式 Owner/账户认证，不能把 Capability 偷换成长期账户密码。

### 实施优先级

- **P0**：工作台壳层与路由、多个 Mailbox、Profile 选择、长期接收、收件箱增强、Session/PAT 管理、域名/Identity 只读状态、三类 Adapter 的状态与能力矩阵。
- **P1**：持久 Outbox、SMTP/Provider 选择、队列和退信、Webhook、转发与过滤规则、域名验证操作。
- **P2**：Passkey/OAuth、AI 摘要与验证码提取、IMAP/SMTP Proxy、移动端和多 Owner 管理。

### 明确不做

- 不引入余额、套餐、支付、广告、社交头像或 SaaS 多租户计费模型。
- 不承诺三类上游 API 的百分百同构；每个 Adapter 必须有固定版本/Fixture 和 Unsupported 列表。
- 不把兼容 Adapter 冒充外部邮箱聚合；未来如需连接 Gmail、IMAP 或第三方临时邮箱，单独定义 Connector 的凭据、同步和故障边界。
- 不为了“功能看起来多”预先部署 Redis、消息队列、微服务或 Rust 解析器。
- 不在发送链路成熟前用前端 Mock 伪装发信完成。

## 后果

当前匿名收件工作台是 P0 的第一个真实 Vertical Slice。后续新增 Dashboard、密钥、域名、Adapter 和发信界面必须先落 Canonical API/数据模型与安全 Contract，再接入 Vue；只做静态菜单或假数据不算完成。发送能力会增加 Postfix 出站、Provider、DKIM/ SPF/DMARC、退信和滥用防护的运维成本，因此单独作为 P1 纵向切片验收。
