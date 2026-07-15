# 旧TempMail本地基线盘点

状态：进行中
证据来源：`../tempmail` 本地副本
盘点日期：2026-07-14

## 当前生产形态

旧项目使用Go API、Go LMTP、Postfix、PostgreSQL、PgBouncer、Redis和前端Nginx容器。宿主机另有公共Nginx，因此HTTP路径实际存在两层Nginx。

## 数据对象

当前SQL至少包含：

- `accounts`
- `account_tokens`
- `domains`
- `domain_health_snapshots`
- `mailboxes`
- `mailbox_state`
- `emails`
- `app_settings`
- `api_request_daily`

现有Migration从v2延续到v11，但初始化Schema与增量Migration同时演进，尚未形成新项目可直接采用的单一Versioned Migration Contract。

## API能力

旧项目已经覆盖：

- Liveness与Readiness。
- 公共设置、Logo、注册和统计。
- 域名提交、MX状态与域名健康。
- Session与Bootstrap聚合接口。
- 邮箱创建、列表、详情、保留策略、批量保留和清理。
- 邮件列表、详情、删除和清理。
- 最新邮件、验证码和链接查询。
- Token创建、编辑、轮换、启用、禁用、删除与用量。
- 管理员账户、域名、设置和自检。
- Postfix域名同步、Ingress Metrics与兼容HTTP投递内部接口。

兼容研究不能只覆盖“创建邮箱、读取邮件”两个基础Endpoint，上述能力需要区分：必须保留、可重新设计、可舍弃、仅管理端需要。

## Redis实际用途

旧项目的Redis主要用于：

- 通用HTTP Rate Limit。
- Token每分钟与每日配额计数。
- 健康检查。

Token总用量仍通过PostgreSQL原子更新。研究阶段需要判断Redis带来的低延迟收益是否足以覆盖额外Container、故障模式和运维成本。

## 后端结构问题

- `store/postgres.go` 超过800行。
- `ingress/server.go` 超过700行。
- `store/tokens.go` 超过500行。
- `main.go` 同时负责HTTP、LMTP、Job、Key文件和Lifecycle。
- DNS查询位于PostgreSQL Store文件中，领域边界不清晰。
- 管理Key写入文件并输出日志，不符合新安全规范。
- API存在 `/api` 与 `/api/v1` 双注册，兼容意图没有独立Contract测试证明。

## 前端结构问题

- 单一 `app.js` 超过4000行。
- 单一 `style.css` 已增长到8000行以上。
- 大量Inline Event Handler与字符串HTML模板。
- Dark Theme由大量分散覆盖规则实现，后期迭代出现多代页面样式叠加。
- 当前只有Light/Dark，没有系统化i18n与Design Token Contract。

这些问题说明新前端必须先确定Component、State、i18n和Theme边界，不能从旧CSS继续累加。

## 兼容迁移风险

- 现有数据库Dump包含真实数据，需要只在受控本地环境恢复。
- 邮件表同时存在解析正文、HTML、Raw Message和Raw Path字段，需要确认真实使用比例。
- `mailbox_state` 是性能快照还是业务事实需要重新定义。
- Token Hash、Scope、Quota和主Token规则必须逐项提取Invariant。
- 域名Pending/Active/Disabled与MX重新检测行为必须建立状态机测试。
- 旧API的错误状态和Ownership行为需要Contract Capture，不能只从路由名称推测。

## 下一步证据

- 从SQL与Repository提取字段级数据字典和外键关系。
- 从真实Dump只读恢复后统计表行数、尺寸、Null分布和Raw Message占用。
- 为旧API建立Golden Contract Fixture。
- 使用本地SMTP样本记录LMTP成功、临时失败和未知收件人的行为。
