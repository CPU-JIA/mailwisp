# ADR 0013：Docker Compose作为主推荐部署方式

状态：已接受，取代ADR 0011中的默认Profile排序
日期：2026-07-15

## 背景

MailWisp面向个人维护者，但可复现部署、版本锁、隔离边界、迁移顺序和跨机器恢复比极少量Host-native资源节省更重要。用户明确要求Docker Compose作为主推荐方式，其他方式为辅。

## 决策

- `deploy/compose/`成为Canonical单机Reference Deployment；README、本地验收、GitHub Actions Container Build和Release均优先验证该Profile。
- Compose固定Go/Node构建镜像、App Runtime、Nginx、Postfix、PostgreSQL与Certbot的精确Tag和OCI Index Digest。
- 服务边界为`edge`、`postfix`、Singleton `app`、一次性`db-provision`与`migrate`、`postgres`、按需`maintenance`及工具Profile `certbot`。Reference Profile仍不引入Redis、PgBouncer、Broker或生产Node.js。
- `db-provision`在每次启动时幂等创建或收敛Runtime Role及已有对象权限，随后`migrate`使用Owner执行Schema Migration；常驻App只使用DML与Sequence权限的Runtime Role。两份独立密码通过Compose Secret文件注入，不进入Compose配置或进程参数，也不依赖空Volume初始化脚本承担升级。
- Browser Session与Create Quota HMAC使用另外两份独立Secret；Linux Secret文件由脚本创建为只读`0444`，父目录保持`0700`，使非Root容器可读而同机其他用户不可进入。
- 只有Edge HTTP(S)与Postfix SMTP发布Host端口。`database`、`lmtp`、`frontend`和`smtp_ingress`按最小通信矩阵分离；PostgreSQL、LMTP与Metrics不公开。
- 所有Service使用有界日志轮转、停止宽限与Healthcheck；用户启动前由固定Compose版本Preflight拒绝漂移工具链。
- 证书保存在命名卷中，由Nginx与Postfix只读共享。Certbot续签后，Host编排先执行两项配置检查，再Reload对应容器。
- `deploy/reference/`Host-native Profile保留为辅助方案，不再作为默认安装路径。

## 影响

- 主部署需要Docker Engine与Compose v2，但获得一致的构建、依赖启动顺序、Healthcheck、资源上限、数据卷和回滚边界。
- Host-native仍可用于熟悉systemd/Postfix运维且追求最少容器层的维护者，但文档与CI优先级低于Compose。
- 容量建议以Compose Profile实测为准；Host-native差异单独记录，不混用性能结论。
