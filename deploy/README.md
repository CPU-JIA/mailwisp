# 部署资源

本目录用于保存固定版本的构建、Docker Compose、Web Edge和Postfix集成资源。生产Secret与Host专用`.env`不得提交。

- `compose/`是主推荐、Canonical单机Reference Profile。
- `reference/`是Host-native辅助Profile，适合需要systemd深度集成的维护者。
- `postfix-test/`只用于固定Postfix真实Integration，不是生产配置。

默认Profile不启用Redis、PgBouncer或消息队列。其他Deployment Profile必须通过独立ADR、故障演练和资源测量后增加。
