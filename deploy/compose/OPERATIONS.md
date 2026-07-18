# MailWisp Compose运维Runbook

本Runbook适用于Canonical单机Docker Compose Profile。所有命令在`deploy/compose/`执行；生产变更前先保存`docker compose config`、当前Git Commit、镜像ID和Volume列表。任何破坏性动作必须限定到明确Project，禁止使用`docker system prune`或`docker volume prune`。

## 离线一致性备份

1. 确认独立加密备份已覆盖`.env`、`mailwisp.env`、四个Secret文件、TLS证书和DNS配置。四个Secret分别是PostgreSQL Owner密码、Runtime App密码、Browser Session Key和Create Quota HMAC Key；MailWisp Bundle不包含这些内容。
2. 在维护窗口前从同一个已审查Commit构建固定镜像并记录镜像ID；任何构建、拉取或配置检查失败都不得进入停机窗口：

```bash
docker compose --profile maintenance config --quiet
docker compose -f backup-verifier.compose.yaml config --quiet
sh preflight.sh
docker compose build --pull app maintenance edge postfix
docker compose images --format json > reviewed-compose-images.json
```

3. 在Host防火墙和云安全组临时阻断新的`25/tcp`连接，等待Postfix Queue按既定策略排空；Queue内尚未交给LMTP的邮件不在Bundle内。停止Postfix后必须再用同一Queue Volume执行一次检查，消除“检查后又接收邮件”的竞态，然后停止App并创建Bundle：

```bash
docker compose exec postfix postqueue -p
docker compose stop edge postfix
queue_output=$(docker compose run --rm --no-deps --entrypoint postqueue postfix -p)
printf '%s\n' "$queue_output"
printf '%s\n' "$queue_output" | grep -Fq 'Mail queue is empty' || exit 1
docker compose stop app
bundle="/backups/mailwisp-$(date -u +%Y%m%dT%H%M%SZ)"
docker compose run --rm --no-deps maintenance backup "$bundle"
docker compose -f backup-verifier.compose.yaml run --rm --no-deps backup-verifier \
  backup verify "$bundle"
```

`backup verify`是Bundle完整性校验：它严格重读布局、Manifest、Size与SHA-256；实际可恢复性仍由隔离Restore演练证明。

停止后的第二次Queue检查必须匹配`Mail queue is empty`并以非零退出码阻断后续步骤；仅把`postqueue -p`输出打印到日志不构成Fail-closed备份门禁。

4. 把完整三文件Bundle复制到独立主机或Object Storage，保留加密、Object Lock/Versioning与生命周期策略；同机`./backups`不构成灾备。
5. 备份窗口结束后恢复服务，显式撤销Host防火墙和云安全组的`25/tcp`临时阻断，再检查Readiness、Queue和外部SMTP：

```bash
docker compose up -d migrate app edge postfix
docker compose ps
docker compose exec postfix postqueue -p
# 在此执行经变更审批的Host/云防火墙 reopen 25/tcp 命令
# 再从外部探针验证SMTP banner、STARTTLS与真实收件
```

## 隔离恢复与切换

生产恢复不得删除或覆盖旧Volume。使用新Project创建全新命名卷，先只启动PostgreSQL：

```bash
restore_project="mailwisp-restore-$(date -u +%Y%m%dT%H%M%SZ | tr '[:upper:]' '[:lower:]')"
docker compose -p "$restore_project" -f backup-verifier.compose.yaml run --rm --no-deps backup-verifier \
  backup verify /backups/<bundle>
docker compose -p "$restore_project" up -d --wait --wait-timeout 120 --no-deps postgres
docker compose -p "$restore_project" run --rm --no-deps maintenance \
  restore /backups/<bundle>
docker compose -p "$restore_project" up -d migrate app
docker compose -p "$restore_project" exec -T app \
  wget -qO- http://127.0.0.1:8080/readyz
```

