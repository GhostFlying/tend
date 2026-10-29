# P5：Attention、明确接续和个人规划（正式检查通过）

## 基线、交付与非目标

基于 P4 具名负责人闭环与 P3 schema 修正，追加 migration 59。依据 docs/issue-agent-design.md §5 与总计划 P5，Refs: D125。此文记录本批实际实施合同；规定 review、五项修正独立复审、正式完整检查及交叉编译已通过，CI/合并与部署仍分别待完成。

完整交付为实际 owner API/CLI + hosted main/manager 工具：创建同身份 attention、通过原 decision 回答并接续等待 worker/当前责任人、owner 明确确认人工动作、来源/期限修订后准确提醒、可读 Inbox/Next Action 及独立后台 task rating。无 UI 布局交互、P6 chat/consultation/run supplement/manual source adapter、外部连接器、部署或新巡逻 agent。信任唯一 owner、其主机/配置；只校验实际角色/Issue scope、并发/版本、重复/迟到/unknown 和原 task/正式验收边界，不引入 request/event 真实性证明链。

## 当前可复用事实与必须修的接点

| 实际代码 | 已有行为 | P5 的最小改动 |
| --- | --- | --- |
| app/decisions.go OpenDecision / AnswerDecision / answerEffect | 唯一 decision/version/answer 与已授权选项效果；owner 直答和 main 转录原文 | Attention 引用，不复制答案。新 canonical 问题复用 OpenDecision 的相同入账逻辑，回答仍走 AnswerDecision，保留实际 owner 原文及既定 effect 检查 |
| app/worker_questions.go AskOwner / recordDecisionAnswerSource / assignmentQuestions | 原 run 问题、同 question_key 去重、原 run 的回答/继续、旧 run 问题取消 | 新 canonical primary worker decision 自动获得 Attention 引用；回答仍原 decision/version/run，保留原身份、租约/workspace/slot，不用 retry 或聊天代替 |
| app/work_item_choices.go createChoice | 旧 choice 有自己的 version/answer；source_key 将同来源多个问题合并；旧写拒 canonical | 不解除旧 guard，不把它用于新 canonical 问题。旧 choice 可被 Attention 引用，答案读取/响应沿原 choice；新 canonical planning question 使用普通 taskless decision，两个同 source 的不同问题各有 Attention/decision ID |
| app/app.go persistOutcome 与 Execute notifier | 两处 wakesMain 抑制 main 自己处理的 decision.answered | 分别判断“需持久接续当前负责人”和“需叫醒 main”；两层都改，不能只改通知文本或 post-commit signal |
| app/event_wake.go recordDecisionWake / recordWorkItemChoiceWake | 回答会按聊天返回链接 queueSingleEventTurn，可能回错业务接续者 | 聊天 return link 保留，业务目标根据原 primary Issue/Attention 和当前 assignee/epoch 选择。main relay 不吞 manager 输入 |
| app/background_queue.go appendBackgroundSource / freezeBackgroundBatch、P4 ensureCanonicalManagerConversationForWake | 合并未领取输入，领取时冻结；具体负责人当前不可运行也保留事实；owner assignee 返回空 conversation | 定向追加原 Issue 的回答输入；owner assignee 的待处理事实留 Attention/Inbox 并给 main 协调输入，不能静默丢弃；不伪造 manager，不修改 assignment |
| app/followup_trigger.go SweepFollowups / RunFollowups、followups.go Save/normalize、background_checkin.go cancelBackgroundFollowups | 一个已有 30s sweep、occurrence key/revision/unknown/未领取合并 | 在同 sweep 支持 canonical deadline 的固定 at 窗口，不启第二 timer/daemon；旧 FollowupCommand guard 保留 |
| followups.go cancelPendingFollowups | 直接 outbox 取消硬编码 main_thread；batch 路径另有取消函数 | 同时覆盖 main 和明确 manager destination，仅 pending+未领 occurrence 可取消；已领/unknown 保留原事实，不改成未执行 |
| scheduler/scheduler.go Score/Compare、app/tasks.go RateTask | task rating + project weight +等待时间决定后台领取顺序，running run 不因 rating 改合同 | 不改评分公式。全局用户动作顺序、Issue 规划顺序、task rating 分别保存/展示并明确实际效果 |

