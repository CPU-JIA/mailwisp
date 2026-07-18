# ADR 0012：Capability交换为加密HttpOnly浏览器Session

状态：已接受
日期：2026-07-15

## 背景

Canonical Capability适合CLI和自动化，但浏览器长期把Bearer明文保存在JavaScript内存中会扩大XSS窃取窗口，也无法在刷新后恢复登录。把Capability写入Local Storage或普通Cookie都不满足安全目标。

## 决策

- `POST /api/v1/session`仅接受Authorization Header中的有效Capability，并交换为短生命周期Browser Session。
- Session使用AES-256-GCM加密认证，内容只包含Inbox ID、Scope、到期时间与CSRF Digest，不包含Capability明文。
- Session只使用`Secure`、`HttpOnly`、`SameSite=Lax`、`Path=/`的`__Host-mailwisp_session` Cookie。独立随机CSRF Token由Session交换响应返回并只保留在页面内存，状态修改请求必须提交`X-MailWisp-CSRF`。
- 页面刷新后，`GET /api/v1/session`使用HttpOnly Cookie恢复身份，同时轮换Session并签发新的内存CSRF Token；服务端不创建任何JavaScript可读认证Cookie。
- `MAILWISP_BROWSER_SESSION_KEY`没有默认值。未配置时Session路由保持关闭，Canonical Bearer API继续可用。
- Session最长七天，默认十二小时，且绝不会超过原Capability到期时间。轮换主密钥会立即使现有Session失效。
- Bearer请求不使用Cookie身份，也不要求CSRF；Cookie与Capability保持两套清晰Transport语义。

## 安全边界

- CSRF Token使用Domain-separated SHA-256 Digest封入Session，并以Constant-time Comparison验证。跨站页面既无法读取响应，也无法构造自定义CSRF Header。
- Cookie Payload由AEAD防篡改与保密；任何解析、版本、到期或认证失败统一映射为Unauthenticated。
- Inbox生命周期仍由PostgreSQL事实状态约束；删除或过期Inbox后，即使Session尚未到期也无法访问对象。
- Session不是永久PAT，不支持跨站Cookie、Domain Cookie或URL Token。

## 运维影响

Reference环境必须生成32字节随机Key并以Base64或Base64URL保存。Canonical Compose通过只挂载给App的`browser_session_key` Secret文件注入；Host-native辅助Profile可使用权限受控的EnvironmentFile或`MAILWISP_BROWSER_SESSION_KEY_FILE`。Key轮换当前采用中断现有浏览器Session的简单语义，符合临时邮箱产品边界。
