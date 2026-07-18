# ADR 0023：预构建Compose Bundle与Fail-closed Release供应链

状态：已实现并通过GitHub Linux Candidate门禁
日期：2026-07-17

## 背景

旧`build-release.ps1`只复制Go二进制、Web产物与Compose文件。Bundle中的Compose Build Context指向解压根目录，而Dockerfile需要`go.mod`、`cmd/`、`internal/`、`migrations/`与完整`web/`源码，因此它无法从解压目录独立构建。这个包只能“看起来像Release”，不满足可交付性。

源码归档、预构建Registry镜像和离线镜像Bundle各有不同信任与运维成本。MailWisp的Canonical Profile面向个人单机Compose：部署者应能验证一个与Git Commit绑定的产物，加载后不再调用编译工具链，也不应因本地Tag缺失而静默回退源码Build。

仓库在本ADR形成时为Private；2026-07-18经用户明确授权已改为Public。GitHub Artifact Attestations对Public仓库可直接使用，对Private/Internal仓库则要求GitHub Enterprise Cloud；SLSA Generic Generator虽然可用，但会把私有仓库名写入公共Rekor。若仓库未来恢复为Private，仍不能用公开透明日志绕过这一隐私边界。

## 决策

- Source Checkout继续以`compose.yaml`提供本地/CI Build，这是开发与自构建路径。
- Release Bundle携带`linux/amd64`的App、Maintenance、Edge与Postfix四份独立确定性镜像Archive，以及Host-native辅助二进制与静态Web资源；禁止再经Docker Engine的非确定性多镜像`docker save`重打包。
- `release.compose.yaml`与`backup-verifier.release.compose.yaml`使用Compose 5.2.0支持的`!reset null`移除全部`build`，并为所有MailWisp镜像设置`pull_policy: never`；本地镜像缺失时必须Fail Closed，不能从Registry拉取同名Tag。
- Bundle同时携带`db-provision`脚本；该一次性Non-root服务复用固定Digest的PostgreSQL镜像，先收敛Runtime Role与已有对象权限，再允许Migration和App启动。它不新增MailWisp应用镜像，也不依赖只在空Volume运行的初始化目录。
- Go二进制和四个镜像写入Version、完整Git Commit与UTC Source Date；镜像同时写入OCI Version、Revision与Created Label。
- Canonical Release只在固定Ubuntu 24.04 Runner从干净Checkout构建。Docker Buildx 0.35.0由官方Binary SHA-256校验安装；每次构建创建独立的BuildKit 0.31.2 Digest Builder并禁用Cache。每个镜像先以Docker Exporter的`rewrite-timestamp=true`和Git Commit Epoch重写Layer Timestamp，再从确定性Image Archive加载；外层使用规范化Tar Metadata与`gzip -n`，同一Job执行两次从零构建并以`cmp`证明Archive逐字节一致。
- Maintenance与Postfix分别把PostgreSQL Client所需八个APK、Postfix所需九个APK固定到精确版本URL与逐文件SHA-256，再由`apk --no-network --repositories-file /dev/null`只安装已下载集合；安装后删除包含墙钟时间、但不属于Package Database的构建期`/var/log/apk.log`，保留真实Package Database，避免Build-time Log破坏可复现性。两者都以固定Alpine Digest为Base，Release构建不依赖浮动APKINDEX，也不继承PostgreSQL Server或`gosu`等无关攻击面。
- Syft 1.48.0为Host Binary、Source和四个Final Image生成SPDX 2.3 JSON；Source扫描前必须证明干净Worktree和`HEAD`与Build Commit一致，再只扫描`git archive <commit>`生成的隔离快照，禁止用Live Working Tree冒充Commit Subject。Trivy 0.72.0先显式下载数据库，再按不可变Image ID扫描Final Image与IaC并记录DB/Check Bundle元数据，Vulnerability DB不得超过48小时。HIGH/CRITICAL允许数为0，不使用`--ignore-unfixed`。Postfix Master因绑定25端口、初始化Queue/Chroot并随后降权而必须以Root启动；该项只允许通过`.trivyignore.yaml`对生产Postfix Dockerfile做路径级、带到期日和理由的显式例外，其他Root Image告警仍阻断。
- Bundle内部Checksum必须覆盖除清单自身外的全部文件。正式发布资产统一复制到唯一的扁平`publish/`目录，拒绝空名、保留名和大小写不敏感的重名；外层Checksum只使用Release页下载后仍成立的Basename，完整覆盖Archive、SBOM、安全证据和Release Evidence。正式Tag先重新计算全部下载文件，再分别为清单列出的Subject及`SHA256SUMS`自身生成GitHub Build Provenance。
- Tag只接受严格`vMAJOR.MINOR.PATCH`，必须指向`main`祖先且包含对应中文Release Notes。Workflow只创建Draft Release。
- Public仓库可以直接使用GitHub Artifact Attestations；Private仓库只有显式声明企业Attestation后端已启用才允许继续。否则Tag Job失败，不发布无Provenance产物，也不向公共Rekor泄漏仓库身份。

## 验证

- 全量`verify.ps1`必须在同一干净Checkout先通过，覆盖Test/Race/Vet/Fuzz、PostgreSQL/Postfix、Govulncheck/Gosec/Gitleaks、前端、Production E2E与灾备恢复。
- 两次Release Archive SHA-256必须完全一致。
- 解压前验证外层Checksum、单一顶层目录、重复Entry和Tar类型，拒绝绝对路径、`..`、Symlink、Hardlink与未知类型；解压后要求内部Checksum覆盖全部Regular File。
- Candidate Artifact、Attestation与Draft Release必须消费同一个扁平`publish/`目录；下载后的文件数必须严格等于Checksum Subject数加清单自身，避免目录折叠或旧Asset造成证据错配。
- 只有Build Output通过固定四镜像白名单、Image ID、Tag、平台、版本和工具链Schema验证后，才允许删除本地Release Tag镜像；从Archive重新加载后核对Image ID与OCI Label。
- 合并后的Release Compose必须渲染完整八服务集合、所有MailWisp服务零`build`且`pull_policy: never`，并使用加载后的镜像重新完成Runtime Role Provision、HTTPS/SMTP/Postfix/LMTP/Parser/Browser Production E2E。
- SBOM必须为SPDX 2.3且每份至少描述一个Package；安全证据必须报告零HIGH/CRITICAL阻断项。

2026-07-17的PR #39 Candidate Run `29574327466`在Ubuntu 24.04上完成两次隔离无缓存构建并通过逐字节比较。下载后的Artifact包含18个扁平文件与17个外层Checksum Subject；Bundle包含42个内层Checksum Subject、4份确定性镜像Archive、6份SPDX文档和5份Trivy报告，阻断项为0，预构建Compose Production E2E通过。Candidate Archive SHA-256为`637f14b06f08c870aa84044f8fa0ec9b59cef6d4a21036c1b869178a523421fa`。

## 后果

- Release Artifact明显大于只含二进制的Archive，但个人服务器不需要在生产机安装Go、Node或下载应用Build Dependency。
- PostgreSQL和Certbot等固定Digest第三方镜像仍由Compose拉取，可信Mirror只能改变传输路径，不能改变身份。
- 当前Public仓库可完成正式Attested Release。若未来改为Private Free/Pro，正式发布会被策略阻断；这不是可跳过的CI故障，PR与`main`仍只生成30天保留的完整Release Candidate Artifact。
- 当前只交付`linux/amd64`。新增Architecture必须独立构建、SBOM、扫描、E2E与容量验证，不能只改Platform字符串。
