# ADR 0025：本地确定性OTP/验证码提取引擎

状态：提议中（草案，未接受）
日期：2026-07-19

## 背景

临时邮箱最高频的用户动作是从验证邮件中找到并复制验证码。当前Canonical列表与详情只提供Subject与有界Text Preview，用户必须人工在正文中定位验证码；Webhook、实时领取与MCP等机器消费方也没有结构化验证码字段可用。

调研结论（docs/product/feature-dossiers.md §1，本ADR的规格依据）：业界没有任何公开实现依赖HTML结构权重或机器学习作为主路径；cloudflare_temp_email正则兜底、Apple通知扫描、2FHey与otphelper全部是「多语言触发词 -> 邻近约束 -> 候选正则 -> 负信号否决」的确定性规则引擎。同时存在两条零启发式标准通道：IETF draft-wells-origin-bound-one-time-codes定义的`One-Time-Code`邮件头，与WICG末行`@host #code`格式，命中即可满置信。

既有约束：

- ADR 0004把「在Durable Persistence前执行验证码提取」列为暂不采用；提取必须发生在Raw MIME与Message元数据持久化之后。
- ADR 0008定义Parser边界（Text Preview 512KiB、HTML Source 1MiB等）；不得引入第二套无界内容处理。
- ADR 0009规定解析结果由Raw Bytes与ParserRevision唯一决定、按Content落库；JSONB只用于有界、随Content整体读写的解析文档。

## 决策

在`internal/mail`实现纯Go确定性OTP提取引擎：无AI、无网络I/O、无新第三方依赖。Parser Worker在流式MIME解析成功后调用提取器，提取结果与`mail_content_parses`在同一PostgreSQL Transaction提交，满足ADR 0004 durable-first与ADR 0009单事务不变量。提取无候选不是Parse Failure：`otp_candidates`为空数组，Content仍进入`parsed`。提取是`(Raw Bytes, ParserRevision)`的纯函数，不读取任何请求态或用户态。

### 标准通道优先（零启发式）

1. `One-Time-Code`头（draft-wells-origin-bound-one-time-codes-00）：按RFC 6376 §3.2 tag-list语法解析；`code`标签必须恰好一个，`origin`可选，`embedded-origin`仅在有`origin`时接受。`code`值必须通过归一化后长度（≤32字节）与字符集（`[A-Za-z0-9-]`）验证。命中记`score=1.00`、`method="header"`。
2. 正文末行WICG格式`@<host> #<code>`：规范化换行后按位置严格解析（`@`标记token、恰一个U+0020、`#`标记token、可选空格后embedded host token；顺序错误或多余字符即失败，行尾多余字段忽略以兼容未来扩展）。命中记`score=0.98`、`method="origin_line"`。

任一标准通道命中即短路，不再运行启发式层。`origin`仅作展示提示写入候选，绝不据此跳转或提升任何信任等级——该头与正文都是发件人完全可控输入。

### 规则引擎五层（每层有界）

一、输入准备：`subject`与`text_body`优先；`text_body`为空时从`html_source`剥离文本（丢弃script/style/head/svg/注释与`display:none`、`visibility:hidden`元素，`a[href]`展开为「锚文本 URL」，块级元素转换行，HTML实体解码）。全文NFKC归一化，并把阿拉伯-印度数字（U+0660-0669）、扩展阿拉伯-印度数字（U+06F0-06F9）与全角数字映射为ASCII。预清洗删除URL与域名串、`Ending \d+`卡尾号、`<#>`前缀与SMS Retriever末尾11字符Hash、引号。正文扫描窗口上限64KiB；截断只发生在行边界，不劈开候选token；`subject`独立于该窗口始终参与扫描。

二、标准通道（见上）。

三、候选生成（上限16个，超限截断并复用ADR 0008的Recoverable Warning记录）：

