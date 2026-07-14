# Cloudflare Temp Email纵向与兼容研究

状态：一手资料研究完成
访问日期：2026-07-14
官方仓库：https://github.com/dreamhunter2333/cloudflare_temp_email

## 一句话定位

`cloudflare_temp_email` 是一套深度绑定Cloudflare平台的全功能临时邮箱产品：Email Routing负责收件，Workers运行业务，D1保存邮件与账号，Pages或Worker Assets提供Vue前端，并按需组合KV、S3-compatible Storage、Workers AI、Rate Limiting和Email Sending。

它最大的价值是三年演进形成的完整功能面与兼容生态；最大的代价也来自同一路径：平台绑定、配置面、认证方式和数据关系不断叠加，形成明显历史包袱。

截至调研日：

- 创建于2023-08-15。
- 约10,394 Stars、6,914 Forks。
- 约644个Commit。
- MIT License。
- 最新公开Release为v1.9.0，主分支已标记v1.10.0。
- 主分支最后Commit日期为2026-07-11。
- 主要由Dream Hunter一人维护。

来源：

- https://github.com/dreamhunter2333/cloudflare_temp_email
- https://github.com/dreamhunter2333/cloudflare_temp_email/releases
- https://github.com/dreamhunter2333/cloudflare_temp_email/blob/main/CHANGELOG.md

## 纵向演进

### 2023：极简Cloudflare原型

初始链路非常短：

```text
Cloudflare Email Routing
        -> Worker email handler
        -> postal-mime
        -> D1
        -> Vue + Naive UI
```

早期能力只有随机地址、最近邮件读取和Worker投递。发布次日便加入Hono与JWT，由此确立“地址凭证JWT + Cloudflare全托管”的长期路线。

- 初始Commit：https://github.com/dreamhunter2333/cloudflare_temp_email/commit/bd08a85016ac601021051c4506ba6558b867c60d
- JWT Commit：https://github.com/dreamhunter2333/cloudflare_temp_email/commit/c4cf946

### 2023末至2024上半年：从Demo变成服务

项目增加Admin Panel、注册、邮箱绑定、发信和清理。因 `postal-mime` 在Worker环境解析特定邮件出现失败，又引入Rust WASM Parser。

这个决策值得借鉴的是“真实邮件兼容问题需要Fallback与Corpus”，而不是Rust本身。MailWisp保持Go实现，但必须建立真实恶意/异常邮件Corpus和Parser Fallback策略。

- 解析问题：https://github.com/dreamhunter2333/cloudflare_temp_email/issues/97
- Rust Parser Commit：https://github.com/dreamhunter2333/cloudflare_temp_email/commit/def400e

### 2024中期：功能爆发

用户、Telegram、Webhook、SMTP/IMAP Proxy、S3/R2附件、转发、角色、Passkey、OAuth2和Turnstile快速加入。产品能力显著增强，同时Authentication、Feature Flag、部署变量和数据关系开始叠加。

### 2024末至2025：部署与体验成熟

时区、全局Inbox、UI、Migration入口、自动清理和Proxy持续完善，并于2025-06发布v1.0.0。此时它已不再只是“随机临时邮箱”，而是Cloudflare上的轻量邮件账户平台。

### 2025末至2026：安全、AI与工程化

项目增加IP黑名单、日限额、Workers AI提取验证码/链接、OAuth2映射、STARTTLS、DOMPurify、E2E环境、Gzip邮件存储、六语言和Agent API。

### 2026至今：治理历史包袱

近期大量工作集中在拆巨型Route、修复Header/CORS/Network Error、统一Domain、补部署文档、修复关联删除和反复调整UI。功能继续增长，但维护成本越来越多来自消化早期决策。

## 当前架构与平台绑定

```text
Browser -> Vue 3 + Vite on Pages/Assets
Email Routing -> Worker/Hono
Worker -> D1
       -> KV（条件依赖）
       -> Workers AI（可选）
       -> S3-compatible Storage（可选）
       -> Telegram/Webhook/发送通道
```

Workers、D1和Email Routing为核心必需组件。KV在注册验证码、Telegram、Webhook、Blacklist和日限流等功能开启时成为条件依赖。附件当前通过AWS S3 SDK访问S3-compatible Storage，R2只是推荐实现，不是原生R2 Binding。

## 数据与认证历史包袱

主要数据表包括 `raw_mails`、`address`、`users`、`users_address`、`user_roles`、`user_passkeys`、`auto_reply_mails`、`address_sender`、`sendbox` 和 `settings`。

风险特征：

- Raw MIME主要作为完整TEXT/BLOB保存。
- 地址和邮件关系缺少数据库外键，删除一致性依赖应用代码。
- SQL Patch与Admin运行时Migration双轨并存。
- Authentication同时存在Address JWT、User JWT、Role JWT、Admin Header、Custom Header、地址密码、Passkey、OAuth2和Telegram Data。
- 地址JWT没有显式过期时间。
- 历史密码方案存在客户端SHA-256直接比较问题。

