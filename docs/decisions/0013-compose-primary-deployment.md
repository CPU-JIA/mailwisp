# ADR 0013：Docker Compose作为主推荐部署方式

状态：已接受，取代ADR 0011中的默认Profile排序
日期：2026-07-15

## 背景

MailWisp面向个人维护者，但可复现部署、版本锁、隔离边界、迁移顺序和跨机器恢复比极少量Host-native资源节省更重要。用户明确要求Docker Compose作为主推荐方式，其他方式为辅。

## 决策

- `deploy/compose/`成为Canonical单机Reference Deployment；README、本地验收、GitHub Actions Container Build和Release均优先验证该Profile。
- Compose固定Go/Node构建镜像、App Runtime、Nginx、Postfix、PostgreSQL与Certbot的精确Tag和OCI Index Digest。
- 服务边界为`edge`、`postfix`、`app`、一次性`migrate`、`postgres`与工具Profile `certbot`。Reference Profile仍不引入Redis、PgBouncer、Broker或生产Node.js。
- PostgreSQL密码通过Compose Secret文件进入PostgreSQL和App；应用支持`MAILWISP_POSTGRES_PASSWORD_FILE`，不要求把密码拼入Compose文件或进程环境。
- 只有Edge HTTP(S)与Postfix SMTP发布Host端口。App、LMTP和PostgreSQL只连接内部Network。
- 证书保存在命名卷中，由Nginx与Postfix只读共享。Certbot续签后，Host编排先执行两项配置检查，再Reload对应容器。
- `deploy/reference/`Host-native Profile保留为辅助方案，不再作为默认安装路径。

## 影响

- 主部署需要Docker Engine与Compose v2，但获得一致的构建、依赖启动顺序、Healthcheck、资源上限、数据卷和回滚边界。
- Host-native仍可用于熟悉systemd/Postfix运维且追求最少容器层的维护者，但文档与CI优先级低于Compose。
- 容量建议以Compose Profile实测为准；Host-native差异单独记录，不混用性能结论。