- 数字码`[0-9]{4,8}`，边界自定义为「前后均非`[A-Za-z0-9]`」而不用`\b`（CJK与数字间无`\b`，否则「验证码123456」漏报）；
- 分组数字`[0-9]{3}[-\s][0-9]{3}`，去分隔后生成拼接候选并标记grouped；
- Google前缀`G-[A-Z0-9]{5,6}`；
- 大写字母数字`[A-Z0-9]{5,8}`且至少含一个数字，仅在触发词同行或窗口内才生成；
- 混合`[A-Za-z]*[0-9][A-Za-z0-9]{3,7}`，仅在触发词同行或窗口内才生成。

四、评分（起点0，保留两位小数）：

- 触发词位于候选前64字符内或同行：+0.45；位于候选后32字符内：+0.30（覆盖「747723 is your authentication code」语序）；
- 候选紧邻分隔符（`[:：]`或`is`/`是`/`为`/`です`）：+0.15；
- `subject`含触发词：+0.10；
- 候选独立成行或所在行≤12字符：+0.10（模板大字号独占行的无DOM文本投影，不解析DOM或字号）；
- 硬否决（直接丢弃）：looksLikeDate——4位数字落在1900-2099视为年份，8位数字满足YYYY(1900-2099)MM(01-12)DD(01-31)视为日期；候选前邻字符为`+`、`-`或数字，或后邻字符为`-`（电话号、国际区号、更长token片段）；货币码（USD/EUR/GBP/CNY/JPY）或货币符号（$、€、£、¥、￥）与候选邻接（金额）；
- 减分：黑名单词（discount/promo/gift code、barcode、unicode、versionCode、encode/decode、postcode、zip、order、invoice、tracking、ending、last）与候选同句：-0.60；候选位于引用行（`>`前缀、`-----Original Message-----`之后、`On ... wrote:`之后）：-0.30。

触发词表以otphelper `sensitivePhrases`全表为基础（覆盖en/fa/ar/de/es/it/tr/ru/he/pl/fi/lv/ja/ko/zh-Hans/zh-Hant），合并cloudflare_temp_email的CJK与EN关键词组，补充zh「动态密码/校验码」、ja「確認コード」、ko「인증번호」。短语表、黑名单、货币表与清洗规则全部编译进二进制随版本发布；任何规则表改动等于新ParserRevision，不做运行时热更新。未覆盖语言的预期行为是漏报而非误报——这是触发词白名单法的固有安全属性。

五、输出：`score>=0.50`的候选按分数降序、同分按首次出现位置靠前取前3写入`otp_candidates`；top-1冗余写入`otp_primary`。`method`取值：标准通道为`header`/`origin_line`；获得触发词邻近加分的候选为`keyword`；未获触发词邻近加分但越过阈值的候选为`standalone`。全部正则运行在Go RE2线性引擎上无回溯；单封邮件提取预算<5ms/64KiB。评分权重与0.50阈值是本ADR固定的初始常量，接受前必须经仓库语料标定复核；标定若调整取值，以修订本草案形式回写，不允许代码内静默漂移。

### 落库契约与ParserRevision

新增单调Migration（版本号以实际合入顺序为准），全部为可空或带默认值的增列，不重写既有行：

- `mail_content_parses.otp_candidates jsonb NOT NULL DEFAULT '[]'`。元素形状：`{"code":"归一化后≤32字节[A-Za-z0-9-]","method":"header|origin_line|keyword|standalone","score":0到1两位小数,"source":"subject|text|html","trigger":"≤64字节命中触发词片段或null","origin":"仅header/origin_line通道的展示域或null"}`；CHECK强制`jsonb_typeof='array'`、数组长度≤3、`octet_length(::text)<=4096`。
- `mail_content_parses.otp_primary text NULL`，CHECK `octet_length(otp_primary)<=32`：top-1冗余列，供收件箱列表免解JSON；不建索引，不参与ADR 0020游标排序或过滤，仅按需展示。
- `messages.otp_override jsonb NULL`，CHECK `octet_length(::text)<=128`，形状`{"code":"该邮件候选之一或null","dismissed":bool}`。

