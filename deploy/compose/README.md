# MailWisp Docker Compose Deployment

Docker Compose是MailWisp默认、主推荐的单机部署方式。它固定应用构建链、Nginx、Postfix、PostgreSQL与Certbot镜像Digest，只公开`25/tcp`、`80/tcp`和`443/tcp`；Go HTTP、LMTP和PostgreSQL仅存在于Compose内部网络。Postfix单独加入非内部`SMTP ingress`网络以接收发布端口流量，并通过内部backend把邮件交给LMTP；数据库与App不会加入SMTP入口网络。

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

从Git Checkout部署时使用Canonical Source Build：

```bash
docker compose build --pull app maintenance edge postfix
docker compose up -d
docker compose ps
docker compose logs --tail=100 app postfix edge
```

`migrate`是一次性服务；`app`只有在Migration成功后启动，Edge和Postfix只有在App Readiness通过后启动。默认不运行Redis、PgBouncer、消息队列或生产Node.js。

从正式Release Bundle部署时，必须先在Bundle根目录验证Checksum并加载随包镜像，再复制`.env.example`：

```bash
docker load --input images/mailwisp-images-linux-amd64.tar
cd deploy/compose
cp .env.example .env
docker compose config --quiet
docker compose up -d --no-build
```

Release Bundle的`.env.example`通过`COMPOSE_FILE=compose.yaml:release.compose.yaml`启用预构建Overlay；该Overlay用`!reset null`删除`migrate`、`app`、`maintenance`、`edge`与`postfix`的全部`build`，并设置`pull_policy: never`。因此Release运行路径不能因源码缺失或Registry波动回退到未审查构建或拉取同名远程Tag；本地镜像缺失必须直接失败。Source Checkout不得复制这条`COMPOSE_FILE`设置，两种路径不能混用。完整步骤见[Release Bundle说明](../../docs/release-bundle.md)。

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

先按[Compose运维Runbook](OPERATIONS.md)在维护窗口前构建并记录镜像，临时阻断Host/云防火墙的`25/tcp`；停止Postfix后必须从同一Queue Volume再检查一次，随后才停止App并创建一致性备份：

```bash
docker compose exec postfix postqueue -p
docker compose stop edge postfix
docker compose run --rm --no-deps --entrypoint postqueue postfix -p
docker compose stop app
docker compose run --rm --no-deps maintenance backup /backups/mailwisp-$(date +%Y%m%d-%H%M%S)
docker compose -f backup-verifier.compose.yaml run --rm --no-deps backup-verifier \
  backup verify /backups/<bundle-directory>
```

完成后按Runbook恢复服务、显式重新放行`25/tcp`并执行外部SMTP验证。

恢复必须使用新的空PostgreSQL卷与空Content卷，并先在隔离环境演练：

```bash
docker compose run --rm --no-deps maintenance restore /backups/<bundle-directory>
```

Maintenance固定使用PostgreSQL 18.4官方Client且以UID 65532运行；常驻App不包含数据库工具，也不挂载Backup目录。独立Verifier Compose不解析生产域名、数据库配置或Secret，只把Backup目录只读挂载并禁用网络。不得在已有数据卷上直接覆盖恢复，生产切换、升级回滚、Queue与离机备份边界见[Compose运维Runbook](OPERATIONS.md)。

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

## 7. 容量Benchmark

使用隔离Compose Project运行核心HTTP与LMTP黑盒容量测试：

```powershell
./scripts/benchmark-compose.ps1
```

脚本只把测试端口绑定到`127.0.0.1`，输出机器可读结果到`artifacts/compose-benchmark/`，并在结束后删除本次临时Volume。场景边界与结果解读见[`docs/benchmarks/README.md`](../../docs/benchmarks/README.md)。该核心Benchmark绕过Nginx与Postfix，不能冒充公网端到端容量。

## 8. 生产浏览器E2E

安装`web/package-lock.json`固定的依赖与Chromium后，可独立运行Canonical Compose真实链路：

```powershell
cd ../..
npm --prefix web ci
npm --prefix web exec playwright install chromium
./scripts/e2e-compose.ps1
```

脚本使用正式App、Edge与Postfix镜像，通过临时自签证书和Loopback随机端口验证HTTPS Session、SMTP→LMTP→Parser、Sandbox HTML、附件和删除闭环。测试证书与Secret不会写入仓库，结束时删除本次容器、Network与Volume；失败证据位于`artifacts/production-e2e/`。该测试不替代公网DNS、MX、ACME或外部SMTP验收。

## 9. 灾备恢复演练

安装前端固定依赖与Chromium后，在非生产环境运行：

```powershell
./scripts/drill-compose-recovery.ps1
```

演练会在随机隔离Project中真实删除原PostgreSQL与Content Volume，从独立External Backup Volume恢复，并验证原Browser Session/附件与恢复后新邮件投递。安全机器结果位于`artifacts/disaster-recovery/result.json`；Dump、Raw MIME、Cookie、Capability和测试地址不会进入Artifact。脚本绝不能指向生产Project。