## Attention 最小模型和状态

独立稳定 `attention_id`；required kind=decision/action/information、提出者实际 principal、来源引用、文本/问题、必要动作/选项、revision、状态和时间。具体责任项有 issue_id；仅全局个人规划或跨项问题可无单一 issue_id，manager 不可创建这种全局对象。可选 task_id 必须当前 primary 属本 Issue；已有 decision/choice 的引用包含原业务 ID/version，而不是重复的 answer_json。处理者为 owner 或明确具体责任人，和 Issue assignee 分开；提出问题、回答、归档都不改变 assignment epoch。

业务状态 open→resolved / withdrawn；information 为非阻塞知会。稳定 Attention ID 通过 revision/原 decision version 和历史表达修订，不建通用 replace/superseded 链或批量替换。新的独立问题才新 ID；撤回不伪装 answered。deadline/任务/Issue 终态不会隐式回答。presentation 的 read_at/archived_at 独立字段，只影响 Inbox 过滤，不改业务状态或阻塞含义；未解决 archived 对象仍能按 unresolved 查询、仍满足真实 blocker。

- decision：Attention 的 answered/resolved 投影来自原 decision 的实际 answered 状态或旧 choice 的 answered 状态；新 typed `attention.respond` 适配到原 AnswerDecision，expected attention revision 和原 decision version 都校验。原 `decisions.respond/resolve_from_message` 也在同事务更新/投影此 Attention 并路由，不能产生第二答案。旧 choice 通过原 respond/resolve，canonical 不建第二 choice 写路径。
- action：owner 当前认证命令可明确确认已完成，保存说明及可选实际来源/结果；不强迫上传证明文件。owner 当前命令本身就是有效确认，不要求额外引用此前消息；main 转录仍引用实际 owner 原文。首片不让 agent 以自由文本自证“用户已完成”。
- information：展示/已读/归档不 resolve；不新增必须逐条 acknowledge 的义务；information 默认不阻 Issue 关闭，需要人行动明确使用 action。
- blocking：首片仅可明确 issue_close 的未决 decision/action，沿既有生命周期查询；不建立 future_dispatch 或通用阻塞框架。等待 worker、禁止继续执行仍用原 decision/已有 task 规则，不改变 active run/unknown。

首次派生 Attention 仅针对新事件/显式 attach，不重写旧历史或扫库 backfill。唯一键使用原 decision/choice 身份；普通人工行动/信息各自有 ID/原 request 去重，同 source 两个问题不共用 source_key。修订沿稳定 Attention ID；普通 taskless planning decision 可在原 decision/version 上细化问题/选项，旧 Attention revision 保存原题目/选项/version；task/worker 的冻结问题仍走原业务路径。撤回普通 planning 问题置原 decision cancelled，保留无答案的历史。修改后 revision+不可变业务快照和事件与 request 同事务。读/归档不触发 answered wake。

## 接续目标、main relay 和交接

将 event 的持久事实处理从 wakesMain 分开：decision.opened/answered/cancelled 无论由谁发起，都按真实业务引用维护 Attention；只有 self-main 的重复 main 唤醒可以省略。Execute 的 post-commit notifier 同样因新接续 outbox/batch 触发，不能只修 persistOutcome。

具体目标：原 worker 的正式回答仍由既有 decision/read/原 queue/receipt 机制返回同 run；当前 primary Issue 的 manager 另收到同 decision/version/answer 来源的待核对输入。任务已结束/原 run 不再适用时沿原 stale/cancel 规则，不重开；Attention 不把自由回答升级成新 task 权限。taskless decision 使用 Attention.issue_id 或显式业务引用，不按回答来自哪个 chat 选负责人。owner/user assignee 保留对象可见并唤醒 main 传递，问题不导致 reassignment。

