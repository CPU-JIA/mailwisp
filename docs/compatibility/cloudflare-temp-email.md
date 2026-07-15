# Cloudflare Temp Email兼容边界

状态：最新匿名Inbox核心流程已实现；用户、Admin、发信与Cloudflare平台能力未实现

一手来源：<https://github.com/dreamhunter2333/cloudflare_temp_email>
核验Commit：`99b332345bcf3beff77ed70feaec9d5e10de3590`
上游版本：`v1.10.0`
核验日期：2026-07-15

MailWisp默认在`/compat/cloudflare-temp`显式命名空间提供Adapter，并由`MAILWISP_CLOUDFLARE_TEMP_ENABLED=false`关闭。只有同时设置`MAILWISP_CLOUDFLARE_LEGACY_PATHS_ENABLED=true`，才启用上游前端使用的Root Path投影。Legacy开关不能脱离Adapter单独启用。

Address `jwt`字段返回MailWisp Canonical Opaque Capability。客户端继续使用`Authorization: Bearer <jwt>`，但MailWisp不签发不可撤销、无过期时间的HS256 JWT。地址与邮件对外ID通过独立PostgreSQL Identity映射为稳定正整数，Canonical UUID不会泄漏到该Contract。

## Supported

- `GET /open_api/settings`及关闭用户体系的Feature Flag Projection；
- `GET /user_api/open_settings`，明确返回用户注册体系未启用；
- `POST /api/new_address`随机或指定名称/域名创建；
- `GET /api/settings`返回当前地址与零发信余额；
- `GET /api/mails?limit=&offset=`与`GET /api/mail/{id}` Raw RFC 822读取；
- `GET /api/parsed_mails?limit=&offset=`与`GET /api/parsed_mail/{id}`解析正文及Attachment Metadata；
- `DELETE /api/mails/{id}`与`DELETE /api/delete_address`；
- Bearer Credential、Inbox Ownership、上游`{results,count}`分页和整数ID；
- 最新匿名模式Vue前端所需的核心初始化、创建、列表、查看与删除调用。
- `POST /api/new_address`与Canonical创建入口共享瞬时Token Bucket和PostgreSQL UTC日配额；超额保持纯文本兼容响应并返回429。

## Partially Supported

- 上游列表上限为100。MailWisp为限制Raw MIME聚合内存与响应放大，单页上限为20，单次Raw或Parsed响应正文总量上限为32 MiB；超限返回413。
- 上游默认可能随机选择Domain并支持随机Subdomain；MailWisp未指定Domain时使用Canonical首个公共域名，不支持随机Subdomain。
- 上游默认名称清洗只保留小写字母与数字；MailWisp执行确定性小写与同类清洗，最终仍受Canonical 64字符上限约束。
- Raw Mail的`metadata`投影为`{}`；核心信封、Raw RFC 822、Message-ID与时间可用。
- 上游`parsed_*`接口按请求即时解析；MailWisp读取后台Parser的持久化结果。新邮件尚处于`pending`时Parsed字段可能短暂为空，Raw接口始终可用。
- `created_at`保持上游无时区格式，但值固定按UTC输出，避免部署时区漂移。
- Raw/Parsed高成本读取使用2个并发槽；过载时返回503并让客户端退避，避免个人服务器在多个大邮件请求下失去背压。
- Root Path只在显式Legacy开关下启用；Nginx/Compose已代理`/api`、`/open_api`与`/user_api`。

## Unsupported

- `x-custom-auth`私站密码、Address Password Login与Password Change；
- User JWT、`x-user-token`、Role Token、注册、登录、OAuth2、Passkey与地址绑定；
- Admin API、D1 Migration、Workers Binding、KV、R2/S3管理与Cloudflare Email Routing配置；
- Send Mail、Sendbox、SMTP/IMAP Proxy、Auto Reply；
- Webhook、Telegram、AI提取、Turnstile与IP Blacklist管理；
- Random Subdomain、Custom Regex/Prefix/Role Domain策略；
- Clear Inbox、Clear Sent Items及Legacy内部/历史拼写接口。

Contract Fixture位于`internal/httpapi/testdata/cloudflare-temp-contract.json`，固定上游Commit、关键源文件SHA-256、Endpoint与MailWisp安全上限。MailWisp与Cloudflare Temp Email项目不存在官方兼容或从属关系。
