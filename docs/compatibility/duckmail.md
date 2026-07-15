# DuckMail兼容边界

状态：核心Contract已实现，附件下载为部分兼容
上游一手来源：<https://raw.githubusercontent.com/MoonWeSif/DuckMail/main/public/llm-api-docs.txt>
核验日期：2026-07-15

MailWisp通过显式`/compat/duckmail`命名空间提供Adapter，默认由`MAILWISP_DUCKMAIL_ENABLED=false`关闭。兼容层不会改变Canonical API字段、Token Grammar或生命周期规则。

## Supported

- `GET /domains`与Hydra分页Envelope；
- `POST /accounts`，精确Address、Argon2id Password Credential与默认/正数`expiresIn`；
- `POST /token`，Address/Password验证后返回可作为Bearer使用的MailWisp Opaque Capability；
- `GET /me`与只能删除自身的`DELETE /accounts/{id}`；
- `GET /messages`、`GET /messages/{id}`、`PATCH /messages/{id}`与`DELETE /messages/{id}`；
- `GET /sources/{id}` Raw RFC 822数据；
- DuckMail `{error,message}`错误Envelope和主要HTTP状态码。
- `POST /accounts`与Canonical创建入口共享瞬时Token Bucket和PostgreSQL UTC日配额；默认每个HMAC客户端身份100次/日，超额保持DuckMail错误Envelope并返回429。

## Partially Supported

- 上游Token示例是JWT；MailWisp返回Opaque Bearer。客户端只要不解析JWT Payload即可兼容，依赖JWT声明的客户端不兼容。
- `downloadUrl`指向Source API；附件Metadata存在，但独立附件下载URL尚未开放。
- 私有域名`dk_` API Key未实现；`GET /domains`只返回配置中的公共域名。
- Hydra提供固定30条分页，但不复制上游所有JSON-LD元数据。

## Unsupported

- `expiresIn`为`0`或`-1`的永久Account。MailWisp会返回`422`，因为永久匿名邮箱违反临时生命周期和V1 Credential策略。
- Legacy Root Path。Adapter不会注册到根路径，避免与Canonical API和其他兼容层冲突。

Contract Fixture位于`internal/httpapi/testdata/duckmail-contract.json`，HTTP黑盒测试验证命名空间、账户、登录、Hydra字段和错误Envelope。