恢复前不得运行Migration；否则目标数据库不再为空。恢复失败后Content可能按保守策略保留，必须换新的空Project/Volume重试，不得覆盖失败目标。使用预先保存的Canary Capability验证Inbox、正文和附件；再运行`mailwisp reconcile`并检查数据库/Content统计。启动新Project的Edge/Postfix前，必须从独立加密备份恢复原TLS Certificate Lineage，或重新签发同时覆盖Web与SMTP Host的证书，并先执行Nginx/Postfix配置检查。验证完成后安排短维护窗口：停止旧入口，确认Queue策略，启动新入口，切换端口后显式恢复`25/tcp`放行，并执行外部HTTPS、SMTP STARTTLS、MX和收件验收。旧Project和Volume至少保留一个回滚窗口，人工批准后才删除。

## 版本升级与回滚

升级PR必须同时更新版本Lock、Digest、Release Note和验证证据。执行顺序：

1. 完成离线Bundle并执行`backup verify`，把Bundle复制到独立存储。
2. 记录当前镜像ID：`docker compose images --format json`。
3. 使用审查后的Commit构建新镜像，执行`docker compose config --quiet`与完整演练。
4. 停入口和App，运行一次性`db-provision`与Migration，再启动App、Edge和Postfix；`db-provision`会收敛Runtime Role密码、属性及已有表权限，但不会修改业务数据。
5. 验证Readiness、Canary收件、附件、Queue、Parser、Retention和Metrics。

应用镜像回滚只有在新Migration被明确证明向后兼容时才允许直接切回旧Tag。若Schema不兼容或状态不明，停止新栈，从升级前Bundle恢复到新的空Project，再按“隔离恢复与切换”执行；禁止对已前向迁移的数据库盲目运行旧二进制。

## 告警起点

[`prometheus-alerts.example.yml`](prometheus-alerts.example.yml)提供低基数应用告警起点，并由固定Digest的Prometheus 3.13.1 `promtool check rules`验证YAML与PromQL；它不会自动部署Prometheus/Grafana。维护者还必须在Host/云监控配置：

规则会在持久物理删除队列`mailwisp_content_deletion_pending`连续15分钟不归零时告警。该状态表示Inbox/Message已在数据库中完成逻辑删除，但Raw MIME仍等待安全重试；不得通过手工清空队列表来消除告警，应先检查Content Volume权限、磁盘与App日志并让Retention重试完成。

- HTTPS `/readyz`连续2分钟失败：Critical。
- Docker数据目录或Content Volume剩余空间低于`max(2 × MAILWISP_LMTP_MAX_MESSAGE_BYTES, MAILWISP_CONTENT_MIN_FREE_BYTES)`：Warning；低于配置水位：Critical。
- Postfix Queue最老邮件超过5分钟或Queue持续增长15分钟：Warning；超过30分钟：Critical。
- TLS证书剩余14天：Warning；7天：Critical。
- PostgreSQL不可用、备份未在预定窗口成功、最近Bundle未通过`backup verify`：Critical。
- 最近一次灾备演练超过30天：Warning；超过45天：Critical。

阈值是个人服务器的保守起点，应依据容量Benchmark和正常基线调整，但不得关闭Readiness、磁盘、Queue、证书和备份新鲜度五类告警。

Compose已把每个容器的本地`json-file`日志限制为`10m × 3`。Postfix Envelope与远端IP、HTTP Request ID和错误上下文仍应按敏感运维元数据管理；定期核对Docker日志实际占用与Host访问权限，集中采集时采用短期Retention和字段脱敏，禁止采集Raw MIME、Cookie、Authorization或Secret。

## 自动演练

在非生产环境运行：

```powershell
./scripts/drill-compose-recovery.ps1
```

脚本只使用随机Loopback端口和隔离Project，会删除该演练Project的数据卷并保留机器可读`artifacts/disaster-recovery/result.json`。不得把脚本改为指向生产Project；它的破坏步骤只因随机Project、专属Label和External Backup Volume三重边界才安全。