`internal/mail`的ParserRevision常量1->2。多候选采用JSONB沿用ADR 0009既定理由：有界、随Content整体读写的解析文档，不为每封邮件制造关系行；`trigger`只保存命中片段、不含整句也不含码值本身，避免形成第二份内容泄漏面。双层约束（Go边界+DB CHECK）与ADR 0008/0009风格一致。

### 存量重算策略

- 默认仅新邮件生效：升级后`parser_revision<2`的已解析Content不自动重算，避免大库一次性补算风暴。
- 提供`serve`之外的reparse管理子命令：以有界批次（`--limit`，默认1000）把`parser_revision<2`且状态为`parsed`的Content重置为`pending`，复用ADR 0009既有持久队列、Fenced Lease与有界Worker补算，补算完成在同一事务覆盖该Content的既有结果行（每Content仍恰一行）。命令可重复执行直至存量清零；重置是纯数据库参数化UPDATE，不写Content Store、不与ADR 0016磁盘水位保护冲突，可在`serve`运行时在线执行，补算节奏天然受2个Worker与既有Backoff约束。

### API与前端

- `GET /api/v1/inboxes/me/messages`与`GET /api/v1/inboxes/me/messages/{id}`在既有响应内附加`otp`对象`{primary, candidates, override}`；旧客户端可安全忽略。复用既有Capability/Browser Session授权与Inbox Ownership检查，不新增读取端点。
- 用户纠正：`PUT /api/v1/inboxes/me/messages/{id}/otp`写入message级`otp_override`，Body为`{"code":"必须等于该邮件otp_candidates中某个code","dismissed":false}`或`{"code":null,"dismissed":true}`；`DELETE`同路径清除覆盖、恢复引擎结果。服务端拒绝候选集之外的任意code（防御任意内容存储）；浏览器路径要求CSRF Proof。覆盖只影响展示层，绝不回写`mail_content_parses`，保持解析结果的纯函数性质。挂message级而非content级是明确语义：多Recipient共享同一Content时每个收件箱是独立视角，各自纠正互不可见；覆盖随Message删除与Inbox TTL一并消亡。
- 下游消费方（ADR 0026出站投递/Webhook、ADR 0027 `POST /api/v1/inboxes/me/messages/next`、v0.4通知渠道）读取同一份持久化`otp_primary`/`otp_candidates`投影进各自载荷，不得各自重新实现提取。
- Vue控制台：收件箱列表行尾与详情页顶部渲染OTP chip——等宽字体、按3位视觉分组展示、点击复制无分隔原码；chip必须与发件人域名并排展示，展示不构成邮件真实性背书（防「去上下文化」钓鱼放大，Gutmann & Murdoch WAY 2019教训）；次候选折叠在下拉；复制仅由用户手势触发`navigator.clipboard`，成功只出本地toast；码值不进入URL、localStorage、前端路由状态或任何遥测。

### 安全边界

- 验证码是短生命周期凭据：不写入日志、Metrics Label、错误码Detail或遥测，延续ADR 0009与ADR 0014纪律。
- 提取零外部I/O：不预取、不HEAD任何链接——一次性链接会被消费（等于替用户完成验证），也构成SSRF面。
- 详情页HTML继续走既有Sanitize与Sandbox iframe管线；提取展示层不渲染邮件内链接为「验证入口」。
- NFKC与数字映射防止视觉相同、字节不同的混淆码绕过长度与字符校验，并保证用户复制的码与展示一致。
- 纠正与读取接口都过既有授权默认拒绝；匿名Inbox的纠正记录随TTL清理。语料采集遵循「不去匿名化、真实码替换为随机同构码」的伦理范式。

## 影响

- Parser Worker每Content增加<5ms有界CPU；存储每Content最多增加约4KiB JSONB与32字节冗余列。
- API变更全部是加法字段与一个窄写入端点，兼容既有客户端与Adapter契约。
- 规则即代码：改进短语表或权重必然抬升ParserRevision，配合reparse命令形成可控、可审计的重算路径；不存在灰色的「同revision不同结果」。
- 提取失败面被压缩为「空候选」，不引入新的Parse终态，不影响ADR 0009的状态机与恢复语义。

## 暂不采用/明确不做

