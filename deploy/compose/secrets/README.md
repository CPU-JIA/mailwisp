# 本地Secret文件

创建PostgreSQL密码、Browser Session Key与Create Quota HMAC Key，文件不得提交Git：

```bash
umask 077
openssl rand -base64 32 > deploy/compose/secrets/postgres_password.txt
openssl rand -base64 32 > deploy/compose/secrets/browser_session_key.txt
openssl rand -base64 32 > deploy/compose/secrets/create_quota_hmac_key.txt
```
