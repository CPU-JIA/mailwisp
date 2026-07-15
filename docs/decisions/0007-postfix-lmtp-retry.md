# ADR 0007：由固定版本Postfix承担公网SMTP与持久重投

- 状态：Accepted
- 日期：2026-07-15

## 背景

MailWisp的Go应用已经具备有界LMTP入口与Durable Persistence语义，但单独证明LMTP Server正确不足以证明公网收件链路可靠。公网SMTP需要处理协议兼容、持久队列、退避、进程重启和临时故障重投，这些职责不应在Go应用中重复实现。

Reference Profile面向个人服务器，组件数量必须克制；同时不能以低占用为理由舍弃已经被成熟MTA解决的可靠性边界。

## 决策

公网SMTP由Postfix承担，Go应用只在本机或受控网络暴露LMTP。Postfix在SMTP返回成功前持久化队列，并在Go LMTP不可达或返回4xx时保留邮件和执行重投。

当前可复现测试基线固定为：

- Postfix `3.11.5`；
- Alpine package `3.11.5-r0`；
- Alpine packaging commit `2d8b64ef1eec4e46a8799e4c5970363a4a8eb40f`；
- Alpine `3.24.0` Linux/amd64 image manifest `sha256:33154315cf4402e697f065e6ec2156e292187e633908ccfede9c66279b6fa956`。

版本证据来自Postfix官方`3.11.5`公告、官方Download Mirrors和Alpine v3.24 APKINDEX。Postfix源码签名Key尚未完成独立获取验证，因此不得描述为“源码签名已验证”；当前信任链是固定Official Alpine Image Digest、Alpine签名Package Index、精确Package Version和运行时版本断言。

`deploy/postfix-test`只用于Integration，不直接作为生产配置。生产Postfix配置必须在域名、TLS、反滥用、DNS和运维边界确定后另行验收。

## 投递语义

MailWisp明确采用At-least-once Delivery，不声称Exactly-once：

1. SMTP已经接受、Go LMTP暂时不可用时，邮件必须保留在Postfix持久队列。
2. Postfix进程或容器重启后，Queue Volume中的邮件必须仍可重投。
3. LMTP返回4xx时，Postfix必须保留队列；恢复后重投同一Raw Message与Envelope。
4. Go完成Content与Metadata Commit后，如果LMTP成功确认在网络中丢失，Postfix可以重投。
5. 该重投形成独立Message记录；相同Raw Message通过SHA-256 Content Addressing复用一个物理Content Object。
6. 未知Recipient由Go LMTP返回`550 5.1.1`，Postfix不得无限重试。

未来如需面向用户暴露重复检测，只能增加可解释、可审计的幂等或折叠策略，不得把Message-ID等不可信Header直接当作Exactly-once依据。

## 自动化证据

Linux Integration使用真实Postfix进程、真实SMTP、真实TCP LMTP和真实持久Queue Volume，验证：

- LMTP未启动时SMTP接受并排队；
- Postfix重启后Queue ID仍存在；
- 启动Go LMTP并强制Flush后投递成功；
- 首次Durable Receive返回临时错误时产生4xx并成功重投；
- Commit成功但LMTP确认连接被中断时产生第二个Message ID，同时复用Content Ref；
- 未知Recipient产生`550 5.1.1`和`status=bounced`，原Queue ID消失。

重投测试在执行`postqueue -f`前必须等待对应Queue ID明确记录`status=deferred`。Receiver看到第一次投递并不代表Postfix已经处理完LMTP 4xx或连接中断；缺少该Fencing会在Race模式下过早Flush并错过本轮重投。

GitHub Actions固定Docker Buildx Action Commit，在Linux门禁构建测试镜像并复用GitHub Actions Build Cache。测试失败时上传Postfix Queue、有效配置和Container Log作为短期Artifact。Windows门禁继续验证原生Go行为，但Postfix Integration只在Linux执行。

首次完整Linux Integration与Race门禁证据：[`Verify MailWisp #29361463282`](https://github.com/CPU-JIA/mailwisp/actions/runs/29361463282)，结果为Success。

Deferred Fencing修复由[Run 29380943653](https://github.com/CPU-JIA/mailwisp/actions/runs/29380943653)验证，合并后`main`由[Run 29381217597](https://github.com/CPU-JIA/mailwisp/actions/runs/29381217597)再次验证全绿。

## 后果

- Go应用不实现公网SMTP队列和退避，保持职责清晰与较低常驻资源。
- Reference Profile增加一个Postfix进程，但换取成熟SMTP兼容和Crash-safe Queue。
- 投递端必须容忍重复Message；Content Store继续通过内容寻址控制重复Raw Data成本。
- 固定版本升级必须更新Digest、Package Version、运行时断言与完整故障测试，不能只修改Tag。
