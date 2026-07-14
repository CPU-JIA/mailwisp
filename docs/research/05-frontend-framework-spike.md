# 前端Framework Technical Spike计划与阶段性判断

状态：Technical Spike设计完成，最终选择待实际构建验证
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

风险：需要自行定义Component Composition、Reactive State、Router、Form、i18n、Focus Management和测试工具；如果实现不严谨，很容易重新形成旧项目的字符串模板和事件耦合。

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

## 暂定方向

如果Vertical Slice的构建、测试和Accessibility结果没有反例，Vue 3 + TypeScript是当前平衡候选；它不是已经批准的最终方案。原生TypeScript只有在我们能证明不会重新制造旧项目维护问题时才优先，React和Svelte只有在实测显著改善质量或交付能力时才采用。
