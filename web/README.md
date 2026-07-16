# Web应用

MailWisp Web Console使用Vue 3、TypeScript、vue-i18n与Vite。Node.js只参与开发和Asset构建，生产环境由Web Edge提供静态产物，并将`/api`同源转发至Go应用。

```powershell
npm ci
npm run dev
npm run typecheck
npm run lint
npm test
npm run test:e2e
npm run build
```

`npm run test:e2e`使用Route Mock专测前端状态机；仓库根目录的`./scripts/e2e-compose.ps1`使用正式Nginx、Go、Postfix与PostgreSQL运行生产链路，两者不能互相替代。

版本全部固定在`package.json`和`package-lock.json`。Capability Token只保留在页面内存，不写入Local Storage；语言和主题偏好可以本地持久化。邮件HTML经过DOMPurify后放入无权限Sandbox iframe，iframe CSP禁止外部网络请求。消息列表使用`pagination.next_cursor`按需加载更早来信；展开历史后仍轮询并按Message ID合并最新页，避免Offset在并发收件时重复、跳项或静默停止实时收信。
