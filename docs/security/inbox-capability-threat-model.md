# Inbox Capability Threat Model

- 状态：当前实现边界，等待完整远端门禁
- 日期：2026-07-15
- 范围：Canonical `wisp_cap_v1_<kid>_<secret>`、`inbox_capabilities`与Capability Service

## 资产与安全目标

需要保护：

- Capability Plaintext与32字节Raw Secret；
- Inbox中的Message Metadata、Parsed Body、Raw MIME与未来Attachment；
- Credential Scope、到期、撤销和Rotation关系；
- 数据库Dump、日志、Trace、Metric、错误响应与CI Artifact。

安全目标：持有有效Capability才能访问其绑定的单个Inbox；数据库泄露不能直接恢复Bearer Token；撤销和Rotation立即生效；任何Scope或资源状态不明确时默认拒绝。

## Trust Boundary

```text
Untrusted Client
    -> HTTPS Edge
        -> Go HTTP Authorization Boundary
            -> Capability Service
                -> PostgreSQL Digest Record
                -> Inbox Lifecycle State
```

Plaintext只允许存在于签发响应、客户端受控存储和单次请求的短生命周期内存中。PostgreSQL、结构化日志、Metric、Trace、Backup Manifest和Content Store都不是Plaintext Token存储位置。

## 威胁与控制

| 威胁 | 控制 |
| --- | --- |
| 低熵或可预测Token | `kid` 12字节、Secret 32字节，全部使用`crypto/rand`；随机源失败拒绝签发 |
| Grammar混淆或宽松解析 | 固定Prefix、Type、Version、长度与ASCII字符集；Strict无Padding Base64URL；拒绝空白、大小写折叠和未知Type |
| 数据库泄露直接获得访问权 | 只保存Domain-separated SHA-256 Digest、非秘密KID、Scope与生命周期，不保存Encoded或Raw Secret |
| KID枚举 | KID不承担认证；未知KID、错误Digest、到期、撤销和失效Inbox对外统一为Unauthenticated |
| Digest比较Timing Leak | 使用`crypto/subtle.ConstantTimeCompare` |
| 跨Token Type替换 | Digest包含Domain、Type、KID和Raw Secret；Capability验证拒绝PAT、Session与Webhook Type |
| Scope提升或默认允许 | Scope使用受约束Bitmask；空集合、未知Bit和缺失Required Scope均拒绝 |
| 跨Inbox访问 | Credential Record只绑定一个`inbox_id`；Principal不接受客户端提供的替代Subject |
| Capability晚于Inbox失效 | Repository条件写入保证Capability不晚于Inbox；每次认证重新检查当前Inbox状态和`expires_at` |
| Rotation竞态产生多个有效Token | Transaction锁定旧Credential；同一`rotated_from_id`唯一；旧Token与新Token提交在同一Transaction |
| Grace Period导致旧Token继续可用 | Rotation立即写入`revoked_at`，不提供默认重叠窗口 |
| 日志或源码泄漏 | Token类型不实现`fmt.Stringer`；日志最多使用Type、KID、结果分类和Request ID；Gitleaks自定义规则及运行时Probe |
| URL、Referer或Proxy泄漏 | Canonical API只允许Authorization Bearer或受控HttpOnly Cookie；禁止Query Token |
| Credential Replay | Capability是Bearer Credential，TLS是必需边界；撤销与短生命周期降低暴露窗口，但不能消除已窃取Token在到期前的Replay |
| 数据库故障伪装成无效Token | Repository Not Found映射为Unauthenticated；连接或查询故障保留内部错误，由Transport返回5xx而不是401 |

## 不在本阶段解决

- 未开放匿名Inbox创建API，因此注册/IP滥用、Captcha和公共Rate Limit尚未进入攻击面；正式开放前必须补齐。
- PAT、Browser Session与Account权限尚未实现。
- `whsec`只实现Grammar和Scanner识别；服务端HMAC Key Material必须通过独立加密存储或主密钥派生ADR。
- 客户端如何安全保存Capability不由服务端完全控制；Web Console不得使用Local Storage保存长期Bearer Token。
- 主机Root权限、进程内存抓取或TLS终止层完全失陷可以获取请求中的Plaintext，这属于部署信任边界失陷。

## 验证证据要求

- Token Grammar Table Test、随机源失败、Round-trip、Property与Fuzz；
- Scope默认拒绝、错误Secret、错误Type、到期、撤销、禁用/过期Inbox测试；
- PostgreSQL中无Plaintext、Digest公式、KID唯一与Scope Constraint测试；
- 并发Rotation只有一个Replacement成功，旧Token立即失效；
- v2带现有Inbox数据升级到v3不丢失；
- Gitleaks Probe必须发现运行时构造的V1 Token，同时Working Tree与Git History零泄漏；
- govulncheck、gosec、GitGuardian、Linux与Windows Race门禁通过。
