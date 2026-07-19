# MailWisp 长期产品路线图（定稿 v1.0）

> 状态：产品规划草案，等待逐版本 ADR 落地为准。本文档是方向共识，不是已接受决策；每项功能的实现规格见 [功能实现规格档案](feature-dossiers.md)，每条与既有 ADR 的偏离都在文末显式声明。
>
> 生成依据：现状代码盘点（前端/后端对 ADR 0024 的差距）+ 四条赛道竞品研究（临时邮箱 / 别名转发 / 自托管邮件服务器 / 开发测试工具）+ 自托管社区真实需求挖掘 + 三视角对抗评审（架构符合性 / 产品策略 / 社区真实性）。

## 一、战略定位

MailWisp = **自托管邮件工作台：公网真实收件优先、发送后置且默认走中继、API/Webhook 一等公民、自带现代收件 UI、明确不做完整 IMAP 服务器。**

三个经研究验证的结构性事实支撑该定位：

1. **纯 VPS 全栈是空心地带**。开源自托管临时邮箱双寡头 cloudflare_temp_email（10.5k★）与 MoeMail（2.7k★）全部绑死 Cloudflare Workers/D1，无法部署到纯 VPS 或内网；MailWisp 的 Compose 单机全栈已站在这个无人认真产品化的位置上。
2. **「真实公网收件 + 内容 Webhook + 现代工作台 + 五分钟 compose up」这个组合无人产品化**。Stalwart 0.8.2 起有面向系统事件的 Webhook、Forward Email 有别名 Webhook，所以不能说"无人提供"；但把这套组合成一个产品的确实没有——最接近的 Inbucket 已低活跃多年。
3. **社区共识：收件自托管低风险、出站送达率是弃坑主因**（HN/r/selfhosted 年经帖级证据）。为「收件先行、发送走 smarthost 中继」的排序提供依据；且竞品 issue 区最大负面来源是部署脆弱与文档漂移——MailWisp 已有的门禁/DR/供应链纪律本身就是获客武器，需要的只是产品化呈现。

## 二、目标用户与北极星

三类目标用户：

- **隐私自托管者**（匿名访客 + Owner）：每站一址、泄露即弃、对抗共享域拉黑。
- **开发者 / QA**：CI 收码、API 自动化、E2E 测试，日常入口是配置文件而非 Web UI。
- **中文自动化人群**：验证码接收 + Telegram/飞书推送。

北极星指标：

| 指标               | 定义                                                                                          | 起始版本 |
| ------------------ | --------------------------------------------------------------------------------------------- | -------- |
| time-to-OTP        | 从拿到地址到复制出验证码的秒数（CI 基准门禁）                                                 | v0.2     |
| 部署就绪时长       | DNS 就绪前提下 `compose up → doctor 全绿 → 首信可读`，由 doctor 打点，剔除 DNS 传播等外生变量 | v0.2     |
| 转正率 / 30 日留存 | 访客→Owner 转化、转正邮箱留存（毕业动线是否成立的唯一证据）                                   | v0.3     |
| 升级零数据事故     | 每个版本发布验收项                                                                            | 持续     |

## 三、产品主线：一条毕业动线

```
temporary（匿名即用）
   └─ 一键转正（保留地址与历史，绑定 Owner）
        └─ persistent_receive（长期收件 + 别名语义 + 自定义域名）
             └─ 域名验证后 persistent_full（转发 / 回信 / 有界发送）
```

每个版本交付这条动线的一段。「地址永续」本身是留存机制；自定义域名是对抗共享域拉黑的结构性解药。

## 四、版本路线

### v0.2 「开箱即用的验证码收件站」

打差异化，不打 me-too 补课（评审已砍掉浏览器通知、轮询设置、favicon 角标、客户端搜索——没人为这些换工具）。

