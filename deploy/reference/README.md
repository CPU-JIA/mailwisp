# MailWisp Host-native辅助部署

本Profile面向希望使用systemd深度集成的一台长期运行Linux个人服务器。Docker Compose是主推荐部署方式；Host-native保留为辅助选择。公网只开放`25/tcp`、`80/tcp`和`443/tcp`；Go HTTP、LMTP与PostgreSQL只监听本机。版本目标见`versions.lock`，升级必须通过PR更新Lock和验证证据。

## 1. DNS与主机名

假设：

- Web：`mail.example.com`
- SMTP：`mx.example.com`
- 收件域名：`example.com`

至少配置：

```text
mail.example.com. A/AAAA <server>
mx.example.com.   A/AAAA <server>
example.com.      MX 10 mx.example.com.
```

服务器PTR应指向`mx.example.com`。上线前从外部网络验证25端口未被云厂商阻断。

## 2. 系统账户与目录

```bash
sudo useradd --system --home /var/lib/mailwisp --shell /usr/sbin/nologin mailwisp
sudo install -d -m 0700 -o mailwisp -g mailwisp /var/lib/mailwisp/content
sudo install -d -m 0755 -o root -g root /etc/mailwisp /var/www/mailwisp /var/lib/letsencrypt
```

将Release Bundle中的`mailwisp`安装到`/usr/local/bin/mailwisp`，静态文件复制到`/var/www/mailwisp`。二进制必须保持`root:root 0755`，配置文件`/etc/mailwisp/mailwisp.env`保持`root:mailwisp 0640`。

同时安装证书Deploy Hook：

```bash
sudo install -D -m 0750 -o root -g root \
  deploy/reference/certbot/reload-mail-services.sh \
  /usr/local/libexec/mailwisp/reload-mail-services.sh
```

## 3. PostgreSQL

安装Release Lock对应的PostgreSQL 18.4，创建独立Role和Database。密码只写入`/etc/mailwisp/mailwisp.env`，不得进入Shell历史、Git或日志。

Reference环境变量至少包含：

```text
MAILWISP_HTTP_ADDR=127.0.0.1:8080
MAILWISP_LMTP_ADDR=127.0.0.1:2525
MAILWISP_LMTP_HOSTNAME=mx.example.com
MAILWISP_PUBLIC_DOMAINS=example.com
MAILWISP_TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128
MAILWISP_POSTGRES_DSN=postgres://mailwisp:<secret>@127.0.0.1:5432/mailwisp?sslmode=require
MAILWISP_CONTENT_ROOT=/var/lib/mailwisp/content
MAILWISP_CONTENT_MAX_BYTES=26214400
MAILWISP_BROWSER_SESSION_KEY=<base64-encoded-32-byte-secret>
MAILWISP_BROWSER_SESSION_LIFETIME=12h
MAILWISP_CLEANUP_BATCH_SIZE=100
MAILWISP_CLEANUP_INTERVAL=0s
MAILWISP_CLEANUP_TIMEOUT=2m
MAILWISP_DUCKMAIL_ENABLED=false
```

Browser Session Key必须独立随机生成，例如`openssl rand -base64 32`。Browser Session始终使用Secure `__Host-` Cookie，因此本地纯HTTP开发继续使用内存Capability模式。轮换Key会立即退出所有现有浏览器Session，但不会撤销Canonical Capability。

本机PostgreSQL也必须启用TLS或改用权限受控的Unix Socket；不得使用公网监听代替。

## 4. systemd

复制`systemd/`中的Units到`/etc/systemd/system/`：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now mailwisp.service mailwisp-cleanup.timer
sudo systemctl status mailwisp.service mailwisp-cleanup.timer
```

`mailwisp-migrate.service`在每次启动前执行幂等Migration。Cleanup每五分钟运行，使用短事务、`SKIP LOCKED`和Advisory Lock。

## 5. Nginx与证书

将`nginx/mailwisp.conf.example`复制到Nginx配置目录并替换三个占位符。先只启用80端口ACME Location，随后签发同时覆盖Web和SMTP Host的证书：

```bash
sudo certbot certonly --webroot -w /var/lib/letsencrypt \
  -d mail.example.com -d mx.example.com \
  --deploy-hook /usr/local/libexec/mailwisp/reload-mail-services.sh
```

首次签发后手动以`RENEWED_LINEAGE=/etc/letsencrypt/live/mail.example.com`运行Deploy Hook，安装Postfix副本。然后启用443配置并执行：

```bash
sudo nginx -t
sudo systemctl reload nginx
sudo certbot renew --dry-run
```

## 6. Postfix

合并`postfix/main.cf.example`，替换占位符；使用`master.cf.example`明确关闭公网`smtpd`的Chroot，以便读取受控证书副本。安装Transport Map：

```bash
sudo install -m 0644 postfix/mailwisp_transport.example /etc/postfix/mailwisp_transport
sudo postmap /etc/postfix/mailwisp_transport
sudo postfix check
sudo systemctl reload postfix
```

未知Recipient由Go LMTP返回永久失败；过载和暂时数据库故障返回4xx，由Postfix持久Queue重投。

## 7. 上线验收

- `curl --fail https://mail.example.com/readyz`
- 浏览器检查CSP、语言、主题、创建Inbox、Token只显示一次
- 外部SMTP发送到新Inbox，API读取正文后删除
- `openssl s_client -starttls smtp -connect mx.example.com:25 -servername mx.example.com`
- `postqueue -p`无长期Deferred异常
- `systemctl start mailwisp-cleanup.service`后过期测试Inbox与Raw MIME被删除
- `mailwisp backup`、空目标`mailwisp restore`和内容一致性检查完成
- `certbot renew --dry-run`后Nginx与Postfix仍加载同一新证书

任何一项未验证都不能标记生产发布完成。
