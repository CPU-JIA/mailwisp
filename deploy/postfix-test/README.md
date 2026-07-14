# Postfix Integration Test Image

本目录只用于验证真实的 `Postfix -> Go LMTP` 故障与重投语义，不是生产部署配置。

固定基线：

- Alpine `3.24.0` Linux/amd64 image manifest：`sha256:33154315cf4402e697f065e6ec2156e292187e633908ccfede9c66279b6fa956`
- Postfix package：`3.11.5-r0`
- Alpine packaging commit：`2d8b64ef1eec4e46a8799e4c5970363a4a8eb40f`

构建与测试由 `scripts/verify.ps1` 和 GitHub Actions 统一执行。镜像不接收公网流量，只允许向测试宿主机上的随机 LMTP 端口投递。
