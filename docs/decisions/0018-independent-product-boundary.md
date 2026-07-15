# ADR 0018：MailWisp作为完全独立的新产品

状态：已接受
日期：2026-07-15

## 决策

- MailWisp不迁移、不读取、不复用也不兼容旧TempMail的数据库、Raw Mail、账号、Token、配置、API、源码与UI资产。
- MailWisp只对自身发布版本提供Schema Migration、Backup、Restore与Rollback Contract。
- 产品需求、Architecture Decision、Test Fixture与Benchmark只来自MailWisp自身约束、协议标准、真实测量和第三方一手Contract。
- DuckMail、YYDS与Cloudflare Temp Email支持继续作为隔离Adapter存在，不进入Canonical Domain Model，也不构成与旧TempMail的继承关系。

## 影响

- 删除旧数据迁移工具、旧Dump恢复与旧API兼容工作项。
- `../tempmail`保持项目范围外；未经未来明确授权，不得读取、修改或删除。
- Release与完成度只验证干净安装和MailWisp自身版本升级，不再等待旧产品迁移证据。
