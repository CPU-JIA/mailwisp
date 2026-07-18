# Inbox Capability Threat Model

- 状态：Capability、Canonical公共API、Browser Session/CSRF与单PostgreSQL事实源的持久创建配额已实现；多Region身份与Challenge尚未纳入
- 日期：2026-07-18
- 范围：Canonical `wisp_cap_v1_<kid>_<secret>`、`inbox_capabilities`、Capability Service与浏览器Session交换边界

## 资产与安全目标

需要保护：

- Capability Plaintext与32字节Raw Secret；
- Inbox中的Message Metadata、Parsed Body、Raw MIME与Attachment；
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
| Plaintext IP进入持久层 | 可信Proxy解析后的IP先规范化，再用独立256-bit Key执行Domain-separated HMAC-SHA-256；PostgreSQL只保存32-byte摘要与UTC日桶 |
| 进程重启绕过创建限额 | 瞬时Token Bucket外增加PostgreSQL原子日计数；Canonical与全部兼容创建入口共享同一事实源 |
| 并发穿透日上限 | `INSERT ... ON CONFLICT DO UPDATE ... WHERE used < limit RETURNING`保证单身份单UTC日原子消费 |
| 配额Key与Session Key复用 | Compose与Host-native均要求独立Secret；轮换Quota Key会重置当日Identity，不能作为无影响的常规轮换 |
| 数据库与Quota Key同时泄露 | 攻击者可对候选IP执行离线枚举；两者同时失陷属于部署信任边界失陷，必须同时轮换Key、保护Backup并审计访问 |
| 复制旧Browser Session后退出 | Browser Session是AEAD保护的Stateless Cookie；退出只清除当前浏览器副本，已复制Cookie在到期或Inbox删除前仍可Replay。控制台不把Cookie暴露给JavaScript，Session最长受Capability与Inbox到期约束；删除Inbox是权威全局失效点 |
| 日志形成长期行为轨迹 | Go HTTP日志只记录Method、Route Path、Status、Duration与Request ID，不记录Query、IP、邮箱或Token；LMTP Go日志不记录远端地址。Postfix仍可能记录Envelope与Queue元数据，Compose使用有界轮转，部署者必须限制访问并按隐私策略保留 |

## 不在本阶段解决

- 匿名Inbox创建API使用可信Proxy CIDR解析客户端IP；进程内Token Bucket和本地Content Fence都以Singleton App为边界，PostgreSQL Advisory Lease会拒绝第二个`serve`进程。多Replica不是当前支持能力，必须先更换共享Storage、限流和跨进程删除协议并通过ADR。
- 共享出口、CGNAT与企业Proxy会共享日额度；默认100次/日优先保护个人服务器，不声称能区分同一公网IP后的自然人。
- 旧HMAC摘要由后续成功创建流量按每次最多100行清理。活跃实例通常只保留当前日及前两日Bucket；长期静默实例可能保留更久，不能将其描述为严格TTL。
- 配额统计通过基础Transport校验后的创建尝试，后续业务失败不退款；这是避免并发与Crash绕过的有意Fail-closed边界。
- 当前没有Captcha或Proof-of-Work；遭遇持续自动化滥用时必须在不泄漏Token和不破坏Accessibility的前提下增加可配置Challenge。
- PAT与Account权限尚未实现；Browser Session只面向一个匿名Inbox，不扩展为账户登录。
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

Capability、Canonical API、Rate Limit、资源Ownership、Browser Session/CSRF与Adapter Contract由仓库固定的Unit、Race、Fuzz、PostgreSQL Integration、Gitleaks/GitGuardian及Linux/Windows工作流共同验证；每次PR与合并后`main`都必须重新通过，单个历史Run不替代当前门禁。
