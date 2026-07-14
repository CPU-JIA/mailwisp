# 数据库迁移

MailWisp使用不可变、单调递增版本的SQL Migration。任何生产切换前，必须实现基于PostgreSQL Advisory Lock的迁移执行器，并通过旧TempMail数据库兼容迁移和恢复验证。
