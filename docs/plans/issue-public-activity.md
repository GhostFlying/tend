# Issue 历史作者与公开过程

## 业务起点与终点

Issue owner 正在后台处理时，用户应看到原执行的公开工具步骤；主动通知应显示发布它的冻结 Agent 身份。终点是历史作者准确、运行中过程可读、空态与整轮用时诚实，并保留通知与静默 check-in 的原发布语义。合入后由统一部署责任验证生产业务，本修复不自行部署。

## 已核实调用链与决策

- `conversation_notify` → `publishCommunication` → `messaging_messages.execution_id` 保存真实 publication execution；activity 作者读取却只按 trigger 找 `main_turns`，漏通知关联。优先沿 publication execution 读 `hosted_executions` 和原 Run 的冻结 inventory；缺失时诚实回退，不查当前 assignee。
- runner 原生公开事件 → mainthread `recordToolUse` → `RecordToolUse` → `bufferBackgroundTool` → `background_replies`。后台 frozen 期间普通 transcript 尚无工具；结清后 `publishBackgroundReplies` 才产生正式 tool rows。增加 Issue block 的只读 `process` 投影，按原 batch/thread/turn 读公开工具记录和已有 callback 结果；不发布 deferred prose，不读取 reasoning，不改变 check-in、通知或业务执行。
- process 用独立 background arrival cursor 分页，结清后仍沿同一序列读取正式 ToolUse；UI 按 native turn/item 去重，不能将 process cursor 与 transcript seq 混用。公开工具存储失败保持原错，重连/历史读不重放操作。原有回复分页、activity 事件分页保留。
- callback → `PutManagerToolResult` 可先创建无名称/无 reply_to 的 ToolUse；后到的真实事件必须补全名称、参数和来源，保留已确认结果和终态，晚到 start 不复活。来源只取匹配的真实 native turn。
- 过程与整轮时间来自 main_turns 的持久开始/终止事实；缺失显示未知。无公开步骤时显示“处理中/尚未记录公开步骤”，不能推断正在思考。

## 故障时仍可执行动作与恢复

投影只读，失败不改变 Issue、Run、工具回执、后台批次或通知。用户可继续读原消息、工具原件和 Issue 状态；重连后从原 cursor 读取，同一 native item 不重复。缺少身份/时间/步骤时明确保留未知。原有 failed/interrupted/unknown 状态与物理退出边界不变。

## 实施与验证

1. 历史作者关联及真实 publication 路径回归，包含通知 trigger 与执行输入不同、后继改名与缺失历史身份。
2. 只读 process 投影、独立分页与时间，覆盖 frozen 工具、callback 先到、重复/倒序/late 事件、终态不复活、结清/失败/重连和历史读取。必须从真实 producer 构造，不向 block fixture 塞 items 冒充恢复。
3. UI 使用 process 事实展示工具并去重，空态和时间未知诚实。使用现有 Web 测试及 Playwright 渲染验证（Browser plugin not available）。同步 CLI block 的 process cursor、帮助与能力说明。
4. 按实际影响运行检查；协议类型为 owner 读取的新增可选字段，runner 通信不变。保留必要全量兜底、独立 `codex review`（指定模型/effort）、最终候选与基线 CI，带 tend label 的 PR 保护规则 rebase & merge。
5. 保存原件与生产验证步骤，交统一部署责任。

## 读取与刷新

`tend conversation block <message-id> --json` 保留普通 `items/more/next_seq`，并返回独立 `process.items/more/next` 及持久开始/结束毫秒时间。继续普通回复用 `--after <next_seq>`，继续公开工具用 `--process-after <process.next>`；二者可同时传入。读取和重连均无业务副作用，不等于通知发布或接受结果。工具变化使用现有合并刷新机制的 `conversation.process_changed` 通知，按原 trigger 合并且不唤醒模型；无新工具、批次或永久刷新历史。