沿 P4 原 conversation/batch/outbox 保存 event、issue、原因及已知 epoch。未领取 manager 输入在真正领取事务根据当前 assignee/epoch 重新核对；handoff 中等待，handoff 完成后交当前具体负责人。已领取沿原 execution epoch，旧写受原围栏；unknown 不重发、不改送另 owner 来规避对账。manager definition 暂不可用照 P4 保留待接续，不反向回滚实际 owner 答案。只合并 pending，批内保留全部 source；不在同一 batch 里合并不同业务问题。

main 可以转录真实 owner 对既定 decision/version 的回答；它不是每项审批 bottleneck，也不能自行替 owner 选择。manager 的本项原文回答转录可经同一真实 owner source/version 规则；不给 manager 跨项回答权限，不把 assistant narration 当 owner。需要兼容的 callback 继续包入 P4 execution/effect_seq，不靠 source_message_id 充当协调身份。

## Deadline 与既有 followup

Deadline 独立 revision/ID，归属 Issue，保存 source reference/revision、timezone（IANA）、nature=external/user_commitment/estimate、strength=soft/hard、time known/unknown、due_at（UTC毫秒，未知为空），可选明确可靠 estimate duration/unknown、reason。外来邮件/资料里的日期只能按其真实来源记录为 external，不自动成为 owner commitment。owner 已授权 main 可整理准确来源，manager 可提出本项估计；owner 当前命令是有效授权；main 转录修改具体承诺保存明确 owner 原文/指令，不自动延期，不追加原事件真实性证明。

默认已知到期前 24h 和到期两个 at 窗口，均投 main；显式配置可覆盖/关闭默认提醒并保存原因。注册时已进入 24h 窗口产生一次 near 提醒；已逾期只产生一次 due（不再补一个相同 late near）；未来正常产生 near+due。未知到期不建假 timer，出现在 Next Action 为时间待澄清；可靠 estimate 才给“最晚开始点”派生值，未知估时不造值。提醒只提示 owner/main 决策，不授权执行/延期/联系第三方。

复用 followup_rules/occurrences/targets 和同 SweepFollowups。新增 canonical deadline 接点只创建固定 bounded at rule，不暴露旧 periodic/condition guard，也不把 FollowupCommand 的 canonical 检查整体删除。Deadline 当前快照存其两个 rule ID；followup_targets 绑定 deadline+issue，用既有 rule revision/occurrence key 保证每窗口一次，不新建 timer/queue/proof ledger。期限投 main 的 route 明确，不由关联 Issue 自动重路由到 manager；需要本项后续时 main/manager 另走明确业务工具。

改期/撤销/来源或提醒配置实际变更使未领取旧检查失效；assignment 变化不重建全局 main deadline timer 或重复提醒，只影响后续本项动作接收人。具体失效：按当前 deadline revision 和目标在领取事务校验，取消 pending 直接 outbox 和 pending batch 中相应 source，不能清别的 source。已 frozen/starting/processing/unknown 保留审计和对账，不重写为 cancelled 未执行；新 revision 的提醒仍需避免与未决 unknown 重复作用。重启立即 sweep 补已到窗口，已有 occurrence/key 不重复；时钟回拨不重复已完成窗口。Issue pause 停新任务派发而不撤销业务 deadline，提醒暂缓另有显式 snooze/提醒暂停；Issue close/cancel 按已收尾真实义务撤销未领取提醒，保留历史。无变化 sweep 不发“仍等待”的消息/用户推送。

## 用户 Next Action、Issue 规划与 task 排序

