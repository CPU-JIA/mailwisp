# ADR 0002：Canonical API与第三方兼容Adapter分离

状态：已接受
日期：2026-07-14

## 背景

Cloudflare Temp Email、DuckMail与YYDS对邮箱资源、Token、分页、错误和实时能力的定义不同：

- Cloudflare Temp Email累积Address JWT、User Token、Admin Header和多套Legacy Path。
- DuckMail把临时邮箱建模为Address/Password Account，通过 `/token` 获取JWT，并使用Hydra集合。
- YYDS默认使用Passwordless Temporary Token，具有Envelope、Long Poll、Webhook和WebSocket语义；其所谓DuckMail兼容并不等于密码登录兼容。

任何一家的外部Contract都不适合直接定义MailWisp内部领域。

## 决策

MailWisp先定义自己的Canonical Domain与 `/api/v1` Contract，再由独立HTTP Adapter兼容第三方行为。

核心命名：

- `inbox`：收件箱资源。
- `account`：正式用户账户。
- `address`：邮件地址值对象。
- `message`：一次可读取的邮件投递记录。
- `source`：原始RFC 822内容。
- `credential`：认证凭证，不与Inbox实体混为一体。

Canonical Inbox默认使用Passwordless Capability Token。Token由`crypto/rand`生成，熵不低于256 bit，只展示一次，数据库只保存Hash和非秘密前缀。内部生命周期使用绝对`expiresAt`；`expiresIn`只在Transport或兼容层转换。

兼容Adapter只负责：

- Path与Method映射。
- Authentication与Credential映射。
- Request/Response Field映射。
- Pagination、Status Code与Error Envelope映射。
- Legacy时间、ID和文本错误格式。

Domain与Application Package不得出现Hydra、YYDS Envelope、Cloudflare Header等第三方概念。

## 兼容策略

### DuckMail

目标是真实模拟Address/Password登录。启用DuckMail Adapter时，为Inbox额外创建Argon2id Compatibility Credential；不得接受后忽略Password。Hydra只存在于DuckMail Presenter。

### YYDS

优先兼容临时邮箱、消息过滤、`messages/next`与Webhook自动化表面，不复制支付、套餐、OAuth或平台控制台。

### Cloudflare Temp Email

显式命名空间为优先方案；Legacy Root Path由部署配置单独开启。只兼容对客户端有价值的邮件与地址行为，不复制D1、Workers Binding和Cloudflare管理能力。

## 验收

- 每个Adapter使用官方一手资料建立Contract Fixture。
- 黑盒Contract Test验证Path、Header、Body、Status与Error。
- 文档分别列出Supported、Partially Supported和Unsupported。
- 兼容测试失败不得通过修改Canonical Domain字段名来绕过。

## 影响

- 外部生态可以逐步接入，核心领域仍保持唯一语义。
- DuckMail兼容会增加可选密码Credential成本，但不影响原生Inbox。
- Adapter数量增加Transport代码，需要共享Application Service而不是复制业务逻辑。
- 无可靠Contract的行为可以明确不兼容，不承诺“百分百兼容”。
