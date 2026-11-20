# 维护审计账本查询

以下均为只读命令，沿用各入口的 owner 权限。每次审计按计划执行，由维护 Agent 决定问题和取证范围。检测器的 case、游标和摘要可以帮助定位，原账本和原件始终可查。

| 要查的事实 | CLI | 怎样读 |
|---|---|---|
| 当前负责 Issue 的 Run | `tend issue show "$ISSUE" --fields issue_id,assignment_epoch,manager,responsibility_runs` | 从 `manager` 取当前 `task_id`、`run_id` 和状态。历史或其他责任按对应 Run 分别核对。 |
| Task 当前执行和 Run 历史 | `tend task show "$TASK" --fields task_id,lifecycle,execution,runs` | 从 `execution` 取当前观察，只有需要历史时才选择 `runs`。 |
| 等待条件和原 Run | `tend task waits "$TASK" --run "$RUN"` | `kind`、`target_id`、`observed_*`、`stopped` 和 `recovery` 属于该条原等待。也可按 `--status stopping\|waiting\|continued\|cancelled` 查找，两个筛选取交集。省略筛选可读该 Task 的全部等待。 |
| 实际停止和待对账收据 | `tend task manager-reconciliation "$TASK" --run "$RUN" --json` | 核对原负责人 Run 的停止/关闭、未处理事件、未决请求和阻塞项。对维护保留的普通 Orca Run 使用 `tend run maintenance-reconciliation "$RUN" --json`。`stopping` 且 `stopped=false` 表示账本仍无该等待的停止确认。 |
| 候选、基线和 CI | `tend issue deliveries "$ISSUE" --json` | 用 `delivery_id` 对应等待目标，核对 PR 的 head、base/target 和同一候选的 CI。追查 CI 唤醒使用 `tend task ci-continuation "$TASK" "$RUN"`，以记录中的原消息或事件 ID 核对处理。 |
| 活动、上下文和游标 | `tend issue activity "$ISSUE" --json` | `more=true` 时把 `next_before` 传给 `--before`。单条通知证据用 `--notification <position>`，该页的 `next` 传给 `--after`。持续读上下文增量用 `tend issue context "$ISSUE" --view incremental --limit 25 --json` 和返回的 `next_cursor`。 |
| 原件 | `tend message original "$MESSAGE" --json` 或 `tend run logs "$RUN" --after <seq> --json` | 消息或 Run 日志按原 ID 查。按 Issue 查材料用 `tend issue materials "$ISSUE" --kind messages --id "$MESSAGE"`。产物用 `artifact show`，附件用 `file download "$FILE" --out <path>` 取原字节。 |

`--fields` 接收完整 JSON 中的顶层键，保留原值。省略字段仍省略，显式 `null`、空值和 `unknown` 仍原样输出。加 `--json` 可看完整字段，Issue 可加 `--revision` 读历史版本。字段选择节省 CLI 输出，详情读取仍用同一个 owner API。等待筛选由支持这些查询参数的服务端处理。

例如，等候判断可以从 Issue 的当前 Run 开始，用原 Run 对应的等待确认条件和 `stopped`。如果探针没报 case，仍可按 `--status stopping` 找到未确认停止的等待，再按原 Run 查收据或日志。等待记录、物理停止、CI 与唤醒各有独立证据。
