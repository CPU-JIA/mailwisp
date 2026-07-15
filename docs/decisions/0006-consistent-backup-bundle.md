# ADR 0006：一致性Backup Bundle与空目标恢复

状态：提议中，等待Reference恢复演练
日期：2026-07-15

## 背景

MailWisp的Message元数据位于PostgreSQL，Raw MIME位于本地Content Store。只备份其中一侧会产生Missing或Orphan；在收件过程中分别复制数据库与文件目录也不能证明二者来自同一个一致性时点。

Reference Profile面向个人服务器，备份必须由一名维护者独立完成、验证和恢复，不引入MinIO、分布式Snapshot或常驻Backup Service。

## 决策

V1使用离线一致性Backup Bundle。执行备份与恢复前必须取得ADR 0004定义的PostgreSQL独占维护锁，因此所有遵守MailWisp协议的`serve`进程均已停止收件。

### Bundle布局

Bundle是一个原子发布的目录：

```text
<backup-name>/
  manifest.json
  database.dump
  content.tar.gz
```

`manifest.json`固定包含：

- Format Name与Format Version。
- UTC创建时间。
- PostgreSQL Server、`pg_dump`与`pg_restore`版本。
- 当前Migration Version。
- `database.dump`的SHA-256与字节数。
- `content.tar.gz`的SHA-256与字节数。
- Content Object数量与未压缩字节数。

Manifest不得包含DSN、数据库密码、Token、邮件地址或邮件内容摘要。V1文件名固定，禁止Manifest提供任意相对路径。

### 备份流程

1. 目标路径必须不存在。
2. 在目标同级创建权限0700的随机`.partial`目录。
3. 取得独占维护锁。
4. 执行完整Content Reconciliation；存在Missing、Corrupt或Orphan时拒绝备份。
5. 使用官方`pg_dump --format=custom --no-owner --no-privileges`流式生成`database.dump`。
6. 以Canonical Object Path顺序流式生成`content.tar.gz`，不包含Staging。
7. 写入过程同步计算SHA-256、字节数和Object统计。
8. 每个文件完成后执行File Sync；Manifest最后写入并同步。
9. 同步Partial目录，原子Rename为最终Bundle，再同步父目录。

任何失败都不得发布最终Bundle；实现会尽力删除带`.partial`标识的未完成目录。

### 恢复流程

V1只允许恢复到空目标，不支持覆盖式或原地恢复：

1. 取得目标数据库的独占维护锁。
2. 严格解析Manifest，拒绝未知字段、未知版本、额外文件与路径穿越。
3. 在任何目标写入前验证两个Component的SHA-256与字节数。
4. 目标数据库不得包含MailWisp或其他Public Schema业务对象；Content Root必须不存在。
5. 将Content Archive解压到目标同级随机目录，逐Object验证Canonical Path、Size与SHA-256，拒绝Symlink、Hard Link、Device与重复Path。
6. 同步文件与目录后，先原子Rename为目标Content Root。
7. 使用官方`pg_restore --single-transaction --exit-on-error --no-owner --no-privileges`恢复数据库。
8. `pg_restore`使用Single Transaction降低半提交风险；但网络或进程终止可能发生在Server Commit后、客户端确认前，因此任何Restore错误都保留已安装Content，禁止自动删除并扩大为Missing。
9. Restore成功后运行Migration Readiness与完整Reconciliation；任何Missing、Corrupt或Orphan都使恢复失败。

先安装Content、后提交数据库，保证不会出现数据库已可见而Raw MIME尚未就位的窗口。恢复期间服务仍由独占维护锁阻止启动。

### PostgreSQL工具

- 不自行实现表级JSON/CSV备份协议，避免每次Schema演进复制`pg_dump`已经解决的依赖、类型、Sequence和DDL问题。
- Adapter只调用官方`pg_dump`与`pg_restore`，将已解析的Host、Port、Database、User、Password与TLS策略分别写入受控libpq环境。由于`pg_restore`强制要求`--dbname`，命令参数只传入固定的`service=mailwisp_restore`选择器；临时Service File内容固定且不含连接字段，原始DSN与密码不进入Command Line、Service File或日志。
- V1 Backup Tool明确只支持Reference Profile的单Host DSN；Multi-host与`target_session_attrs`在可证明无语义损失前拒绝执行。
- 客户端Major Version必须与PostgreSQL Server Major一致；Reference Profile固定验证18.4工具链。
- Go Bundle层不依赖Docker。Integration Test使用固定Digest的PostgreSQL 18.4容器与精确18.4客户端；Reference部署要求宿主机提供同Major官方客户端。

## 暂不采用

- 在线复制Content目录后再单独Dump数据库。
- 只依赖Volume Snapshot但不冻结PostgreSQL与应用写入。
- 将Raw MIME重新写入PostgreSQL以简化备份。
- 覆盖正在运行或包含现有数据的目标。
- 未校验Hash直接解压Tar。
- 在Archive中保留源机器UID、GID、绝对路径、Symlink或特殊文件。
- 把Docker Socket访问写入Canonical Backup Service。

## 接受条件

- 同一数据集连续Backup可以成功Verify并恢复到两个独立空目标。
- PostgreSQL Count、Foreign Key、UUID Version、Content Object数量和全部Digest一致。
- Manifest、Database Dump、Content Archive及Archive内部单Object篡改均被拒绝。
- Dump、Archive、Manifest、Rename与Restore各阶段失败不会产生可误认为成功的Bundle或半提交数据库。
- 普通与Race Integration使用固定PostgreSQL 18.4官方工具通过。
- 至少完成一次Reference Linux文件系统上的实际备份、删除、恢复与应用读取演练。

上述证据完成前，本ADR保持“提议中”，不得宣称MailWisp已经具备可依赖的灾难恢复能力。

## 2026-07-15阶段性实现证据

已经完成：

- V1严格Manifest、固定三文件布局、Component SHA-256与Size验证。
- Content Archive确定性顺序、逐Object Digest验证、路径穿越与重复Path拒绝。
- Partial目录生成、Manifest最后写入、文件与目录同步、最终原子Rename发布。
- Bundle篡改、未知字段、额外文件、失败不发布与空目标恢复单元测试。
- Restore前完整验包，Content先安装，数据库使用Single Transaction恢复；失败时保守保留Content，成功后完整Reconciliation。
- `mailwisp backup <directory>`与`mailwisp restore <bundle-directory>`正式命令及独占维护锁。
- `pg_dump`与`pg_restore`固定命令入口；连接字段只通过清理后的libpq子进程环境传递，错误输出有界并执行Secret Redaction。
- PostgreSQL Server、`pg_dump`与`pg_restore`Major一致性验证；Gosec零Issue且未使用`#nosec`。
- 固定PostgreSQL 18.4真实Bundle Round-trip Integration与Race Test已在GitHub Actions Linux完整门禁通过。

尚未完成：

- 至少一次Reference Linux文件系统上的备份、删除、恢复、应用读取与人工Runbook演练。
- 断电或宿主机硬重启条件下的Bundle发布与恢复验证。