| 交付项                        | 说明                                                                                                                                                   |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 创建器完整化                  | 域名选择、TTL 档位（**后端已支持仅未暴露**）；自定义前缀（服务层能力已存在，只需 Canonical 暴露 + 防枚举/保留字边界，小 ADR）                          |
| OTP 本地识别                  | 纯规则引擎，不出网不用 AI；行内 OTP chip 一键复制；配公开测试语料库 + 准确率基准进 CI（护城河建在可积累资产上，正则本身一周就会被抄）                  |
| 每邮箱最小 Webhook            | Standard Webhooks 签名 + 有界重试 + SSRF 出站防护；挂在既有 Capability 模型上**不需要 Owner**；复制已两次验证的 PG 队列模式                            |
| SSE 实时收件 + 原子领取长轮询 | 有界连接 + 轮询降级；`GET /messages/next?wait=` 原子领取正确性基线，SSE 只做界面优化（与既有立场一致）                                                 |
| `mailwisp doctor`             | 最小入站自检：MX/25/证书，**诚实分级「本机视角 / 公网未验证」**，附 swaks 外部探测文档                                                                 |
| 自动化面                      | OpenAPI 发布、`mailwisp mcp` 只读 experimental 子命令（同一二进制、stdio、Capability 认证、默认关闭——不增加服务端进程、不产生第二发布产物）            |
| 前端基建                      | Vue Router 与深链（URL 零凭据，需修订 ADR 0010）；E2E 重写成本显式计入                                                                                 |
| 发布工件                      | **LICENSE 落地**、README 重写（首屏 hero：AI Agent 自动收验证码）、版本化文档、升级承诺、资源占用基线、备份/恢复命令的产品化文档（命令已存在，缺曝光） |

> 注意：TTL 续期 API 从 v0.2 移除——架构评审发现它牵动 scope 位宽 CHECK 迁移，与 v0.3 的 Schema 批次自相矛盾；v0.2 只做倒计时展示。

### v0.3 「Owner 底座与一键转正」（唯一的大架构投资）

- **前置 ADR×4**：Owner 账户认证（Compose Secret 自举 + 密码+TOTP）；Owner 会话模型（有状态 `owner_sessions` 表，激活 ADR 0005 预留的 `wisp_ses_v1`，修复 AGENTS §9 撤销缺口）；**Schema 解锁**（`profile` 列 + `expires_at` 可空 + CHECK 守住 `temporary ⇒ 必须到期` + scope 位宽放宽 + Retention 策略化 + persistent 容量强制 + 续期总寿命上限，爆炸半径覆盖 LMTP 热路径在内的全部 unexpired 假设查询）；**一键转正语义**（Owner 认证会话 + 有效 Capability 双因子、事务原子改写、与 Retention 清扫的 TOCTOU Fence、原 Capability 处置）
- Owner 级 Scope 命名空间与多邮箱资源寻址 API 面（`/api/v1/inboxes/{id}`，与匿名单邮箱面长期共存）
- PAT 签发与管理（Token 语法已预留 `pat` 类型，生命周期已由 ADR 0005 锁定：默认 90 天 / 最长 365 天 / 不签发永久）；密钥与会话页
- 多邮箱列表/切换/命名；一键转正；**地址级停用（paused）一等操作**（别名生命周期 active→paused→deleted，也是对撒库垃圾的第一响应）
- 最小工作台壳层 + 概览页**以健康块为主角**（磁盘水位/队列深度/24h 入站成功率——指标已存在）
- DNS 自检从 doctor 升级为绿勾页（保持本机/公网分级诚实）
- **两个用户可感知钩子**（纯架构版本的 release note 等于消失）：Bitwarden forwarder 兼容 API（addy.io 形态，自托管 Server URL 当天可用）、mbox/eml 导出
- 门禁：新表全部进备份包/DR/provision 契约（ADR 0006/0022），升级前自动快照 + 回滚文档
- 兼容中心**降级**为只读 API + 诊断 JSON，从导航删除（用户一辈子看一次的内部布线）

### v0.4 「地址主权与自动化」

- **地址/别名模型 ADR**（`inbox_addresses` 多地址→单 Inbox、`retired_addresses` 墓碑、`inbox_catch_all_rules` 域级规则、LMTP 多地址路由）
- **自定义收件域名**：添加 + 收件侧验证（复用 DNS 自检）+ Postfix 配置管理决策——对抗共享域拉黑的结构性解药
- 服务端搜索 ADR（pg_trgm，中文短查询退化诚实披露，ADR 0019 基准复跑）
- Owner 级 Webhook 管理 + 死信 UI；规则 v1 **收缩为「匹配 → Webhook/通知」**
- 通知集成：Telegram Bot + **ntfy/Gotify**（英文自托管社区原生语言）+ 飞书/钉钉模板
- 入站认证展示 ADR（Go 内 DKIM-only + 明示 SPF 无法事后验证，**不渲染伪造绿勾**）
- Spam 立场 ADR（地址阻断 + 发件人黑名单；「不做内容过滤」也白纸黑字）
- 增长：cloudflare_temp_email/MoeMail 迁移指南（三个 Adapter 就是迁移漏斗）、GitHub Action + testcontainers + CI 示例仓库
- 星标/归档标记列迁移

