# 前端Framework Technical Spike计划与阶段性判断

状态：历史Technical Spike（Vue 3已由ADR 0003与0010正式接受并投入生产控制台）
日期：2026-07-14

## 固定比较场景

四个候选必须实现同一组最小Vertical Slice，不能用Hello World的Bundle大小代替产品结论：

1. 登录状态与失效状态。
2. 邮箱列表、分页和空状态。
3. 邮件详情与加载/错误状态。
4. 中文/英文切换，中文默认。
5. Light、Dark、Follow System。
6. 至少一套扩展Theme Preset。
7. API请求取消、重复请求保护和统一Error显示。
8. 邮件HTML隔离展示占位。
9. Keyboard Navigation与基本ARIA。
10. Unit Test和Browser E2E入口。

比较候选：

- 原生TypeScript + Web Components。
- Vue 3 + TypeScript。
- React + TypeScript。
- Svelte + TypeScript。

统一测试机器、Node版本、Vite版本、产物压缩方式和页面功能，记录：

- Source LOC和文件数量。
- `dist` 未压缩/压缩/Br压缩大小。
- 首屏脚本请求数。
- 初始脚本执行时间。
- 交互状态代码量。
- i18n与Theme代码量。
- Accessibility测试结果。
- Unit/E2E测试工作量。
- 升级、类型声明和插件兼容问题。

## 阶段性判断

### 原生TypeScript + Web Components

优势：Runtime最少，部署无框架运行时，长期依赖面小。

风险：需要自行定义Component Composition、Reactive State、Router、Form、i18n、Focus Management和测试工具；如果实现不严谨，很容易形成字符串模板和事件耦合。

### Vue 3

优势：Component模型清晰，TypeScript支持成熟，i18n与Accessibility生态完整，渐进式引入，适合将现有Vanilla UI拆成模块；Cloudflare Temp Email也验证了Vue在该产品类型中的可行性，但不代表必须照搬。

风险：Vite、Vue、vue-i18n、可能的Pinia和测试工具需要锁定兼容版本；不能默认引入全局Store。

候选版本：Vue 3.5.39、Vite 8.1.4、vue-i18n 11.4.6、Pinia 3.0.4；TypeScript首轮优先6.0.3，7.0.2只做非阻断试跑。

### React

优势：生态与第三方组件最广，招聘和长期外部协作容易。

风险：对本项目而言生态可能是过度选择；Router、State、Form、i18n、UI和Server/Client边界需要更多决策，容易增加依赖和运行时复杂度。

### Svelte

优势：编译时优化、组件代码直接、产物有潜力较小。

风险：需要验证大型管理控制台的表格、编辑器、Accessibility、测试和长期插件维护；不能只凭Hello World Bundle作结论。

## 主题与i18n验收

无论最终Framework是什么，必须先建立独立的Design Token Contract：

```text
color.surface.*
color.content.*
color.border.*
color.action.*
color.status.*
font.family.*
font.size.*
space.*
radius.*
shadow.*
motion.*
```

模式与Preset分开：

- 模式：Light、Dark、Follow System。
- Preset：只改变Semantic Token，不复制组件样式。
- 主题切换必须覆盖Focus、Disabled、Loading、Error、Success、Warning、邮件隔离区域和图表。
- 24套不是目标；只有通过对比度、交互状态、截图回归和维护成本检查的Preset才可加入。

i18n必须：

- 使用稳定Message Key。
- 中文默认，英文完整覆盖后才允许切换。
- 日期、时间、数字与相对时间使用Locale-aware Formatter。
- 不把接口错误英文直接展示给用户；使用Error Code映射本地文案。
- 支持长文本、复数、空状态和窄屏布局。

## 第一轮实际结果

统一环境：

- Node.js 22.20.0。
- npm 11.15.0。
- Vite 8.1.4。
- TypeScript 6.0.3。
- Chrome Headless + Playwright 1.61.1。
- 四套方案共享完全相同的Data、i18n Message、Design Token CSS和测试流程。

功能切片均包含：中英文、System/Light/Dark/Mist主题、Token登录、Inbox列表、Message详情、返回与退出。

### Source LOC

| 方案 | 方案专属文件 | 方案专属LOC | 共享LOC |
|---|---:|---:|---:|
| 原生TypeScript | 2 | 101 | 162 |
| Vue | 3 | 66 | 162 |
| React | 2 | 59 | 162 |
| Svelte | 4 | 68 | 162 |

