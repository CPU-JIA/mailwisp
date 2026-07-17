# MailWisp Release Notes规范

每个正式Tag `vMAJOR.MINOR.PATCH`必须在同一Commit包含`docs/releases/<tag>.md`。Release Workflow在文件缺失、Tag不属于`main`、版本格式不严格、完整门禁未通过、构建不可复现、SBOM或扫描失败、Provenance不可用时一律Fail Closed。

Release Notes默认中文，并必须明确列出：

- Breaking Change；
- Migration与数据影响；
- 配置变化；
- 已验证范围；
- Rollback步骤和不支持的回退边界。

GitHub Actions只创建Draft Release。发布者必须再次核对Artifact Attestation、外部/内部Checksum、SPDX SBOM、安全扫描和解压后Production E2E证据，才可把Draft转为Published。
