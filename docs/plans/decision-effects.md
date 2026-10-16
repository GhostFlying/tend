# 待决事项回答的效果（C11）

**依据：`docs/findings/phase-c-acceptance.md` 未决问题 1；修订 D31，记为 D53。**

## 问题

按 D31，回答待决事项只关闭问题，后续动作由回答方显式发起。托管形态下回答方就是主 Agent，问题不大。外挂形态下 owner 常用 `tend decide` 直接回答，这时没有人执行回答：要等 owner 回到某个 agent 会话，agent 从增量里看到回答后才会动手，在那之前任务一直停在 `blocked: waiting_decision`。

## 决策（D53）

tend 自己开出的待决事项，选项里有 tend 能直接执行的，开出时就带上效果：

| 选项 | 效果 |
| --- | --- |
| retry the implementation | 请求下一代实现 |
| raise the budget | 请求下一代实现（请求本身会把预算提高到这一代） |
| retry acceptance | 请求当前 artifact 的下一代验收 |
| cancel the task | 取消任务 |

“revise the spec”“fix the acceptance environment”需要判断和改写，没有效果，仍交给 agent。planner（主 Agent）开出的待决事项选项是自由文本，不带效果。

- **同一事务**：回答选中带效果的选项时，记录回答和执行效果在同一个事务里完成，事件、outbox 一起提交（约束 2）。效果和对应的显式命令走同一条领域规则（`RequestImplement`、`RequestAcceptance`、`Cancel`）。
- **版本**：开出时记下任务当时的控制版本。回答时如果任务的控制版本已经变了（有人重试、修改、暂停或取消过），回答照常记录，效果不执行，结果里说明原因（约束 3：旧的回答只作为事实保存，不能触发后续动作）。领域规则拒绝时（例如任务已结束）同样只记录回答。
- **通知**：`decision.answered` 事件带上效果的结果。增量里回答那一行写明 tend 已经执行了什么，或者为什么没有执行；没有执行时这一行归为 attention，agent 必须处理。效果产生的任务事件（`task.cancelled`、`task.implement_requested` 等）照常进入增量。会话的 agent 下一次输入时就知道 tend 已经按 owner 的回答做了什么，不会重复执行。
- **可见性**：快照、`tasks_read` 和 `tend task` 里，带效果的选项标出 tend 会执行。`tend decide` 打印执行结果。
- 托管形态的 `decisions.resolve_from_message` 走同一个命令，规则相同。

## 实施

1. 迁移 `0010`：`decisions` 增加 `effects_json`（选项到效果的映射，默认 `{}`）和 `asked_revision`。
2. `protocol`：`DecisionEffect` 枚举（`implement`、`accept`、`cancel`）；`DecisionView` 增加 `effects`。
3. `domain`：`DecisionDraft` 增加 `Effects`，四处开出待决事项的规则填上。
4. `app`：`applyDecision` 存下效果和控制版本；`AnswerDecision` 执行效果，结果和事件里带 `effect`。
5. `contextbuilder`：回答行写明效果；未执行时归为 attention；选项标出 tend 会执行；外挂说明更新。
6. `cli`：`tend decide` 打印效果，`tend task` 和 `tend status` 标出带效果的选项。
7. `design.md` 写入 D53，并在 D31 中注明被修订。

## 测试

- `domain`：四处待决事项的效果映射。
- `app`：选中各效果后任务状态、动作和事件；控制版本变化后只记录回答；不带效果的选项和 planner 的待决事项行为不变；重复回答返回原结果。
- `contextbuilder`：另一个会话的增量里出现执行结果；未执行时进入 attention。
- e2e：run 失败 → owner 用 CLI 选 cancel → 任务 cancelled，agent 下一次 hook 看到。
