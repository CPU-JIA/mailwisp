# 外部API兼容阶段性矩阵

实现更新（2026-07-15）：DuckMail、YYDS Temporary Inbox与Cloudflare Temp Email匿名Inbox核心Adapter已分别在显式命名空间实现，具有独立Contract Fixture、黑盒测试和兼容矩阵。三者认证、ID、Envelope与能力范围仍彼此独立，不能泛化为同一Contract。

状态：阶段性结论，等待完整Contract复核
访问日期：2026-07-14

## 初步判断

三类外部API没有一套适合直接成为MailWisp核心Contract：

- Cloudflare Temp Email功能丰富，但累积多套Header、JWT与Feature Flag历史。
- DuckMail接近mail.tm/Hydra风格，客户端生态容易适配，但账户密码与Root Path不适合直接定义核心Domain。
- YYDS Mail公开能力更广，包含Long Poll、WebSocket与Webhook，但认证和Envelope具有自身业务语义。

因此MailWisp应先设计Canonical API，再通过Adapter兼容。

## DuckMail阶段性Contract

Base URL：`https://api.duckmail.sbs`

主要Endpoint：

```text
GET    /domains
POST   /accounts
POST   /token
GET    /me
DELETE /accounts/{id}
GET    /messages
GET    /messages/{id}
PATCH  /messages/{id}
DELETE /messages/{id}
GET    /sources/{id}
```

Authentication：

- 常规接口使用Bearer Token。
- Token通过Address与Password交换。
- Private Domain API Key以 `dk_` 开头，部分Domain/Account接口支持。
- 文档显示API Key也使用Authorization Bearer语义，Adapter必须精确复核Header优先级。

Lifecycle：

- `expiresIn` 以秒表示Account Lifetime。
- 省略时默认24小时。
- `0` 或 `-1` 表示永久。

实时能力：

- 官方文档未发现Webhook Contract。
- 当前前端文案明确使用1至2秒Polling，并显示Mercure被弃用的迹象。

## YYDS Mail阶段性Contract

文档：https://vip.215.im/docs

阶段性证据：

- API Base：`https://maliapi.215.im/v1`。
- Authentication至少包含JWT、`X-API-Key`（`AC-`前缀）和Temporary Token。
- 核心资源包含Accounts、Inboxes、Messages、Sources、Webhooks和Custom Domains。
- 提供 `/messages/next` Long Poll语义。
- 文档声明WebSocket与Webhook能力。
- 使用OpenAPI 3.1与稳定 `errorCode`。
- 文档声明DuckMail-compatible，但兼容不是完全同义：Password字段可能被忽略，`/token` 更接近已有Temporary Token续签，而不是Address/Password登录。

## Canonical API候选原则

当前值得进一步验证的核心模型：

```text
/api/v1/inboxes
/api/v1/inboxes/{id}/messages
/api/v1/messages/{id}
/api/v1/messages:next 或Cursor/Long Poll
/api/v1/webhooks
/api/v1/domains
```

Authentication优先研究Passwordless Capability Token与正式User Session并存，避免把“临时邮箱必须拥有密码账户”写死。

## Adapter边界

### DuckMail Adapter

- 复刻Root Path。
- 复刻Hydra风格Collection Envelope。
- 复刻Address/Password Token Exchange。
- 复刻整数/字符串ID、时间与Error细节。
- 通过Contract Fixture验证常见DuckMail Client无需修改。

### YYDS Adapter

- 复刻 `/v1` Envelope、Address参数与Endpoint Alias。
- 映射JWT、`X-API-Key`与Temporary Token。
- 映射Long Poll、WebSocket与Webhook语义。
- 对“DuckMail-compatible”差异建立显式测试，不能继承其宣传结论。

### Cloudflare Temp Email Adapter

- 独立处理Address JWT、User Token、Admin Header和Feature Flag Projection。
- Legacy Root Path默认关闭，由部署配置显式开启。

实现边界更新：当前仅投影Address Bearer核心流程。`jwt`字段承载Canonical Opaque Capability；用户/Admin/Header体系保持Unsupported。为满足上游整数ID，Adapter使用独立PostgreSQL Identity映射；Raw/Parsed列表使用20项与32 MiB硬上限，避免复制上游无界响应风险。

## 可舍弃边界

以下兼容若会显著污染核心或无法获得可靠Contract，可以不实现：

- Cloudflare平台管理、D1初始化与Workers Binding。
- 没有公开稳定Contract的私有管理接口。
- 仅存在于单个历史版本、无客户端价值的拼写错误和内部字段。
- 无法验证行为的“完整IMAP兼容”宣传。

所有舍弃项必须在最终Compatibility Matrix中明确，不使用模糊的“基本兼容”。
