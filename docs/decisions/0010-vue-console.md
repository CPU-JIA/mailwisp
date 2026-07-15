# ADR 0010：Vue 3正式控制台与安全邮件渲染边界

状态：已接受（首个生产切片已实现）
日期：2026-07-15

## 背景

第一轮Framework Spike对比了原生TypeScript、Vue、React和Svelte。MailWisp控制台需要真实处理令牌登录、邮件列表、异步解析状态、错误与离线、中文/英文、主题、键盘访问和不可信HTML。单纯比较Hello World Bundle不能代表长期维护成本。

## 决策

- 正式控制台放在`web/`，使用固定版本Vue 3.5.39、Vite 8.1.4、TypeScript 6.0.3和vue-i18n 11.4.6。
- 首个切片不引入Pinia、Router或UI组件库；状态只存在Mailbox Composable，跨页面复杂度达到证据阈值后再增加Store。
- 生产构建只输出静态Asset，Go服务不运行Node.js；反向代理负责静态文件与`/api`同源转发。
- 用户偏好（语言、主题）可以存储在Local Storage；Capability Token不持久化，默认只保留在当前页面内存，降低XSS和共享设备泄漏影响。
- HTML邮件永远不进入可信DOM：先用DOMPurify移除脚本、表单和危险属性，再放入无权限`iframe[sandbox]`；iframe内CSP只允许内联样式、`data:`/`cid:`图像，远程资源全部阻断。
- 生产门禁固定执行Type Check、Oxlint、Unit Test、Production Build和npm audit；后续增加真实API Browser E2E与截图回归。

## 取舍

Vue运行时比原生和Svelte更大，但当前首个构建的核心脚本Gzip仍处于个人服务器可接受范围，换取成熟的组合式状态、i18n和测试生态。没有证据证明Runtime成为P95瓶颈前，不切换框架。

## 未覆盖

- 当前Canonical API仍使用Bearer Capability；HttpOnly Browser Session需要独立ADR与CSRF验证后再接入。
- 邮件附件下载API、内嵌图片CID解析和真实SMTP到浏览器E2E需要PostgreSQL/Linux环境完成Integration验证。