- LLM/ML主路径与可选AI Provider：违反单进程、零外部依赖；cloudflare的AI路径仅证明其可作未来可选Provider，本ADR不设计。
- 图片验证码OCR与PDF/附件内码：明确Unsupported，写入Compatibility。
- Magic Link（auth_link类）提取与展示：v0.2只做码通道；「展示即诱导点击」的安全语义与链接通道留待Webhook/规则切片另行ADR。
- 浏览器自动填充与跨站集成：MailWisp是网站不是浏览器。
- 规则热更新：规则随二进制版本走，改表=新Revision。
- 新表存候选：列级扩展够用；出现独立生命周期或索引需求时按ADR 0009纪律走新Migration演进，不无界扩大JSON。
- DOM/HTML结构与字号权重：公开实现无一真正解析字号，「短行加分」是其无DOM替代；结构权重留待未来Revision以证据论证，不做承诺。
- 升级钩子自动重算存量：默认手动分批，理由见存量重算策略。
- 用户纠正聚合反哺（如按发件人域自动降权）：引入状态化规则，与「解析结果=纯函数(bytes, revision)」冲突；v0.2只做展示层覆盖并保留纠正数据，聚合另立ADR。
- cloudflare-temp Adapter把`otp_primary`投影为其`metadata.ai_extract`契约：本ADR不承诺，兼容矩阵单独评审标注。
- 「复制后自动清理验证码邮件」（Apple Clean Up先例）：v0.2不实现；未来若做必须默认关闭。

## 验证要求

- Unit：tag-list恰一`code`与非法多`code`拒绝、无`origin`时不构成域绑定码、WICG严格顺序与失败样例、NFKC与阿拉伯/波斯/全角数字归一化、CJK无`\b`边界、分组码拼接与复制归一化、每条否决与减分规则（年份/YYYYMMDD、`+`/`-`前邻、货币邻接、含zip code在内的黑名单、引用行、卡尾号与`<#>`Hash预清洗）、多候选排序与同分取位置靠前、16候选截断Warning、0.50阈值与top-3边界。
- 语料门禁进CI：仓库内建testdata语料=开源交易邮件模板渲染（mailgun/transactional-email-templates、usewaypoint OTP模板、Redwiat）x 12语言触发词x 5格式族合成集，加自有账号真实收码的匿名化样本（真实码替换为随机同构码），加钓鱼语料抽样作误报对抗组；按语言桶（en/zh-Hans/zh-Hant/ja/ko/de/fr/es/pt/ru）与格式桶分别断言precision>=0.95、recall>=0.85；未覆盖语言只允许漏报不允许误报；规则表任何改动必须跑全语料回归；提供fixtures驱动的标定脚本输出混淆矩阵，作为权重与阈值取值的证据。
- Fuzz：提取输入按仓库门禁运行固定时长Fuzz（512KiB边界正文、RTL与零宽字符、正则对抗样本），不得Panic，所有输出不得突破候选数与字节上限。
- PostgreSQL Integration（固定18.4）：提取结果与`parsed`同事务提交、任一约束失败整体回滚；CHECK拒绝超长JSONB与超限数组；带revision 1真实数据升级后旧行不变且可读；reparse命令分批重置后由既有Worker补算、结果行被覆盖且revision=2；空候选路径仍为`parsed`。
- HTTP：列表与详情`otp`字段形状、override PUT/DELETE的Capability与Session+CSRF授权、非候选code拒绝、跨Inbox访问统一404、override在响应中的优先语义。
- 前端Unit与Playwright：chip渲染（无/单/多候选）、用户手势复制且剪贴板为无分隔原码、发件人域名并排、纠正与dismissed全流程、码值不出现在URL与Storage、zh/en文案与主题状态。
- 日志与Metrics检查：提取路径日志、错误码与Label不含码值、地址或正文。
- Benchmark：<5ms/64KiB作为回归基线记录，不作为生产容量结论。
- 全仓门禁按AGENTS §13执行：gofmt、`go test ./...`、Race、vet、govulncheck与安全扫描全绿。
