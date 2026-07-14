# ADR 0005：不透明Token语法与生命周期

状态：提议中，等待认证实现与安全测试
日期：2026-07-15

## 背景

MailWisp需要区分个人访问令牌、临时邮箱Capability、浏览器Session与Webhook签名密钥。Canonical Token必须满足：

- 一眼可以识别为MailWisp凭据，便于日志脱敏与Secret Scanner发现。
- 不依赖JWT即可撤销、轮换、缩小Scope和绑定资源生命周期。
- 泄露数据库时不能直接恢复或使用明文Token。
- 语法可以版本化，未来升级而不猜测Token类型。
- 第三方兼容层不能反向污染Canonical认证模型。

`sk-mailwisp-32hex`会模仿其他厂商的Secret Key命名，32位Hex也只有128 bit随机空间，并且缺少类型、版本与Lookup ID。`mailwisp_32hex`同样无法表达生命周期和用途。

## 决策

Canonical Opaque Token统一使用：

```text
wisp_<type>_v1_<kid>_<secret>
```

`wisp`保留MailWisp的品牌与“过时即逝”意象；它只负责识别，不承担任何安全强度。安全性全部来自CSPRNG生成的`kid`与`secret`。

### Token类型

| Type | 用途 | 主要边界 |
| --- | --- | --- |
| `pat` | Personal Access Token | 代表账户，必须绑定Scope与到期时间 |
| `cap` | Inbox Capability Token | 只授权单个或明确集合的临时邮箱资源 |
| `ses` | Browser Session Token | 只进入HttpOnly Cookie，不暴露给前端脚本 |
| `whsec` | Webhook Signing Secret | 只预留语法与扫描边界，不作为Bearer Token；持久化方案另行决策 |

未经ADR扩展不得临时创造新的Type。`admin`、`service`和`refresh`等名称不预留实现，实际需要出现后再证明边界。

### 编码

- `kid`：12字节`crypto/rand`，编码为24个小写Hex字符。
- `secret`：32字节`crypto/rand`，编码为43个无Padding Base64URL字符。
- `kid`是非秘密Lookup ID；`secret`提供256 bit随机强度。
- Token不允许前后空白、大小写折叠、Unicode替代字符或宽松解码。
- V1完整语法：

```text
^wisp_(pat|cap|ses|whsec)_v1_[0-9a-f]{24}_[A-Za-z0-9_-]{43}$
```

文档、测试与日志不得放入能够通过该正则的示例Secret。需要展示结构时只使用`<kid>`和`<secret>`占位符。

### Bearer Token存储与验证

PAT、Capability与Session只在签发响应中展示一次。数据库保存：

- Internal UUIDv7 ID。
- `kid`与Token Type。
- 32字节Secret Digest。
- Subject、Audience与规范化Scope。
- `created_at`、`expires_at`、`last_used_at`与`revoked_at`。
- 可选的创建者、说明与轮换关系。

V1 Digest定义为：

```text
SHA-256(
  "mailwisp-token-v1\x00" ||
  type || "\x00" ||
  kid || "\x00" ||
  raw_secret
)
```

`raw_secret`是Base64URL解码后的32字节，不是编码文本。Token具备256 bit不可预测熵，因此不使用面向低熵人类密码的bcrypt或Argon2；增加高成本Password KDF不会提升可度量的抗暴力能力，反而扩大认证CPU DoS面。Digest比较必须使用Constant-time Compare。

`whsec`不适用上述不可逆Digest存储：MailWisp作为Webhook发送方需要取得Key Material生成HMAC。V1只锁定其Token Grammar与Secret Scanner识别；正式Webhook实现前必须通过独立ADR选择受控加密存储或由部署主密钥派生，不能把Digest误当作可签名密钥，也不能以明文列临时绕过。

验证顺序固定为：

1. 严格解析Prefix、Type、Version、长度和字符集。
2. 通过`type + kid`查询唯一Token Record。
3. 计算Domain-separated Digest并进行常量时间比较。
4. 检查撤销、到期、Subject、Audience和Scope。
5. 更新`last_used_at`时不得让认证主路径等待高频同步写入；具体采样或异步策略另行验证。

对外只返回统一认证失败，不暴露“Kid存在”“已到期”或“Digest错误”的差异。结构化安全日志最多记录Type、Kid、结果分类和Request ID，禁止记录完整Token或Secret。

### 生命周期

- PAT默认90天到期，V1最大365天，不签发永久PAT。
- Capability到期时间不得晚于对应Inbox或资源集合的最早失效时间。
- Browser Session在登录、提权、密码或MFA状态变化时轮换；Cookie使用`__Host-`前缀、`Secure`、`HttpOnly`和明确`SameSite`。
- Webhook Signing Secret未来必须允许新旧两把Key短期重叠，验证端通过Kid支持无停机轮换；其Key Material存储尚未在本ADR接受。
- 撤销立即生效；Redis若以后加入，只能缓存撤销结果，不能成为唯一真相源。

### 兼容层

DuckMail、YYDS或Cloudflare兼容Adapter可以接受其Contract要求的Token位置和错误格式，但进入应用层后必须转换为Canonical Principal、Scope与Capability。兼容需求不得迫使Canonical API签发永久JWT、客户端Hash密码或第三方风格Secret。

## 暂不采用

- `sk-mailwisp-*`或其他厂商前缀仿制。
- 32位Hex作为完整Secret。
- 永久JWT Bearer Token。
- 数据库明文、可逆加密或只保存Token尾部。
- 将Token放入URL Query、Local Storage、日志、Metric Label或Trace Attribute。
- 仅依赖Prefix判断Token类型或权限。

## 接受条件

状态改为“已接受”前必须完成：

- Go Token Package使用`crypto/rand`，任何随机源失败都拒绝签发。
- Grammar Parser的Table Test、Property Test与Fuzz Test。
- 签发、验证、Scope、到期、撤销、轮换和并发使用Integration Test。
- 数据库中不存在明文Token的Migration与Repository Test。
- 日志、HTTP Error、Telemetry与Audit Event泄漏测试。
- Gitleaks自定义Rule与GitGuardian验证能够识别V1 Grammar。
- Capability Token与Canonical API授权模型共同完成Threat Model。
- Webhook Signing Secret在正式实现前完成独立Key Material存储ADR。
- DuckMail、YYDS和Cloudflare Adapter使用Contract Test证明不会绕过Canonical授权。

## 2026-07-15阶段性实现

当前开发分支已经实现：

- 四种V1 Grammar的CSPRNG生成、严格Parser、Strict Base64URL与Domain-separated Digest原语；
- Inbox Capability的Digest-only PostgreSQL持久化、Scope、到期、撤销和原子Rotation；
- Capability只绑定单个Inbox，并实时检查Inbox状态与到期时间；
- 自定义Gitleaks规则与运行时Scanner Probe；
- Unit、Race、Fuzz与固定PostgreSQL Integration测试。

PAT、Browser Session和Webhook Signing Secret Repository尚未实现；本阶段不得因为Grammar可解析就宣称这些Credential已经可用。完整远端门禁通过前，本ADR仍保持“提议中”。

在上述证据完成前，本ADR只锁定候选Grammar，不代表认证系统已经完成或安全结论已经成立。
