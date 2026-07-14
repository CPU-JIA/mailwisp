# ADR 0001：采用单服务器模块化单体

状态：研究中，尚未最终接受
日期：2026-07-14

## 背景

MailWisp面向自托管场景，单台Linux服务器是Reference Profile而不是质量上限。项目优先保证正确性、安全性、用户体验和长期可维护性，同时追求低延迟、有界并发和合理资源效率。旧系统包含可参考的协议与持久化行为，但Transport、Job、Storage和Lifecycle职责混杂。

## 决策

当前假设是使用一个Go应用二进制，并按模块化单体组织。宿主机Nginx终止HTTP TLS并提供静态资源，Postfix负责公网SMTP与重试，通过LMTP向应用投递；PostgreSQL保存持久数据，Redis仅用于限流与短期缓存。

应用默认不使用PgBouncer，由pgx Pool直接连接PostgreSQL。只有测量证明连接压力需要PgBouncer时才重新评估。

此决策必须在竞品、工具链、固定版本、Benchmark和故障模型研究完成后重新确认，证据支持更优方案时允许替换。

## 影响

- 一名维护者可以理解部署与恢复流程。
- 进程内调用避免RPC延迟和Service Discovery复杂度。
- 模块边界需要由代码结构和测试保证，而不是依赖网络隔离。
- 后台任务需要明确Owner、取消路径和单实例协调。
- Reference Profile优先保证单节点体验，但不得制造无法合理演进的结构性障碍。
