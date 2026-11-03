# 摘要和交接的记录审计

使用实际 instance 标识读取，不能把界面会话名猜作 instance：

```sh
tend conversation handoff-audit --instance INSTANCE --after 0 --limit 50 --json
```

返回 `items` 中的稳定事件 ID、sequence、kind 和记录时间。`more=true` 时以原 `next_after` 继续；所有页属于有界 retained ledger 读取，`history_complete=false`，不能将返回条数当整个生命周期的交接次数。

未来的 `identity_observation=recorded` 项冻结该事件发生时的 instance、原 thread、source version/generation，以及当时已确认的 target/handoff。准备时尚未取得的 target 为空；后来确认 target 不会回填准备事件。摘要尝试的开始、失败和成功分别记录，同 source 重试不会被当前 attempt 表的一行合并为一次。

旧空 payload 或缺少明确身份的记录为 `observation_unavailable`。它们仍保留原 ID、kind 和时间，不以今天的 binding 补齐旧来源。接口不返回 prompt、摘要正文、文件路径或凭据。

`summary_started` 只表示本地已记录尝试，不证明 provider 已排队或开始生成；失败事件没有记录原因时不能推断失败原因，也不提供排队时长。审计不会触发新摘要、重试未知外部操作、刷新运行状态或改变准入。

主 Agent 的 `conversation_handoff_audit` 提供同一只读契约（instance/after/limit）；其它 Issue/执行器没有该工具权限。
