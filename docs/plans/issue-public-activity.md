# Issue 历史作者与公开过程

## 业务起点与终点

Issue owner 正在后台处理时，用户应看到原执行的公开工具步骤；主动通知应显示发布它的冻结 Agent 身份。终点是历史作者准确、运行中过程可读、空态与整轮用时诚实，并保留通知与静默 check-in 的原发布语义。按最新用户调整，本代码责任以必要验证、真实独立审阅和受保护合入完成；本轮全部部署和生产验收责任已取消。设计依据为 D256。

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
5. 保存检查、失败复现与独立审阅原件；受保护合入即完成本代码责任，不承担发布或生产验收。

## 读取与刷新

`tend conversation block <message-id> --json` 保留普通 `items/more/next_seq`，并返回独立 `process.items/more/next` 及持久开始/结束毫秒时间。继续普通回复用 `--after <next_seq>`，继续公开工具用 `--process-after <process.next>`；二者可同时传入。读取和重连均无业务副作用，不等于通知发布或接受结果。工具变化使用现有合并刷新机制的 `conversation.process_changed` 通知，按原 trigger 合并且不唤醒模型；无新工具、批次或永久刷新历史。

## 独立审阅后的同链修正

真实 Codex 审阅及定向故障复现发现：callback 以 main_agent+跨会话 reply_to 插入会触发历史 conversation_inherit trigger，必须保持 producing Issue 会话；公开工具原件读取失败只能使 process 不可用，原讨论仍可读且应有显式错误/重读入口；工具去重必须沿 process arrival 顺序，不能先插入较晚 callback；整轮结束还须遵循持久消息处理状态，恢复等待中的原责任不能因某次 native turn 已结束而停表。补充这些真实回归，同一审阅 Task 按修正 delta 复核。原失败和旧候选完整检查的中止记录保留，不记作通过；已通过且未变化的证据按实际源码影响评估复用。

## 正式第 1 轮审阅后的修正计划

真实 round 复核确认前四项已修复，另有两项同链 P2，采用下列有界修正：

1. 通用 `Chat → Transcript → ConversationQueue.moreBlock` 只支持普通回复的 `after`，不展示公开 process。共享 `buildTranscriptEntries` 仅为普通 `more` 生成继续读取；由已实现 `process_after` 的 Issue `activityTurnRows` 单独追加 process 继续读取。用真实 Queue + builder 回归确认 process-only 页没有无效按钮，普通回复仍能继续；用现有 Playwright 通用会话及 Issue 页面验证渲染边界。Browser plugin not available。
2. `main_turns.updated_at` 是可重复更新的记录时间，不是稳定结束事实。只读投影继续从原 turn 取开始时间并检查活跃 native turn；结束改取持久 `conversation.message_state` 中本次处理区间的首个终态转换（replied/processed/failed/interrupted）。重复或迟到的终态记录不延长该区间，新的 queued/accepted/processing/unknown 状态开始新区间；恢复等待或缺少已确认终态时保留无结束时间。不改业务写入、迁移、runner 协议或原失败事实。
3. 使用真实 App 后台轮次及注入时钟覆盖重复终态、native reconciliation 的重复记录、晚到终态、重新读取历史与新处理区间，保留恢复等待计时回归。先运行针对性 fresh race、实际消费者测试与渲染验证，再依影响计划执行必要检查与当前候选 CI；同一独立 reviewer Task 只按实际差异复核。

本地影响计划以已完整审阅且有最终 CI 的 `2b091400` 为差异基线，验证本轮新增源码及其反向依赖；此前未变范围保留原始通过/失败及补验组合证据，不能把旧完整命令改记为通过。PR CI 仍以实际 PR 基线完整计算并满足全部必需作业。实际已有单 CPU 子进程下多组并行的 watchdog 失败记录，本轮本地协调者采用两组并行、App 八片的有界预算，启动时记录真实线程和包预算；不降低断言或原测试 watchdog。

两项失败仍可继续原讨论/其他普通分页，不触发工具重放；处理后从原消息及 cursor 恢复。当前已有的原始检查、完整命令失败及后续通过复现均保留，不能被新结果覆盖。

## 合入前基线同步

另两项独立修复合入后，同步最新 main；仅设计文档追加位置冲突，保留既有 D254/D255，将本功能编号顺延为 D256。业务源码自动重放；比较修正前候选与同步后候选，结合当前基线 CI 和原 reviewer 的实际差异复核评估既有检查，不机械重复不变范围。部署取消决定不变。
