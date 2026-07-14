# 数据库迁移

本目录保存不可变、单调递增的SQL Migration，并通过 `embed.FS` 编译进MailWisp二进制。

规则：

- 已进入共享分支的Migration禁止修改，只能新增更高版本。
- 文件名使用六位数字版本与明确英文动作，例如 `000001_create_ingress_core.sql`。
- Migration使用Goose SQL Annotation，由受控Migration Role执行。
- 执行Migration时必须持有PostgreSQL Advisory Lock，防止多实例并发升级。
- 破坏性变更必须使用Expand/Contract阶段，并完成备份恢复与Rollback验证。
- Unit Test不得假装验证SQL；Integration Test必须在固定PostgreSQL镜像上真实执行。
