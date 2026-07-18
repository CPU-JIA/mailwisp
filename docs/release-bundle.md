# MailWisp Linux amd64 Release Bundle

该Bundle同时提供Canonical Docker Compose部署所需的预构建应用镜像，以及Host-native辅助部署所需的Go二进制与静态Web资源。它只面向`linux/amd64`，不包含任何Secret、生产配置、TLS证书、数据库Dump或Raw MIME。

## 完整性验证

Release页与GitHub Actions Candidate Artifact提供相同的扁平资产集合，`SHA256SUMS`只引用同目录Basename，因此两种下载方式的验证命令完全一致。先验证外层清单，解压后再验证Bundle内部清单：

```bash
sha256sum --check SHA256SUMS
tar -xzf mailwisp-<version>-linux-amd64.tar.gz
cd mailwisp-<version>-linux-amd64
sha256sum --check SHA256SUMS
```

正式Tag Release还必须同时验证Release Subject和`SHA256SUMS`自身的GitHub Artifact Attestation。MailWisp当前Public仓库直接使用GitHub Artifact Attestations；若未来改为Private，只有在GitHub Enterprise Cloud启用Artifact Attestations后才允许发布。流水线在不具备该能力时Fail Closed，不会静默发布无Attestation的Release。

## Canonical Docker Compose部署

Bundle已经携带App、Maintenance、Edge与Postfix镜像。Release Overlay会删除全部源码`build`定义，并将这些镜像的`pull_policy`设为`never`：缺失本地镜像时直接失败，禁止从Registry拉取同名Tag，也防止运行时产物与已审查Commit不一致。

```bash
for image in \
  images/mailwisp-app-linux-amd64.tar \
  images/mailwisp-edge-linux-amd64.tar \
  images/mailwisp-maintenance-linux-amd64.tar \
  images/mailwisp-postfix-linux-amd64.tar; do
  docker load --input "$image"
done
cd deploy/compose
cp .env.example .env
cp mailwisp.env.example mailwisp.env
install -d -m 0700 secrets backups
openssl rand -base64 32 > secrets/postgres_owner_password.txt
openssl rand -base64 32 > secrets/postgres_app_password.txt
openssl rand -base64 32 > secrets/browser_session_key.txt
openssl rand -base64 32 > secrets/create_quota_hmac_key.txt
chmod 0444 secrets/*.txt
sudo chown -R 65532:65532 backups
```

`secrets/`父目录必须保持`0700`。文件使用`0444`是为了让Compose只读Bind Mount可被Non-root容器读取；每个Service仍只挂载其明确需要的Secret。一次性Non-root `db-provision`使用Owner与Runtime密码幂等创建或收敛最小权限Role并补齐已有对象权限，`migrate`和Maintenance只使用Owner，常驻App只使用Runtime Role。

编辑`.env`和`mailwisp.env`中的示例域名后执行：

```bash
sh preflight.sh
docker compose config --quiet
docker compose up -d --no-build
docker compose ps
```

启动顺序固定为`postgres → db-provision → migrate → app`。因此同一Bundle既支持空数据卷，也支持MailWisp自身已有数据卷的升级；Role创建和已有表授权不依赖PostgreSQL只执行一次的初始化目录。

Bundle中的`.env.example`已经固定当前Release Tag，并通过`COMPOSE_FILE=compose.yaml:release.compose.yaml`自动启用预构建Overlay。不要删除该行，也不要在Release目录执行`docker compose build`。

Preflight要求Linux amd64 Docker Engine与`versions.lock`中的精确Docker Compose版本；版本不一致必须先安装锁定版本，不能以配置渲染成功代替工具链身份验证。

独立验证备份时使用对应Verifier Overlay：

```bash
docker compose \
  -f backup-verifier.compose.yaml \
  -f backup-verifier.release.compose.yaml \
  run --rm --no-deps backup-verifier backup verify /backups/<bundle-directory>
```

PostgreSQL、Certbot等第三方镜像不重复封装进应用镜像归档；Compose继续按已锁定的Tag与Digest获取它们。可信Registry Mirror只能改变传输路径，不能改变Digest身份。

## Host-native辅助部署

`mailwisp`是静态Go二进制，`web/`是生产静态资源，`deploy/reference/`包含systemd、Nginx、Postfix和Certbot辅助配置。安装前先执行：

```bash
./mailwisp version --json
```

输出的`version`、`commit`和`build_date`必须与`release.json`一致。Host-native不是默认路径，详细步骤见`deploy/reference/README.md`。

## SBOM、漏洞证据与回滚

外层扁平发布资产同时提供：

- Host Binary、Source及四个运行镜像的SPDX 2.3 JSON SBOM；
- 固定Trivy版本生成的Image与IaC扫描证据；
- 覆盖Release Archive、SBOM和安全证据的`SHA256SUMS`；
- 与Git Commit绑定的`release-evidence.json`。

回滚不得把已前向迁移的数据库直接交给旧版本。先按`deploy/compose/OPERATIONS.md`完成兼容性判断和备份验证，再在新Compose Project与新Volume中恢复受支持Backup。
