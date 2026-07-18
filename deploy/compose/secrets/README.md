# 本地Secret文件

创建相互独立的PostgreSQL Owner密码、Runtime App密码、Browser Session Key与Create Quota HMAC Key，文件不得提交Git：

```bash
umask 077
openssl rand -base64 32 > deploy/compose/secrets/postgres_owner_password.txt
openssl rand -base64 32 > deploy/compose/secrets/postgres_app_password.txt
openssl rand -base64 32 > deploy/compose/secrets/browser_session_key.txt
openssl rand -base64 32 > deploy/compose/secrets/create_quota_hmac_key.txt
chmod 0444 deploy/compose/secrets/postgres_owner_password.txt \
  deploy/compose/secrets/postgres_app_password.txt \
  deploy/compose/secrets/browser_session_key.txt \
  deploy/compose/secrets/create_quota_hmac_key.txt
```

父目录必须保持`0700`且不得共享。文件使用`0444`是为了让Linux Docker Compose只读Bind Mount可由Non-root容器进程读取；每个服务仍只能挂载Compose显式授予的Secret。
