# ADR 0022：Canonical Compose离线灾备与隔离恢复演练

状态：已接受
日期：2026-07-16

## 背景

MailWisp的PostgreSQL元数据与Content Store必须来自同一个离线时点。既有Bundle实现已经覆盖严格Manifest、Hash和空目标Restore，但Canonical Compose仍缺少可执行证据：App镜像不带PostgreSQL工具，Content Volume直接挂载到`MAILWISP_CONTENT_ROOT`又会让空卷目标目录预先存在。

## 决策

- 常驻App镜像继续保持最小，不安装`pg_dump`或`pg_restore`。
- 新增按需构建的Non-root `maintenance` Target与Compose Profile。PostgreSQL 18.4 Client及新增依赖使用精确APK URL与逐文件SHA-256锁定，在固定Alpine Digest上以无Repository/Network解析的方式安装；它不携带PostgreSQL Server或`gosu`，只持有Owner PostgreSQL Secret，并连接内部`database`网络、挂载Content与可写Backup Storage。
- App不再挂载Backup Storage；只有Maintenance能写Bundle，独立Verifier只能只读访问。
- Content Volume继续直接挂载到`/var/lib/mailwisp/content`，保持既有卷根`objects/sha256`布局。Restore同时接受“不存在的Root”和“现有空挂载目录”，但拒绝非空目录、符号链接与非目录目标。
- 独立`backup-verifier.compose.yaml`不包含其他生产服务，不加载配置、不持有Secret、不连接网络或Live Content，只以只读方式挂载Backup Storage；`mailwisp backup verify <bundle>`在销毁源数据前严格重读三文件布局、Manifest、Size和SHA-256。
- 自动化演练使用随机隔离Compose Project与带专属Label的External Backup Volume。中途删除该演练Project的全部内部Volume，并重点证明原PostgreSQL/Content Volume消失；External Backup必须继续存在，最终再按精确名称和Label删除。
- 两阶段Playwright在受限临时目录保存测试Cookie。恢复后必须使用原Browser Session读取原邮件和附件，并再经SMTP投递新邮件证明恢复后可写。Cookie、Capability、测试地址与Raw MIME不得进入Artifact。

## 验证链路

`scripts/drill-compose-recovery.ps1`执行：

1. 启动正式PostgreSQL、幂等Runtime Role Provision、Migration、App、Nginx Edge与Postfix。
2. 通过HTTPS创建Inbox/Session，经SMTP→Postfix→LMTP投递带附件邮件。
3. 等Parser完成并确认Postfix Queue为空，停止Edge、Postfix和App。
4. Maintenance创建Bundle，再执行独立`backup verify`。
5. 删除隔离Project的原PostgreSQL与Content Volume，并明确观察二者不存在。
6. 只启动空PostgreSQL，确认Public Schema无业务对象；不得先运行Migration。
7. Maintenance恢复到全新Content Volume与空数据库。
8. 对比Migration、表Count、Content Catalog、FK Orphan与UUIDv7快照。
9. 先由`db-provision`对恢复出的已有表补齐Runtime Role权限，再启动完整栈，以原Browser Session验证原附件，并投递、读取新邮件。
10. 清理全部Container、Network、内部Volume、External Backup Volume和临时Secret；任一清理失败都使门禁失败。

机器可读证据只保留安全的Manifest统计、工具版本与Boolean检查，不上传Dump、Content Archive、Cookie、Capability、地址或测试状态文件。

## 影响与边界

- Maintenance镜像增加少量按需磁盘占用，但不增加常驻进程、App攻击面或空闲内存；Verifier复用同一镜像但使用独立最小权限Service。
- Bundle不包含域名配置、PostgreSQL/Browser/Quota Secret、TLS证书或Postfix Queue；这些必须独立加密、离机备份。
- 生产Runbook禁止自动删除旧Volume。恢复必须先在新Project和空目标隔离验证，再安排端口切换；失败目标不得原地重试覆盖。
- GitHub Linux演练证明Compose逻辑删除/恢复；宿主机掉电、云盘故障、DNS、ACME和公网25端口仍属于目标基础设施验收。

## 接受证据

2026-07-17的[`Verify MailWisp #29545090225`](https://github.com/CPU-JIA/mailwisp/actions/runs/29545090225)在`ubuntu-24.04`通过Linux Full Verification：

- Artifact `disaster-recovery-29545090225-1`为`passed`，Docker Compose 5.2.0，`pg_dump`/`pg_restore` 18.4。
- 原PostgreSQL/Content Volume删除、External Backup存活、空数据库观察、数据库快照与Content Digest匹配、原Session/Plaintext/HTML/附件恢复、恢复后投递全部为真。
- 最终FK Orphan与非UUIDv7计数为0，Container、Network、内部Volume与External Backup残留为0。
- Artifact `production-e2e-29545090225-1`同时证明Canonical Production Compose浏览器与SMTP路径为`passed`。

同一PR的Compose Benchmark Run [`#29545090189`](https://github.com/CPU-JIA/mailwisp/actions/runs/29545090189)在Ubuntu 24.04.4完成12个场景，全部`failed=0`，Parser为400/400且Metrics与Docker Stats非空。该证据接受本ADR的Canonical Compose自动灾备设计，不扩大为目标服务器硬件、网络或公网邮件验收。
