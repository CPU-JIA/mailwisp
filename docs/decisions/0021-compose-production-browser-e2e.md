# ADR 0021：Canonical Compose必须通过真实SMTP到浏览器E2E

状态：已接受
日期：2026-07-16

## 背景

现有Go Integration分别验证Postfix、LMTP、PostgreSQL、Parser与HTTP，Vue Playwright验证浏览器状态机，但浏览器测试通过Route Mock返回API响应。各层独立通过不能证明生产Nginx静态资源与反向代理、Secure Cookie、Postfix投递、Content Store、持久Parser Queue和Vue能够在同一次用户旅程中正确组合。

## 决策

- `deploy/compose/compose.yaml`仍是唯一Canonical生产定义；`production-e2e.compose.yaml`只替换Loopback发布端口、测试证书挂载与有界测试参数。
- Dockerfile Syntax Frontend同时锁定`1.20.0`与Manifest Digest，Build Grammar也不得通过浮动Tag漂移。
- Postfix同时加入独立非内部`SMTP ingress`网络与内部backend：前者使Docker真实发布`25/tcp`，后者仅用于LMTP交付；App与PostgreSQL不加入SMTP入口网络。
- 单一收件域直接使用`relay_transport = lmtp:inet:app:2525`，不依赖发行版可能未编译的Berkeley DB `hash:`映射；本地投递关闭，因此不加载Alias数据库。
- 测试启动PostgreSQL、Migration、App、Edge和Postfix正式镜像，不使用Mock Repository、Vite Dev Server或后门投递接口。
- 浏览器必须通过HTTPS Edge创建Inbox，把一次性Capability交换为`Secure`、`HttpOnly`、`SameSite=Lax`的`__Host-mailwisp_session`，并在Reload后恢复。
- 邮件必须从Host Loopback SMTP进入Postfix，再经LMTP、Content Store、PostgreSQL和Parser后由Vue读取；测试覆盖Plain Text、Sandbox HTML、附件下载、消息删除和Inbox删除。
- HTML用例包含Script与远程Tracking Image，浏览器必须证明脚本被移除且没有外部请求。
- Nginx安全Header使用单一Snippet，并在设置独立Cache Header的静态Location中显式包含，避免`add_header`继承规则使HTML或Asset响应丢失CSP等边界。
- 编排脚本只将HTTP、HTTPS与SMTP绑定到`127.0.0.1`，动态选择空闲端口，使用.NET在临时目录生成7天自签SAN证书和随机Secret。
- 临时Fixture不得逃逸系统Temp Root；Windows移除继承ACL并只授权当前用户，Linux使用`0700`，避免测试Secret在运行期间被同机其他用户读取。
- 失败保留Compose状态、容器日志、Nginx/Postfix有效配置以及Playwright Trace、Video与Screenshot；无论成功失败都删除本次容器、Network、Volume与临时Secret。
- Session Restore与用户操作共享Abort/Owner边界，迟到的恢复响应不得覆盖用户刚创建或打开的Inbox。

## 非目标

- 自签名Loopback测试不证明公网DNS、MX、ACME、真实CA信任、云厂商25端口或跨运营商投递可用。
- 本门禁不替代目标Linux主机上的证书续签、外部SMTP、备份恢复和断电演练。

## 验证

- Mock Browser E2E故意延迟Session Restore，并验证用户创建结果保持不变。
- Production Browser E2E验证安全Header、Capability不进入URL/Local Storage、Cookie属性、Reload、SMTP收件、Parser、Sandbox、附件字节与CSRF保护删除。
- Compose Contract Test固定Overlay、编排脚本、Loopback端口、HTTP到HTTPS 308、版本锁定、失败证据与Volume清理要求。
- GitHub Actions无论门禁成功或失败都上传机器可读`result.json`，失败时额外保留Compose日志与Playwright Trace/Video/Screenshot。
- Benchmark与Verify Workflow共用同一个Linux安装脚本；脚本从`versions.lock`读取Compose版本与SHA-256，临时下载、校验后再安装，Runner预装版本不得绕过锁定。
- `scripts/verify.ps1`在本地和GitHub Linux Full Verification中运行该链路，不允许因Docker、证书或浏览器缺失而跳过。
