# MailWisp Docker Compose Deployment

Docker Compose是MailWisp默认、主推荐的单机部署方式。它固定应用构建链、Nginx、Postfix、PostgreSQL与Certbot镜像Digest，只公开`25/tcp`、`80/tcp`和`443/tcp`；Go HTTP、LMTP和PostgreSQL仅存在于Compose内部网络。

Host-native配置保留在`deploy/reference/`，只作为需要深度系统集成时的辅助Profile。

## 1. 准备配置

```bash
cd deploy/compose
cp .env.example .env
cp mailwisp.env.example mailwisp.env
install -d -m 0700 secrets backups
openssl rand -base64 32 > secrets/postgres_password.txt
openssl rand -base64 32 > secrets/browser_session_key.txt
openssl rand -base64 32 > secrets/create_quota_hmac_key.txt
chmod 0600 secrets/postgres_password.txt
chmod 0600 secrets/browser_session_key.txt
chmod 0600 secrets/create_quota_hmac_key.txt
sudo chown -R 65532:65532 backups
```

编辑`.env`中的Web域名、SMTP Host、收件域名和证书名称；编辑`mailwisp.env`中的公开域名与LMTP Host。Browser Session Key与Create Quota HMAC Key通过独立Docker Secret文件注入，不写入普通环境变量或Git。

匿名创建先经过进程内瞬时Token Bucket，再经过PostgreSQL持久UTC日配额。默认每个HMAC客户端身份每天100次：

```dotenv
MAILWISP_CREATE_DAILY_LIMIT=100
```

数据库只保存HMAC-SHA-256身份摘要，不保存Plaintext IP；HMAC Key必须独立生成并纳入Secret备份。轮换Key会立即切换到新的Quota Identity，等同于重置当日客户端计数，必须作为有意的运维动作执行。

`mailwisp.env`默认把每个Inbox限制为500条Message和256 MiB逻辑存储：

```dotenv
MAILWISP_INBOX_MAX_MESSAGES=500
MAILWISP_INBOX_MAX_STORAGE_BYTES=268435456
```

逻辑存储按Inbox中的每条Message累计Raw MIME大小，不按Content Store物理去重后的磁盘大小计算。该限制用于隔离单Inbox滥用，不替代主机磁盘水位监控。

Content Store默认额外保留1 GiB文件系统可用空间：

```dotenv
MAILWISP_CONTENT_MIN_FREE_BYTES=1073741824
```

MailWisp会在LMTP DATA前预检，并在Content Store写入前为一个最大消息窗口执行并发预留。磁盘压力返回`452 4.3.1`，由Postfix保留Queue并重投。部署者仍应监控Docker数据目录；不得把该水位设为小于`MAILWISP_LMTP_MAX_MESSAGE_BYTES`。

DNS至少包含Web/SMTP Host的A/AAAA记录和收件域名MX记录，云厂商必须允许公网25端口。

## 2. 首次证书

首次启动Edge前，使用固定Certbot容器独占80端口签发同时覆盖Web与SMTP Host的证书：

```bash
docker compose --profile tools run --rm --service-ports certbot \
  certonly --standalone \
  -d mail.example.com -d mx.example.com
```

证书保存在`letsencrypt`命名卷中，由Nginx与Postfix只读共享。

## 3. 构建与启动

```bash
docker compose build --pull
docker compose up -d
docker compose ps
docker compose logs --tail=100 app postfix edge
```

`migrate`是一次性服务；`app`只有在Migration成功后启动，Edge和Postfix只有在App Readiness通过后启动。默认不运行Redis、PgBouncer、消息队列或生产Node.js。

Docker Hub链路较慢时，应在Host Docker daemon配置可信`registry-mirrors`或组织级Pull-through Cache。MailWisp仍使用官方镜像名和锁定Digest，不在Compose中硬编码地域Mirror，也不接受只保留Tag、无法证明与原Digest一致的重打包镜像。Mirror只改变传输路径，不改变镜像身份。

内部Metrics不通过Edge公开。临时诊断可执行：

```bash
docker compose exec app wget -qO- http://127.0.0.1:8080/metrics
```

长期采集应让既有Prometheus或兼容Collector加入Compose内部Network，不得把`/metrics`直接暴露到公网。

## 4. 证书续签

由Host Cron或systemd Timer定期执行：

```bash
docker compose --profile tools run --rm certbot \
  renew --webroot -w /var/www/certbot
docker compose exec edge nginx -t
docker compose exec postfix postfix check
docker compose exec edge nginx -s reload
docker compose exec postfix postfix reload
```

只有配置检查通过后才执行Reload。续签失败不得删除当前证书卷。

## 5. 备份与恢复

创建一致性备份前停止收件入口与App：

```bash
docker compose stop edge postfix app
docker compose run --rm app backup /backups/mailwisp-$(date +%Y%m%d-%H%M%S)
docker compose up -d
```

恢复必须使用新的空PostgreSQL卷与空Content卷，并先在隔离环境演练：

```bash
docker compose run --rm app restore /backups/<bundle-directory>
```

不得在已有数据卷上直接覆盖恢复。

## 6. 上线验收

- `docker compose config`无错误且所有生产镜像均固定Tag与Digest；
- `curl --fail https://mail.example.com/readyz`；
- 浏览器完成创建、Session/Token、收件、正文、附件与删除流程；
- 外部SMTP投递后可从API读取，未知Recipient永久失败，过载返回临时失败；
- Inbox达到Message或逻辑存储配额后返回`552 5.2.2`，且多Recipient投递不产生部分写入；
- Content Store低于安全水位时返回`452 4.3.1`，释放空间后Postfix Queue能够成功重投；
- `openssl s_client -starttls smtp -connect mx.example.com:25 -servername mx.example.com`；
- 强制重启App后Postfix Queue能够重投；
- 证书Dry Run、备份恢复、Content Reconciliation与断电恢复演练通过。

这些验收未完成前，不得把目标服务器标记为生产就绪。
