# ADR 0016：Content Store使用双阶段磁盘水位保护

状态：已接受
日期：2026-07-15

## 背景

单Inbox配额只能隔离某个收件地址，不能阻止大量Inbox、异常投递或其他主机进程共同耗尽Content Store所在文件系统。依赖操作系统在写入中途返回`ENOSPC`会产生更多Staging残留、数据库故障和主机级连锁问题；只在LMTP DATA前读取一次剩余空间又会被并发会话同时穿透。

## 决策

- `MAILWISP_CONTENT_MIN_FREE_BYTES`定义Content Store文件系统必须保留的可用空间，Compose主推荐Profile默认1 GiB。
- LMTP在回复`354`并接收DATA前执行快速容量检查。水位不足返回可重试的`452 4.3.1`；文件系统探测失败返回`451 4.3.0`。
- Content Store在创建Staging文件前再次执行权威检查，并在进程内互斥账本中为一个`CONTENT_MAX_BYTES`窗口预留容量，写入完成或失败后释放。
- 同一进程内的并发写入不能共同预留超过当前可用空间减去安全水位的容量。实际文件系统可用空间仍在每次检查时重新读取，以纳入其他进程和Docker Volume上的变化。
- Storage Pressure Metrics只使用`capacity`与`check_error`两个固定Reason，不包含路径、地址或主机标识。
- 这是临时系统容量故障，不是Recipient永久配额；Postfix必须保留Queue并在空间恢复后重投。
- `/readyz`继续允许读取和删除现有邮件，不因磁盘水位进入全局Unready；写入准入由LMTP状态码与专用Metrics表达。

## 影响

- 每次DATA预检与每次Content Store写入各增加一次文件系统容量查询；它们不位于每个Read Chunk的Hot Path。
- 预留窗口按最大单封Raw MIME计算，在磁盘接近水位时会保守拒绝部分本可容纳的小邮件，以换取并发边界清晰和实现可审计。
- LMTP预检可能因并发或外部磁盘变化在随后写入时失效，因此Content Store写前检查才是权威边界。
- 进程外写入仍可能在检查后快速消耗磁盘；操作系统写入错误继续映射为临时投递失败。该机制降低风险但不虚假承诺跨进程硬配额。
- 运维仍需监控宿主机和Docker数据目录水位；默认1 GiB是安全起点，最终资源建议以Compose容量测试为准。
