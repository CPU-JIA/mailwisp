# 部署资源

本目录用于保存固定版本的构建、宿主机Web Server配置和Postfix集成资源。生产Secret与Host专用 `.env` 不得提交。

- `postfix-test/`只用于固定Postfix真实Integration，不是生产配置。
- `reference/`是单台Linux个人服务器的Host-native Reference Profile，包含Nginx、Certbot、Postfix、PostgreSQL、systemd、Retention Timer和版本锁。

Reference Profile不启用Redis、PgBouncer或消息队列。其他Deployment Profile必须通过独立ADR、故障演练和资源测量后增加。
