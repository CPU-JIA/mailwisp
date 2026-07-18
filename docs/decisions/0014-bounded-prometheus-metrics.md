# ADR 0014：采用内部低基数Prometheus Metrics

状态：已接受
日期：2026-07-15

## 背景

MailWisp需要观测HTTP、LMTP、Parser、Retention和PostgreSQL Pool，但个人服务器默认不应被强制部署Prometheus、Grafana或OpenTelemetry Collector。Metrics标签如果包含邮箱地址、Message ID、Token KID或原始URL，还会制造隐私与高基数风险。

## 决策

- Go进程使用固定`github.com/prometheus/client_golang v1.23.2`和独立Registry暴露`GET /metrics`。
- HTTP只使用Method、ServeMux Route Pattern和Status；LMTP只使用固定结果类；Parser与Retention只使用固定Outcome；禁止任意用户数据Label。
- PostgreSQL只暴露Pool总连接、已借出和空闲连接Gauge；Retention额外暴露无Label的`mailwisp_content_deletion_pending` Gauge，表示可重试物理删除任务积压。
- Compose与Host-native Nginx明确对公网`/metrics`返回404；Endpoint只允许在Loopback或Compose内部Network采集。
- 默认不捆绑Prometheus/Grafana。需要长期监控时，由部署者把现有Collector接入内部Endpoint。

## 取舍

Prometheus Client增加少量依赖与运行时内存，但避免自行实现文本协议、Histogram和并发安全。独立Registry避免全局可变注册；不默认部署监控栈保持Reference Profile低占用。

## 验证

- Unit Test验证Metric名称、固定Label与OpenMetrics响应。
- HTTP测试验证未组合时404、组合后使用Route Pattern。
- LMTP并发上限测试验证Active/Rejected信号。
- 部署Contract Test验证Nginx公网拒绝`/metrics`。
