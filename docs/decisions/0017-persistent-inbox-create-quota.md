# ADR 0017：匿名Inbox创建使用HMAC身份与PostgreSQL日配额

状态：已接受
日期：2026-07-15

## 背景

进程内Token Bucket适合限制瞬时创建速率，但重启后状态清零，也无法限制攻击者在一整天内持续创建大量空Inbox。Redis不是Reference Profile的默认事实源；直接长期保存Plaintext IP又会扩大隐私与数据泄漏风险。

## 决策

- Canonical、DuckMail、YYDS与Cloudflare Temp Email的匿名创建入口统一经过同一持久日配额；Adapter只保留各自Error Envelope，不建立独立计数模型。
- 可信Proxy解析后的客户端IP先规范化为4-byte IPv4或16-byte IPv6，再使用独立256-bit Key执行Domain-separated HMAC-SHA-256。PostgreSQL只保存32-byte摘要，不保存Plaintext IP。
- 默认每个身份每个UTC日最多创建100个Inbox；`MAILWISP_CREATE_DAILY_LIMIT`允许部署者在1到1,000,000之间调整。
- PostgreSQL通过`INSERT ... ON CONFLICT DO UPDATE ... WHERE used < limit RETURNING`原子消费配额，并发请求不能共同穿透上限。
- 达到日上限统一返回HTTP 429，并提供`RateLimit-Limit`、`RateLimit-Remaining`、`RateLimit-Reset`与`Retry-After`。
- 每次成功消费在同一事务内最多删除100个早于当前UTC日两天的旧Bucket。没有创建流量时不会产生新数据；流量恢复后会继续有界清理。
- 配额统计通过JSON、必要字段与基础Transport校验后的创建尝试。后续地址冲突、Credential计算或业务持久化失败仍消耗本次额度，不执行隐式Refund，避免Crash与并发边界产生可绕过的双重状态。
- HMAC Key通过独立Secret文件注入。`serve`缺少Key时拒绝启动；Migration、Backup、Restore等离线Role不要求该Secret。
- Docker Compose是Canonical主推荐部署路径，并通过独立Docker Secret注入HMAC Key；Host-native只保留辅助配置。

## 影响

- 有效创建请求在进入地址生成、Argon2id或Compatibility Mapping前增加一个短PostgreSQL事务。
- HMAC Key轮换会改变所有Identity Digest，相当于重置当日配额；旧摘要在后续成功创建流量驱动下有界清理，长期静默实例可能保留更久，运维清理不得依赖固定72小时承诺。
- HMAC降低数据库内容被单独读取时的IP枚举风险，但数据库与外部HMAC Key同时失陷仍属于部署信任边界失陷。
- 共享公网出口、CGNAT或企业Proxy下的用户共享日额度。Reference默认100次优先保护个人服务器；需要公共大规模服务时应结合Edge身份、Captcha或账户配额重新建模。
- 配额在HTTP Admission层生效；内部维护代码和Repository测试不会伪造客户端IP，也不会被第三方Adapter命名污染。
