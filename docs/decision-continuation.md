# 问答 handoff 的等待与恢复

条件答复通过新的显式 typed 入口把答案与后继调度分开。旧 `tend decide` / 原 answer
operation 无新字段时保留原来 answered 与 stopped 齐备后自动续接的合同；不要用旧入口
表达机器等待。新入口必须有 `continuation`，option、text、时间经过及自然语言不作为
typed 就绪证据。历史后继 action/run 保持身份，不因新策略重新执行。Refs: D174。

使用 `tend decision-continuation answer`，同时提供原 decision version、父 Task
`--revision` 与完整 `AnswerDecisionPayload`；`--data` / `--file` / stdin 均支持。新 operation
为 `decisions.respond_continuation`。以下三种策略都不跳过原 worker stopped 回执、原
Task/spec/action、Issue 责任或当前合同校验；缺策略拒绝，不改变旧命令默认。

```sh
tend decision-continuation answer <decision_id> --revision <task_control_revision> --data '{"version":1,"text":"现在继续","continuation":{"mode":"continue_now"}}' --json
tend decision-continuation answer <decision_id> --revision <task_control_revision> --data '{"version":1,"text":"等待单独授权","continuation":{"mode":"wait","reason":"由 owner 独立决定恢复时机"}}' --json
tend decision-continuation answer <decision_id> --revision <task_control_revision> --data '{"version":1,"text":"等待依赖完成","continuation":{"mode":"dependency","reason":"需要依赖 task 的原 spec 完成证据","dependency":{"task_id":"<dependency_task_id>","spec_version":1}}}' --json
```

`continue_now` 声明就绪意图，原执行未停时仍不能创建后继。`wait` 必须有原因，由独立
`resume` 释放等待。`dependency` 必须有原因及一个明确的普通 execution Task/spec version（拒绝 manager Task），以
该 exact spec 的真实 completed 事实为准。依赖 spec 变化、证据缺失或未完成都不能当成
就绪；不接受调用方的 `ready=true`。reconcile 只对账原依赖，不将普通 wait 改成就绪。

先读原 decision 的独立 continuation 投影：

```sh
tend decision-continuation show <decision_id> --json
```

owner API 对应 `GET /api/v1/decisions/{id}/continuation`。完整输出包括原
`decision_id`、`run_id`、`task_id`、`decision_version`，独立 `revision` 和父 Task 的
`task_control_revision`，`policy`、`state`、`resume_condition`、`blocked_reason`，以及
已提交的 `continuation_action_id`、`successor_run_id`、`answer_request_id`、
`resume_request_id`。无 policy row 的历史输出为 `state=legacy`、不含 policy，恢复条件
为 `legacy_answer_and_stopped`；它不会伪装为一个旧 wait 决定。读取只查账本，不探测模型或 provider，不启动原 worker。
`ready` 也不证明后继已启动；应分别核查 action/run 与实际运行回执。

恢复或对账都使用新的业务 request ID，并同时冻结这两层版本：Envelope 的
`expected_control_revision` 是父 Task revision，payload 的 `expected_revision` 是
continuation revision。CLI 必须显式给 `--revision`，不会在重试时替换为最新值。

```sh
tend decision-continuation resume <decision_id> --revision 7 --request-id <new_request_id> --data '{"expected_revision":3}' --dry-run > original-envelope.json
tend operation submit --file original-envelope.json --json

tend decision-continuation reconcile <decision_id> --revision 7 --request-id <new_reconcile_request_id> --data '{"expected_revision":3}' --json
```

两条写入口对应 owner operations `decisions.continuation.resume` 和
`decisions.continuation.reconcile`，target 类型均为 `decision`。`--file <payload.json>`
与 `--data` 等价，`--file -` 从 stdin 读取完整 payload。owner 可直接发起独立恢复；
协调工具的 resume 必须引用本次 execution 实际准入的 owner `source_message_id`，
它不能用 queued、withdrawn 或仅可见的消息替代授权。机器 reconcile 读取原冻结依赖的
完成事实，不要求伪造新的 owner source。

Main 与当前 IssueManager 使用真实 named tools：`decision_continuation_answer` 将实际 owner
答复提交到 `decisions.resolve_continuation_from_message`，必须给原 decision version、父 Task
CAS、完整非空 policy、实际 source 和 effect_seq。`decision_continuation_read` 输入
`decision_id`；`decision_continuation_resume` 和 `decision_continuation_reconcile`
输入 `target_id`、`expected_control_revision`、严格递增的 `effect_seq` 和完整 `payload`。
resume 的 payload 还必须有实际 `source_message_id`。IssueManager 只可处理其当前
assignment epoch 下的原 primary Task。Consultation 和缺少已持久 execution 的调用
没有这些能力。旧 `decisions_resolve` 保留无策略自动续接语义；条件答复使用新 named tool，不能把
未知字段塞入旧调用或把 Agent 解释当成 owner 的恢复决定。

已有 Attention 责任路由同样接收 typed `continuation`：owner 的 `attention respond`
保留 `version` 与原答复，合法上级责任通过 `attention resolve` / `attentions_resolve`
保留 responsibility revision、assignment epoch、原 question version 与 reason。
两者的完整 payload 都可带上述 continuation 对象。上级 Agent 在既有权限内处理自己
负责的问题，不需要额外制造一个 human 决策；其答复身份仍是 Agent，不能变成 owner
授权，也不能扩大原任务或交付权限。

传输失败、超时或失去回执时保留 unknown，不换 request ID、不刷新两层 CAS、不自动
重发。先查实际 continuation、父 Task 及后继 Run：

```sh
tend decision-continuation show <decision_id> --json
tend operation submit --file original-envelope.json --json
```

业务状态、原答复和后继身份是恢复依据；不保存第一次请求的响应副本。确认仍需提交时
才单次提交完整信封，同文答复和重复恢复不能改变 disposition 或创建第二个后继。
版本过期时读实际状态并作出独立业务决定，不将未知动作替换成新动作。
这些入口不增加 merge、deploy、改 task spec 或清除历史 unknown 的权限。