关联删除修复证明了缺少数据库约束的成本：

- https://github.com/dreamhunter2333/cloudflare_temp_email/commit/99b332345bcf3beff77ed70feaec9d5e10de3590

## API兼容价值

高价值兼容Endpoint包括：

```text
GET    /open_api/settings
POST   /open_api/site_login
POST   /open_api/admin_login
POST   /open_api/credential_login
POST   /api/new_address
GET    /api/mails
GET    /api/mail/:id
GET    /api/parsed_mails
GET    /api/parsed_mail/:id
DELETE /api/mails/:id
DELETE /api/delete_address
POST   /api/address_login
POST   /api/send_mail
GET    /api/sendbox
```

兼容难点不只是Path：

- Address JWT使用 `Authorization: Bearer`。
- User JWT使用 `x-user-token`。
- Admin与私站密码使用不同Header。
- `/api/mails` 返回Raw RFC822，`/api/parsed_mails` 才返回解析结构。
- 部分客户端依赖文本错误、整数ID、无时区时间与 `{results,count}` Pagination。
- `/open_api/settings` 暴露大量Feature Flag，外部客户端可能据此改变行为。

建议：

```text
/api/v1/...                   MailWisp Canonical API
/compat/cloudflare-temp/...  显式Compatibility Adapter
Legacy Root Paths            部署时可选开启
```

Cloudflare历史Header、Error和Feature Flag只存在于Adapter，不能进入核心Domain。

## Issues揭示的真实问题

### 部署配置比代码性能更容易失败

Worker Route、Mail Domain、Pages API Base、Email Routing、Actions Secret、JWT Secret和DNS/MX组合复杂。

- https://github.com/dreamhunter2333/cloudflare_temp_email/issues/607
- https://github.com/dreamhunter2333/cloudflare_temp_email/issues/1044
- https://github.com/dreamhunter2333/cloudflare_temp_email/issues/515

MailWisp必须明确区分Web Domain、API Domain、SMTP Hostname和Managed Mail Domain，并提供自动DNS/MX诊断。

### Network Error吞掉真实根因

CORS、522、旧部署Cache和后端异常都可能被前端显示为同一个Network Error。

- https://github.com/dreamhunter2333/cloudflare_temp_email/issues/829
- https://github.com/dreamhunter2333/cloudflare_temp_email/issues/1077

MailWisp需要稳定Problem Details、Request ID、可复制诊断信息和依赖级Readiness。

### 大邮件破坏一致性

D1出现 `SQLITE_TOOBIG` 时，通知可能已发出而邮件本体没有持久化。

- https://github.com/dreamhunter2333/cloudflare_temp_email/issues/823

MailWisp必须先完成Durable Persistence，再触发Telegram、Webhook、AI或其他Integration；Raw MIME与大Attachment不能无上限堆入主数据库行。

### SMTP/IMAP兼容不能虚张声势

项目的Python Proxy将HTTP/JWT适配为SMTP/IMAP，但没有完整Read/Unread模型。能让客户端登录不等于完整IMAP实现。

- https://github.com/dreamhunter2333/cloudflare_temp_email/issues/609
- https://github.com/dreamhunter2333/cloudflare_temp_email/issues/602
- https://github.com/dreamhunter2333/cloudflare_temp_email/issues/839
- https://github.com/dreamhunter2333/cloudflare_temp_email/issues/1074

MailWisp若未来提供IMAP，必须明确支持的RFC子集和Contract，不做模糊宣传。

## 值得复用

- 地址Credential让无正式账户的临时邮箱易于使用。
- User与Mailbox解耦，可绑定多个地址。
- Admin、User、Address三种使用视角。
- Raw与Parsed Message分离。
- Parsed Mail API方便Agent与轻量客户端。
- Domain/Prefix Role、Turnstile、IP Limit、Daily Quota、Webhook和多Integration的产品经验。
- Mailpit驱动的SMTP E2E测试思路。
- Integration失败不阻断核心持久化。
- DOMPurify与Sandbox隔离邮件HTML。

## 直接舍弃

- Cloudflare平台深绑定。
- D1 Schema与无外键关系。
- 多套历史Authentication Header进入核心。
- 无过期、难撤销的永久Address JWT。
- 客户端SHA-256密码方案。
- Raw MIME永久大BLOB单行存储。
- 双轨Migration。
- 默认Wildcard CORS。
- 未持久化成功先发送外部通知。
- 只实现少量命令便宣称完整SMTP/IMAP兼容。
- 无日志证据时输出确定性自动诊断。

## 对MailWisp的结论

Cloudflare Temp Email不适合作为MailWisp运行架构蓝本，但非常适合作为功能需求库、历史风险样本和Compatibility Contract来源。

最优复用方式是：提取高价值产品行为和API Contract，通过可关闭Adapter兼容；核心数据、Authentication、Error、Migration与Mail Persistence重新设计。
