# ADR 0011：Reference Profile采用Nginx、Certbot与Host-native服务

状态：部分取代；Host-native技术选择仍有效，默认Profile排序由ADR 0013取代
日期：2026-07-15

## 背景

MailWisp需要同时处理HTTPS和公网SMTP TLS。只比较Web反向代理的配置长度会忽略Postfix证书共享、续签Reload和故障排查。本ADR保留Host-native辅助Profile的技术选择；主推荐部署已由ADR 0013调整为Docker Compose。

## 决策

- Host-native辅助Profile使用长期支持Linux与systemd；Go应用、Nginx、Postfix和PostgreSQL直接运行在Host。
- Web Edge选择Nginx 1.30.4稳定线。Certbot 5.6.0使用Webroot签发同时覆盖Web与SMTP Host的证书。1.30.4替代1.30.3是因为Final Edge Image扫描确认旧Digest包含已有修复版本的`c-ares`、`curl/libcurl`与`libexpat` HIGH CVE；升级仍固定Docker Official Image Digest并由Release Trivy门禁复核。
- Nginx直接读取Let’s Encrypt证书；Certbot Deploy Hook把证书原子安装到`/etc/postfix/tls`后先执行配置检查，再Reload Nginx和Postfix。
- Postfix只负责公网SMTP、持久Queue、TLS与LMTP重投；Go应用继续只监听Loopback HTTP和LMTP。
- 前端构建为静态Content Hash Asset，由Nginx提供；生产环境不运行Node.js。
- PostgreSQL 18.4是唯一持久化事实来源；Reference Profile不启用Redis、PgBouncer或消息队列。
- 过期Inbox通过systemd Timer调用`mailwisp cleanup`。每批事务有上限并使用PostgreSQL Advisory Lock，多个误启动实例不会并发清理。

## 为什么不是Caddy

Caddy在单一HTTPS服务上具有更低配置成本，但其自动证书存储不是Postfix的稳定接口。为SMTP复制证书、处理Issuer路径变化和可靠Reload仍需额外同步层。Nginx加Certbot虽然组件多一个，但Certificate Lineage和Deploy Hook是明确、可测试且长期常见的运维边界。Extended Profile仍可提供Caddy模板，但不是当前Reference默认。

## 影响

- Native Profile空闲内存和故障面更小，也要求维护者使用发行版安全更新与Release Lock核对版本。
- Docker Compose是ADR 0013确定的Canonical主推荐Profile，并承担优先的备份、恢复、证书与故障演练；本ADR中的Host-native方案仅保留为辅助Profile。
- 生产发布前仍必须在目标Linux执行Nginx/Postfix配置检查、真实ACME续签Dry Run、SMTP STARTTLS、HTTP Security Header和断电恢复演练。