### v1.0 「转发、回信与有界外发」（persistent_full）

优先级经评审重排——**转发高于写信**（别名品类核心动词是「转发到真实邮箱」；通用写信是 commodity）：

1. 出站架构 ADR：smarthost 中继默认、可选直发但 UI 明示风险、DKIM 签名、Outbox = PG 队列复制既有模式、**永不承诺直发送达率**；E2E 用固定 Digest SMTP sink 容器做协议证据
2. **入站转发**（第一公民）：签名 VERP 信封 + reverse-alias contact + From 重写 + 本域 DKIM 重签（SimpleLogin 同构，不用 SRS/postsrsd、不做 ARC）；垃圾不转发、退信回流阈值停用
3. 回信隐匿：reverse-alias 模式（零新增监听面）；SMTP 凭据模式推迟另立 ADR
4. 域名验证操作化、发送配额、滥用防护、日志隐私
5. `temporary` 永不发信（边界不动摇）；README 从 v0.2 起预告「Receive-first, sending lands in v1.0 — here's why」

### v1.1+ 与候选池

v1.1：通用写信 UI（草稿/附件/队列页，Stalwart 范式）。候选池（须真实需求信号）：Passkey/OIDC、PWA、DMARC 聚合报告、域名池健康监控、HIBP、转发像素剥离、多 Owner、**POP3 只读/JMAP**（正视「长期邮箱锁死在浏览器里」的流失风险）、本地 LLM opt-in、MCP 管理能力、公共 Demo 实例（滥用策略前置）。

## 五、明确不做

完整 IMAP/JMAP 服务器；直发送达率承诺与 IP 预热；外部邮箱聚合 Connector（除非单独 ADR）；云端 AI 提取（本地规则优先，LLM 须 opt-in 且支持本地模型）；公共收件箱模式；手机号掩码；余额/套餐/广告/多租户计费；Redis/消息队列/K8s/微服务；商业化若发生不收基础可用性的钱（Poste.io 反例），只考虑托管/规模/支持。

## 六、需要拍板的关键决策

**即刻（阻塞 v0.2 发布）**：

1. **LICENSE**——~~仓库已 Public 但根目录无 LICENSE 文件~~ **已拍板（2026-07-19）：AGPL-3.0-only**，官方全文与 README 许可证章节见 PR #43。取 -only 不含 or-later，未来许可证版本主动权保留在维护者手中；BUSL 明确排除。
2. 公开发布节奏是否接受（README 重写、Show HN / r/selfhosted 发布计划、awesome-selfhosted 提交）。

**随版本 ADR 批次**：

| 决策点                                              | 建议                                                                                                                                              |
| --------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| PAT 与 Bitwarden 兼容的 ADR 边界                    | 两个 ADR：PAT 独立先行（v0.3 底座），Bitwarden 作其首个消费方                                                                                     |
| whsec / 通知凭据 / DKIM 密钥的统一 Key Material ADR | 合并一份「服务器侧可签名密钥材料」ADR：Compose Secret 主密钥 + HKDF 派生，一次清掉 ADR 0005 三处欠账                                              |
| egress 网络形态（ADR 0013 修订）                    | app 挂 outbound-only 网络 + 进程内 SSRF guard，不引入 forward proxy 容器                                                                          |
| TOTP 是否绝对强制                                   | 强制 + `MAILWISP_OWNER_TOTP_OPTIONAL` 逃生舱（默认 false，安全文档披露降级后果）                                                                  |
| compat 创建的 Inbox 用哪种 Profile                  | `persistent_receive`（无固定到期受配额），需与「temporary 禁止永久」边界对齐                                                                      |
| 转发权限落哪一档 Profile                            | `persistent_receive` 完成 destination 验证 + 转发域出站就绪后即可转发（需松绑 ADR 0024「只 persistent_full 发信」对「代收改写」与「撰写」的区分） |

## 七、对 ADR 0024 的声明式偏离（三处，均有理由）

1. **验证码提取从 P2 上提到 v0.2 主打**——自托管侧全赛道空白、成本低、双人群通吃。
2. **兼容中心从 P0 UI 降级为 API/文档**——用户价值极低，Adapter 的真正价值是迁移漏斗。
3. **收件箱搜索从 P0 移到 v0.4**——临时邮箱平均个位数邮件，服务端搜索在多邮箱场景才有真实需求。