React专属LOC最少，但大量JSX集中在长行中，LOC不能单独代表可读性。原生方案为了管理DOM、Escape与事件重绑定，代码明显更多，并使用整页 `innerHTML` 重渲染；若按生产CSP和安全要求改成逐节点Component，代码量还会继续上升。

### Production Build

| 方案 | 总Raw Bytes | 总Gzip Bytes | 总Brotli Bytes | JS Gzip |
|---|---:|---:|---:|---:|
| 原生TypeScript | 8,905 | 3,712 | 2,943 | 2.17 KB |
| Vue | 70,075 | 27,599 | 24,851 | 26.38 KB |
| React | 198,179 | 62,261 | 53,531 | 61.53 KB |
| Svelte | 46,160 | 18,123 | 15,980 | 16.74 KB |

共同CSS为3.44 KB Raw、1.27 KB Gzip。结果来自同一Vite Production Build，未引入Router、完整i18n Library、Form Library和UI Component Library，因此只能比较这个固定切片的基础Runtime成本。

### Type Check与Browser E2E

四套方案全部通过：

```text
tsc / vue-tsc / svelte-check
Vite production build
Playwright Chrome E2E
```

E2E对每套方案验证：

- 中文首屏。
- 切换英文并同步HTML `lang`。
- 切换Mist Theme并同步 `data-theme`。
- Token登录。
- Inbox列表。
- Message详情与返回。
- Console无Error。
- axe-core Accessibility扫描无Violation。

首次E2E发现所有方案都因缺失Favicon产生Console 404，修复入口后重新执行，最终4项全部通过。没有通过Ignore List掩盖错误。

首次加入axe-core后，Mist主题的Muted Content在Soft Surface上对比度只有4.1:1，低于WCAG AA的4.5:1，四套方案同时失败。将共享Semantic Token从 `#55777d` 调整为 `#45686e` 后，四套方案重新构建并全部通过。这个结果证明Theme必须建立在共享Token与自动化Accessibility门禁上，而不是为每个页面维护独立Dark/Color覆盖。

### Dependency与License

- Vue、React、Svelte、Vite、vue-i18n均为MIT License。
- `npm audit` 报告0个已知漏洞。
- axe-core 4.12.1与Playwright 1.61.1固定版本进入Spike门禁。
- 所有依赖使用精确版本，`package-lock.json`纳入仓库。

## 阶段性取舍

### 淘汰原生TypeScript作为主控制台方案

它的Bundle最小，但相同功能需要最多手写生命周期代码，功能增长后容易形成字符串模板、Inline Event和CSS覆盖堆积。原生方案仍适合极小Embed Widget，不适合MailWisp完整管理控制台。

### 淘汰React作为首选方案

React生态强，但在本切片中Runtime体积最高，且MailWisp没有必须依赖React生态的独占需求。选择React不会直接提升邮件产品正确性，却会增加Router、State、Form和生态决策面。

### Svelte保留为性能备选

Svelte的产物明显小于Vue，Component表达也清晰。风险在于长期生态、Svelte 5语法演进、i18n与复杂管理组件选择仍需更多验证。如果未来Full Console证明Vue Runtime成为真实瓶颈，Svelte是首要替代方案。

### 推荐Vue 3

Vue比Svelte多约9.5 KB Gzip、比原生多约23.9 KB Gzip，但换来成熟Component模型、TypeScript、vue-i18n、Accessibility/Test生态和更低的长期组织风险。对一个包含Admin、Mailbox、Message、Domain、Token、Webhook和API Docs的控制台，这个网络成本在Content Hash与Brotli缓存后可接受。

当前推荐组合：

```text
Vue 3.5.39
Vite 8.1.4
TypeScript 6.0.3
vue-i18n 11.4.6
Pinia 仅在跨页面状态复杂度证明需要时引入
Playwright 1.61.1
```

## 当前结论

Vue 3 + TypeScript是当前最平衡且真实可实现的前端方案。后续ADR 0003与0010已正式接受并实现中文默认/英文切换、主题、错误与分页状态、无权限邮件Sandbox、附件、Raw Source、键盘交互及Mock/Production/Disaster Recovery三层Playwright；本历史Spike不再保留框架选型待办。