程序不实现通用自动评分器。主 agent 解释和安排用户动作次序，保存显式 rank、动作、责任主体、来源、触发/阻塞条件、期望时间、已知/未知估时、状态（open/blocked/done/cancelled）和目标引用。引用 Attention 的条目用其真实业务状态投影，不能在两个地方复制回答；其他个人行动可独立存在。manager 只提供/更新本项候选动作，不重排跨项用户计划；owner 可直接修改。

Issue 的后台规划顺序是独立 rank/说明，用来指导主 agent/manager 选择下一项关注，并明确它本身不改变 scheduler。实际 worker 领取仍用原 task Rating/project Weight/等待时间：为 named main 仅开放已有 `tasks.rate` 的窄操作，目标必须为 canonical primary task、expected task control revision，沿同 effect_seq/原 RateTask；不授 UpdateIntent/CreateAction/cancel/原生执行。manager 仍仅 rate 本项 primary。若用户说提高某 Issue 执行优先级，main 必须实际 rate 选定未完成任务并说明变化，不以仅改 rank 冒充生效。已开始 run 和冻结 spec 不改，排序不会解除依赖或 source blocker。

## 最小存储与公共入口

候选追加 server migration 59（正式按最新编号定，不能改历史/占空号）；不改 server/runner wire，不触 runner journal：

1. attentions 当前行 + attention_revisions（业务快照），decision/choice 稳定引用唯一，issue/kind/status 与 unresolved 查询索引，read/archive 只是呈现字段。
2. issue_deadlines 当前行 + issue_deadline_revisions，当前 rule IDs、来源、nature/strength/known；规则/occurrence 复用旧表，followup_targets 增加 deadline 关联值，无第二 schedule 表。
3. planner_entries：type=user_action/issue_priority、rank/revision、目标/责任/条件/时间/来源与状态；排序变更事件留审计。两类列表独立排序，不与 transport inbox 表混用。

同一个 command 事务内原 decision/owner action、Attention/current revision、不可变快照、event、outbox/request 共同提交。无需历史 ledger backfill/第二份 answer 存储。owner 读取 Inbox/Next 页面为 views；不是复用内部 inbox transport 去重表。

拟 API：GET /api/v1/attentions（status/kind/issue/archive）、/{id}；POST 原 /api/v1/ops 接 `attentions.create/update/respond/confirm/withdraw/present`、`issues.deadlines_put/cancel`、`planner.entries_put/reorder`。GET /api/v1/inbox、/api/v1/next-actions、Issue detail 同 ID 摘要与 deadline/plan order。复用 ops stale detail、OwnerTaskSpec 等现有模式，不改旧字段语义。

CLI：`tend attention list/show/create/respond/confirm/withdraw/read/archive`，`tend issue deadline ...`，`tend next-action ...`/`tend planner ...`。沿 P1c现有 JSON inline/file、发送前可见自动 request ID、可选原 ID+完整原 Envelope/revision 重放；同 Envelope transport retry，不另建默认磁盘请求账本，stale 不偷偷刷新重写。

Hosted tools 使用 P4 NamedSpecs/HostedCoordinationCommand：main 的 attention/期限/全局规划和窄 tasks_rate；manager 的本项 attention、owner answer relay、本项候选动作；current principal/role/epoch 和原 decision version 守边。consultation 无业务写。worker 不因 Attention 获新权限，继续现有 ask-owner。DTO、payload、GET roots 在 protocol 唯一定义，schemagen→TS，只生成类型/无布局逻辑；app/services 不依赖 runner。

## 顺序与验收

一个 coherent P5 候选，不再交付多层无 caller 存储片：先真实 Attention/decision+action/information/API/CLI，再两个 gate 的 durable current-owner routing，再 deadline 同 sweep/revision/cancel，最后 NextAction/Issue rank/窄 task rating、schema/types。同一组合独立 review 和唯一 fullcheck；若实际规模必须拆，只能在可实际调用/可验证边界拆，不把未接续队列/未生效排序标完成。实施范围已核定本计划及五项最小化修正，先接通可调用 Attention/answer/wake 主链，再继续 deadline/planner。

