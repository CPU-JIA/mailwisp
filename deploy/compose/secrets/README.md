# 本地Secret文件

创建`postgres_password.txt`并写入高熵PostgreSQL密码，文件不得提交Git：

```bash
umask 077
openssl rand -base64 32 > deploy/compose/secrets/postgres_password.txt
```
