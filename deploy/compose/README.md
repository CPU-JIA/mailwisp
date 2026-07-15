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
chmod 0600 secrets/postgres_password.txt
chmod 0600 secrets/browser_session_key.txt
sudo chown -R 65532:65532 backups
```

编辑`.env`中的Web域名、SMTP Host、收件域名和证书名称；编辑`mailwisp.env`中的公开域名与LMTP Host。Browser Session Key通过独立Docker Secret文件注入，不写入普通环境变量或Git。

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
- `openssl s_client -starttls smtp -connect mx.example.com:25 -servername mx.example.com`；
- 强制重启App后Postfix Queue能够重投；
- 证书Dry Run、备份恢复、Content Reconciliation与断电恢复演练通过。

这些验收未完成前，不得把目标服务器标记为生产就绪。