必要正常路径测试：

- 同 source 两个新问题同时存在；回答一个不解决另一个；chat/link/Inbox/detail 指向同 ID/version/answer。重复原 request 只一次，稳定 ID 修订/withdraw 留历史，read/archive不 resolve。
- owner 明确认动作完成成功、无文件也合法；agent narration 不能伪确认；information read/archive 不新增待确认义务；open nonblocking information 不封死 Issue，实际 blocking action 必须明确收束才能 close/finish_cancel。
- 真实 worker ask-owner→main 转录真实 owner answer→原 decision/version/run 正式通路继续，并 durable 唤醒当前 Issue manager；taskless answer、owner direct answer、本项 relay同样正确。active worker 等待/human_control/unknown 保留原资源，不套 manager 无槽等待语义。
- main 自 relay 不自唤醒但 manager 仍被叫醒，两层 notifier 均覆盖；manager 忙只合并 pending；handoff 前后未领目标更新、已领旧 epoch fenced、owner assignee可见+main接续；manager配置不可用不回滚答案。
- 已知 deadline 默认 near/due；登记已进窗口一次 near、已过期一次 due；改期/撤销/来源 revision/时区、pause、停机补扫、clock rollback、pending direct/batch取消与其他source保持、已领/unknown真实保留；无变化重扫静默。external/commitment/estimate和soft/hard/unknown读回不混淆。
- owner/main 用户动作排序与 Issue rank读回；rank不谎称改变领取；main窄tasks.rate确实改变两个 eligible task 的原 scheduler次序，跨角色不借此改spec/新action/接管，active run原合同。
- public真实API/CLI+pooled工具回归、schema根/TS当前性/CLIstdlib依赖；定向race/vet后冻结再独立xhigh和root唯一makecheck，无真实模型/部署声明。

## 当前候选验证状态

P5 隔离候选已经 owner API/CLI、实际 pooled 工具和 App 用例接通；仍未正式纳入、独立 review、完整检查或部署。本轮采用 P4 r5 已复审的 receipt/repair/context 修正及迁移 58，不覆盖其原实现；P4 后续正式 E2E 修正仍需作为前件组合，不能把本片定向通过作为 P4 正式验收。

首轮定向 race 的两个失败已据实际 caller 修正：跨项来源的拒绝发生在 Authorize，负例改用原 Execute；转交用户后的真实 ledger continuation 被 fake main 误当新目标，测试现读取原 delegated source、checkin/commit，并精确验证三个原 Issue、一个 task 和仅一次 owner 接续，不改变有效生产路由。等待回答的原 worker 保持 run/lease 后提交产物，新的独立验收 run 完成正式验收，实施 rationale 不流入验收。

本轮定向 race 通过（App 49.359s、API 7.004s、mainthread 23.570s），含 Attention、deadline、planner、真实 HTTP/CLI、角色负例和具名 repair 回归；vet、schema 当前性/预算、生成 TS 当前性与类型检查通过。迁移 59 字节保持不变。Deadline 暂停/终态及其他项提醒保持、manager 只能提出本项未排序行动已通过实际入口检查。独立 review 与唯一完整 make check 是后续门槛。


## r3：独立预审四项修正

r2 固定范围独立预审发现四项正常路径 P2，全部采纳，不扩展单用户威胁模型。Deadline 按原有性质守权：manager 仅修改原 estimate，main 修改、降类或撤销已有 user_commitment 沿真实未撤回 owner 来源，owner 当前命令仍可直接处理。取消输入仅追加可选既有来源/版本与 reason，形成可调用的具名工具路径，不要求 proof 或主会话再次审批。

