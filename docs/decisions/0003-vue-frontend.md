# ADR 0003：采用Vue 3构建Web控制台

状态：已接受
日期：2026-07-14

## 背景

旧TempMail前端已经增长为超过4000行的单一JavaScript和超过8000行的单一CSS，字符串模板、Inline Event与主题覆盖相互耦合。MailWisp需要完整管理控制台、中英文、主题、Accessibility、邮件详情隔离和长期组件维护。

项目使用固定版本对原生TypeScript、Vue、React与Svelte实现了同一交互切片，并执行Production Build、Type Check、Playwright与axe-core。

## 决策

Web控制台采用：

```text
Vue 3.5.39
TypeScript 6.0.3
Vite 8.1.4
vue-i18n 11.4.6
Playwright 1.61.1
axe-core 4.12.1
```

Pinia不作为默认依赖，只有跨页面共享状态复杂度证明需要时引入。生产环境只部署静态构建产物，不运行Node.js。

UI必须使用Semantic Design Token，并提供中文默认、英文完整覆盖、Light、Dark与Follow System。Preset数量不设KPI，每套Preset必须通过对比度、Keyboard、交互状态和截图回归。

## 证据

固定切片Production Build：

| 方案 | 总Gzip | JS Gzip |
|---|---:|---:|
| 原生TypeScript | 3.71 KB | 2.17 KB |
| Vue | 27.60 KB | 26.38 KB |
| React | 62.26 KB | 61.53 KB |
| Svelte | 18.12 KB | 16.74 KB |

Vue不是最小Bundle，但在Component、i18n、Accessibility、测试生态和长期维护之间最平衡。四套E2E最终全部通过；测试真实发现Favicon 404和Mist主题4.1:1对比度问题，修复共享Token后axe无Violation。

## 被拒绝方案

### 原生TypeScript作为完整控制台

拒绝。网络最小，但手写DOM、事件、Escape、Focus与生命周期成本最高，容易重演旧项目。

### React作为首选

拒绝。当前没有React独占需求，同功能Runtime最大，并会扩大Router、State、Form与生态决策面。

### Svelte作为首选

暂不采用。Bundle更小且表达清晰，但复杂管理控制台生态与Svelte 5长期演进仍需更多证据。若Vue成为真实前端性能瓶颈，优先重新评估Svelte。

## 影响

- 接受约24 KB Gzip相对原生方案的首屏脚本成本，以换取长期结构收益。
- 需要持续固定Vue、Vite、TypeScript、i18n和测试工具兼容版本。
- 邮件HTML不得直接进入Vue DOM；必须使用独立Sanitization和Sandboxed iframe边界。
