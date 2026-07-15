# YYDS Mail兼容边界

状态：Temporary Inbox核心Contract已实现，用户/API Key与实时自动化能力为部分兼容或未实现

一手来源：<https://maliapi.215.im/v1/openapi.json>
OpenAPI版本：`1.0.0`
核验日期：2026-07-15
核验内容SHA-256：`12882318619f9e2e0b3055cdafa3b1993739185a39bcff87a48a1418a34647a6`

MailWisp通过显式`/compat/yyds/v1`命名空间提供Adapter，默认由`MAILWISP_YYDS_ENABLED=false`关闭。所有JSON响应保持`{success,data,error,errorCode}`Envelope，Temporary Token映射为MailWisp Inbox Capability，不签发JWT，也不在Query中携带Token。

## Supported

- `GET /domains`公共收件域名；
- `POST /accounts`与Deprecated Alias `POST /inboxes`；
- 随机地址、`localPart + domain`、Legacy前缀或完整`address`创建；
- `POST /token`使用当前Temporary Token原子Rotation，旧Token立即失效；
- `GET /accounts/me`、`GET /accounts/{id}`与`DELETE /accounts/{id}`；
- `GET /messages?limit=&offset=`、完整Inbox级`total`与`unreadCount`、`GET/PATCH/DELETE /messages/{id}`；
- `GET /sources/{id}` Raw RFC 822数据；
- Attachment Metadata及Bearer保护的独立下载URL；
- Stable `errorCode`、主要HTTP状态与Temporary Token Ownership检查。
- `POST /accounts`和`POST /inboxes`与Canonical创建入口共享瞬时Token Bucket和PostgreSQL UTC日配额；超额保持YYDS Envelope并返回429。

## Partially Supported

- 上游列表上限为200；MailWisp同样接受200，但不实现`seen`、`since`、`q`与`after_id`过滤。
- `PATCH /messages/{id}`支持标记已读；Temporary Token不支持Starred或恢复未读。
- 上游Attachment `downloadUrl`可能附加Query Token；MailWisp拒绝Query Credential，客户端必须继续发送Bearer Header。
- Upstream支持随机/自动Domain Strategy；MailWisp在未指定Domain时使用配置顺序中的首个公共域名。

## Unsupported

- JWT User Session、Cookie User Session与`AC-` API Key；
- 用户、套餐、支付、OAuth、Passkey、2FA与Admin API；
- Private/Custom Domain管理、Wildcard Rule和Subdomain；
- `/messages/next`原子Long Poll、WebSocket、Webhook与Share Link；
- Legacy Root Path。Adapter只注册在显式兼容Namespace。

Contract Fixture位于`internal/httpapi/testdata/yyds-contract.json`。Fixture固定一手来源、版本、Hash、Envelope、Authentication与已实现Endpoint，黑盒测试验证创建、列表、Token Rotation和禁用状态。