停机跨 near/due 时，未生成的过时 near 静默结束；已 queued 且确未领取的 near 沿原取消路径保留 occurrence 身份和历史，claimed/frozen/unknown 不改写，未知结果沿原对账规则处理。关联 open Attention 的 NextAction 不能独立写 done/cancelled，必须先经原 respond/confirm/withdraw，读模型只投影同一 Attention 的真实状态。main 转录行动/NextAction 完成检查 owner 原消息未撤回，跨 Issue 来源仍可用；owner 当前命令不需额外原文。旧 AnswerDecision 语义不改。

r3 在新隔离副本组合已复审通过的 P4 E2E 后继修正，原 r2 与 P4 证据保留。四项使用实际 caller 先红后绿；新增 owner 直令、main 跨项来源、正常新承诺取消工具、已领取/unknown 提醒保留正控。首次新增 cancel 正控发现 reason 未从具名 payload 映射，已最小修正并通过实际 pooled tool 调用，不止断言请求被拒。最终定向 race、vet/schema/types 结果随固定 artifact 记录；独立增量复核与正式完整检查待完成。


r3 最终定向检查：race 已通过（App 99.071s、API 6.903s、mainthread 29.401s、E2E 19.035s），包括本片四项回归、合法 owner/main 正控、原 worker 独立验收、具名工具及前件两条正式 E2E；vet、schema 当前性/预算、生成 TS 当前性和类型检查通过。取消的真实 pooled 主工具调用读取同一 owner 来源及 reason 后成功，owner CLI 原用法保留。独立增量复核与正式完整检查尚未完成，部署未执行。


## 正式纳入状态

本批在已通过完整检查和独立 review 的 P4 提交 `069289c5edb8da0f390b20407a77370d1961d274` 上纳入已审 42 路径 P5 独立批，P4 的 [PR #167](https://github.com/GhostFlying/tend/pull/167) 仍在 CI，尚未合并。生产、测试和生成件与原已审字节一致；共享状态文档保留 P4 的实际检查及历史失败，不回退为旧候选状态。迁移只新增 0059；本片不包含后继 Attention 最晚行动时间/提醒字段、P6、P8 或可见 UI。

本轮仅完成精确前后像与暂存范围核对，未重新生成、运行完整检查或规定 CLI review。历史候选定向 race/vet 和独立预审不替代本轮正式门槛，CI/合并及部署仍分别记录。


## 正式 review 修正范围

正式规定 review 提出的五项实际调用问题逐项复现后收口：main 转录 owner 来源须沿原准入投影；deadline 旧未知 occurrence 对账后新窗口可由既有 sweep 继续；近期限未领取提醒在实际投递冻结事务重新核窗口；planner 单项与列表共用 Attention 状态投影；Attention CLI 传递分页游标。保原 owner 直接授权、同 turn/已确认/冻结恢复来源、跨项合法 relay，以及原未知 occurrence 身份和同批其他输入。此片不扩展单用户安全边界或增加定时器/服务，原正式检查与本次实际 red→green/独立复审分别记录。


本轮五项均由正常调用复现：main 引用 queued/unknown 且无消费回执的四类 payload、旧提醒对账后 due/改期窗口停滞、未先 sweep 的实际领取、planner 真实 HTTP 列表/单项以及 51 条义务的 CLI 第二页。局部修复后定向 race 通过（App 138.249 秒、API 19.637 秒），受影响 App/API/views/CLI 的 vet 通过；owner 直令、当前 turn/已准入跨项及冻结 recovery relay、撤回来源、原 worker 独立验收和 claimed unknown 保留。协议、迁移和生成件未变；本次增量待独立窄审及正式组合检查，不运行重复全量检查。

P4 [PR #167](https://github.com/GhostFlying/tend/pull/167) 已 CI 全通过并 rebase 合并至 `668c8f74ff234dde4ae148d30637339fb4ad1002`，与本片原前件 `069289c5edb8da0f390b20407a77370d1961d274` 源码树相同。原正式纳入和历史检查身份保持；当前状态以此补记为准，不回写旧过程记录。
