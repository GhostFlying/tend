# P1b：递归 Issue 控制与 primary task 接入

状态：P1b1 聚合存储与纯规则代码、本地完整检查及独立评审已完成，未注册产品入口；PR CI/合并未完成，后续分片尚未实施，本批无发布。Refs: D121、D124、D126；依赖 [P0 总合同](issue-agent-refactor.md)及 [P1a 内部改名](issue-internal-names.md)。本批起点为 `d52d093741da41e78dc60effb0493a67058eded5`；子批开始和评审时各自固定实际 base/head。准备草稿不属于已实现代码。

## 交付分片与启用门槛

P1b 保留 task/run 的独立状态机、冻结 spec、正式验收及 workspace 合同。内部基础先合并，公共 owner API/CLI 留 P1c；不注册新工具、不修改 Web，不把数据库存在新表称为功能可用。各分片分别写真实进度、独立 review、make check、PR CI 与线性合并记录；中间批不部署。

| 分片 | 完整且可核查的行为 | 依赖与门槛 | 明确未启用 |
| --- | --- | --- | --- |
| P1b1 聚合存储与纯规则 | 追加控制/边/不可变快照；单 revision 原子存取；树、组合义务环、生命周期谓词；旧 saver 的中央拒绝边界 | P1a；migration 与事务故障测试、legacy 回归通过 | 无 canonical app/HTTP/tool 创建入口，无 task 接入、manager 自动推进或 Agent assignment |
| P1b2 owner 内部命令 | 经 App.Execute 的 canonical create/update、父子/依赖修改和 owner 生命周期；逐入口 legacy guard；精确执行/unknown blocker；支持无执行义务时的取消闭环 | P1b1；来源/身份/版本/原子回滚、并发父项关闭测试；不允许创建 canonical task | 不接入公共 API；有活动 task 的 cancel 返回明确阻塞，不能声称已停止；无 Agent 转交/manager 控制 |
| P1b3 primary 读模型与上下文 | 显式 primary/reference 解析，stage-free detail/观察/上下文/证据范围、来源与 digest trigger；完成评估成员枚举改为 primary | P1b2；按下表逐消费者审计；验收 narrative 隔离、legacy 行为不变 | 无生产 canonical task 创建/领取入口；内部命令不越过此前生命周期 blocker；不启用未来 P5 attention/期限 |
| P1b4 task 与控制闭环 | canonical task 内部创建/绑定、primary/ancestor 领取门槛、正式取消协调、close/reopen、completion review 和已有 durable 通知目标接入 | P1b2、P1b3；所有 primary 消费者及 guard/unknown/cancel 故障测试通过，才开放内部 canonical task 分支 | 无公共创建注册；P2/P3 Agent 选择及 P4 manager 自主行为、P5 完整唤醒合同未启用 |
| P1c owner 接口 | 独立计划确定公开 DTO/ops/CLI；完整 P1b 闭环后注册新入口和生成 schema/type | P1b1–P1b4；重新评估实际 API/通信兼容边界 | 无未经用户确认的可见 UI；仍不部署 |

分片按实际 patch 大小可进一步拆出纯读取与通知接入，但不能把安全前提留给后续补丁后先启用路径。P1b1 的存储原子性和纯规则本身是交付行为；P1b2 命令仅由 app 测试验证，HTTP/tools 分发器没有引用。P1b4 之前不允许 canonical task 创建；测试若需要持久活动事实，通过正式 app fixture 或明确标注的底层故障 fixture 构造，不把临时测试写入当作产品入口。禁止用环境开关或全局 bypass 绕过 guard。任何执行路径缺少取消/unknown/primary 安全闭环则保持不可达。

## 身份、持久化与历史边界

沿用现有 opaque ID（`protocol.PrefixWorkItem`），不引入不被 ValidID/FK/workspace 接受的新前缀；不是把历史 ID 改名。过渡 `work_items`、`body_json.work_item_id` 与历史 JSON 原字节含义保留；P1c 的 Issue DTO 以 `issue_id` 表达相同值。只有真实 `issue_controls` 行代表 canonical 身份，旧行不推断 assignee，不自动采用、不回填历史业务。Parent/dependency 目标必须同为 canonical；历史结果引用只作明确证据。

检查时 migration 到 `0053_agent_recoveries.sql`；编码前重查下一个编号。只追加 migration，不改已有文件。新增对象：

- `issue_controls`：与 work_items 一对一；nullable parent、active/paused/cancelling/cancelled/closed、具体 assignee、正 assignment epoch、active/handoff 状态与待交接身份；约束字段组合并索引 parent/assignee。
- `issue_dependencies`：canonical issue 到 prerequisite 的显式边，拒绝自身/重复边；跨行图由同一写事务验证。
- `issue_control_revisions`：与 work_item_revisions 同一 `(issue_id, revision)` 的不可变控制/边快照及理由、真实 actor/request/source；禁止更新和删除。未来协调 effect provenance 只保存已被服务端验证的值，不从 owner payload 自报。
- assignment 交接记录：epoch、前任/后任、fenced/reconciling/completed 进度、具体未决义务与回执。尚无真实 Agent resolver 的阶段不能由任意字符串创建有效转交。

`work_items.revision` 是唯一聚合版本；controls 没有独立可变 revision。Canonical 保存同时写 coherent body_json、status/revision 列、controls/edges、plan 快照和同 revision 控制快照。现有 `store.GetIssue` 只解码 body_json，不能仅更新 SQL 列。旧 status 投影 active→open、paused/cancelling→snoozed、closed/cancelled→closed；取消不伪造 completed 或 closure evidence。当前与历史读取都要求完整对应快照，缺行明确报错，不能降级成默认 owner。

继续使用 `task_work_item_owners` 的 `task_id` 唯一 primary provenance，在 spec1 创建后插入一次；不另建竞争 ownership 表、不从 stages 或当前 links 重算归属。`work_item_task_links` 保留 legacy/可选 stage 信息；canonical secondary 关系仅表达结果引用，不能变成第二 primary。

## app、授权与原子保存

复用 `internal/app/app.go` 的 App.Execute、认证、去重、savepoint、事件和 outbox。同 request_id 重试沿原身份；所有副作用同事务，外部控制交 dispatcher 提交后发送。控制写同时核对唯一聚合 revision 和当前 assignment epoch。P1b 内部 operation 预留 `issues.create/update/pause/resume/cancel/finish_cancel/close/reopen/reparent/dependencies/assign`；拒绝未知 operation/action 和错误 target，P1c 再注册公开路由。

Canonical create 默认 assignee 是当前认证 owner 的精确 Subject；不接受伪造 owner 身份或未注册 agent_id。旧 `app.CreateIssue` 仍是 legacy 创建命令，另取清晰的 canonical 命令名，不暗改当前 wire 语义。未来 resolver 使用“执行环境稳定 identity + local Agent ID”；runtime binding 可更换但 run 历史快照冻结，definition categories 为多值，category 不授 implement/accept/owner 角色。P1b 不提前实现 P2 注册，也不扩大 `PrincipalBackgroundAgent` 或现有正式角色权限。

保存流程：加载聚合并校验来源/版本/epoch → 同事务收集父链、图、primary 子树和执行事实 → 纯规则决定 → 写当前与两个快照 → 记录 request/event/outbox。每一写入阶段注入失败，业务状态须完整回滚；正常失败 request 审计可保留。底层提取私有 projection saver，canonical saver 在完整校验后调用；legacy `Tx.SaveIssue` 中央发现 controls 行即明确拒绝，不部分委派，不用包级开关。

Legacy 必须逐实际写入口 guard，不能只靠中央 saver 的迟到拒绝：

| 当前路径 | canonical 拒绝点及回归边界 |
| --- | --- |
| app.issueForWrite；旧 Update/Close/Reopen/Link 等 | 加载后、业务写前拒绝；中央 SaveIssue 再兜底 |
| app.CreateIssue | 继续创建独立 legacy 身份，不自动采用历史行 |
| app.CreateWorkItemChoice / UpdateWorkItemChoice | 直接读 issue 或解析 choice.WorkItemID 后拒绝，再写 choice/message/version |
| app.FollowupCommand create | 复用 issueForWrite；其余 update/pause/resume/cancel/trigger 解析 rule.WorkItemID 后显式拒绝 |
| app.taskIssue / BindIssueTask | 旧 task 分支拒绝 canonical；P1b4 新 binder 走完整聚合事务 |
| app.workItemUpgradeEvidence | 是证据读取，不按文件名误判写入；保留有效历史证据读取 |

对每个旧 mutation 以其他字段均有效的 payload 验证 cutover 拒绝和业务零泄漏：current/revisions/spec/links/choice/followup/source binding/event/outbox 不变。保持 legacy-only 用例原行为。Run 结果、正式 decision 答复、delivery observation 等事实回执继续按原 task/run/版本校验接纳，不能一概因 primary canonical 而拒绝；事实接纳也不授旧入口修改 canonical 计划的权限。

## 树、依赖与生命周期规则

Parent tree 单独无环；另建“完成义务图”，`A → B` 表示 A 完成须等待 B，包含 parent→children 与显式 depends_on。分别无环不足以保证可完成：child 等 parent、或 descendant 沿依赖返回 ancestor 均拒绝；parent 依赖自己的 child 是重复同向义务，允许；兄弟间单向排序允许。Result reference 不进入控制图。

新领取只继承 ancestor 的状态/授权准入，另检查任务 primary 的自身显式完成依赖和可选 stage 前置条件；不继承 parent 的全部 completion dependencies。必须证明 A 是 B 的 parent 且 A depends_on B 时，B task 可领取，A task 等待，B 验证完成后 A 可领取；ancestor pause 则确实阻止 B 的新领取。不能把 parent 等 child 的完成义务误投成 child 自等待。

Prerequisite 满足要求 canonical closed + 实际 achieved completed endpoint + 服务端验证的持久证据，关联成功 `issues.close` 的 actor 去重域、request/event 与不可变 revision。Cancelled、declined、handed_off、superseded、legacy closed、导入 JSON 中自报 proof 均不能放行；缺证据返回 blocker，owner 可显式修改/移除/替换依赖。领域 `EvidenceVerified` 只能是服务端投影事实，不能成为 owner 请求布尔值。事实变化约束新领取，不隐式停止/重启现有 run 或改其 spec。

| 动作 | 要求与结果 |
| --- | --- |
| create / reparent / reopen | 同事务验证完整 ancestor 链，不能在 closed/cancelled/cancelling 或授权围栏下激活 child；child 单独 reopen 不越过 ancestor，需显式授权的 ancestor 同事务 reopen 才可放行 |
| pause / resume | 只门禁新 implement/accept claim；活动冻结合同继续，包括等答、human_control 和 unknown；实现完成而 paused 时保留 artifact/workspace，resume 后再领正式验收 |
| cancel / finish_cancel | 先持久意图并阻止新派发；明确 child/task disposition，走现有正式 task 控制及回执；所有义务核对完才 cancelled。取消未知不能冒充已停止 |
| close | 检查完成证据、primary task 和 canonical 子树、child 责任、completion review、attention/承诺、活动 turn/run、未决控制及 unknown；不靠空 stage 认定空闲 |
| reopen | 新责任 revision；不复活 terminal task/run、不抹历史 closure；ancestor 准入仍校验 |
| reparent | 移动子树无活动 responsible/native/logical turn、执行或 unknown；同项目/授权，完整图同事务重检；不移动/共享 workspace、不扩大来源授权 |
| assign | 单 assignee；先推进 epoch 围栏旧控制，再核对旧 turn/unknown，最后启用新责任；旧 epoch 仅留事实。相同 owner 是 no-op；真实 Agent 转交在 P2/P4 前不可用 |

P1b owner close 保守阻止活动 manager turn；P4 再定义“发起关闭的当前协调 turn”准确豁免，不广泛忽略活动轮。未来 attention 等义务尚无表时不能假称覆盖；存在的 decisions、source/逻辑责任和 completion review 必须查询。新增 P5 义务接入同一 blocker 合同后才启用其生产路径。

Unknown/blocker 使用实际持久表和身份：复用 `store/context_blockers.go`、`task_context_blockers.go`，查 task/action/run/lease/control/request/outbox，查 main_turns→conversation_messages→准确 conversation 的 starting/started、未完成 logical execution/context、`native_turn_receipts_pending`、recovery root/attempt/successor 及 compact/steer/handoff。`MainTurn.NativeReconciled` 是内存值，不能假造 SQL 列；main_turns 没有通用 unknown 状态。现有 HasActiveMainTurn 针对 main destination，不能直接当全部 manager 检测器。每类 blocker 返回具体 object/request/revision，重启后可复核，不重放 unknown。

## primary / no-stage 消费者审计与分片落点

以下来自 P1a 固定代码，不是已修复声明。禁止把 ListTaskIssues 简单改成 primary+secondary 并集。显式拆 primary provenance、parent authority、optional stages、result references；纯读取允许的证据分享不等于管理权。

| 现有文件/位置 | 当前风险 | canonical 接入 / 分片 |
| --- | --- | --- |
| store/issues.go:39、:93 | ListTaskIssues/SaveIssue 只遍历 stage links | legacy helper 不变；P1b1 中央 guard，P1b3 独立 primary/reference resolver |
| views/scheduling.go:41 | 所有链接项门禁领取 | P1b4 primary + ancestor 状态与 own dependencies；secondary 不控制 |
| views/views.go:57 | task detail 归属取 links | P1b3 primary 直接显示，references 单列语义 |
| store/conversation.go:80 | task 绑定递归到所有链接项 | P1b3 只绑定 canonical primary 的来源/授权；证据关系另存 |
| app/task_issues.go:96 | 刷新先清 SharedContext，空 stage 会丢目标 | P1b3 primary 与允许的 ancestor 来源事实，精确冻结 revision/digest |
| store/task_context_blockers.go:13 | source mismatch 只查链接 task | P1b3 primary 所有 task，含无 stage；精确来源/当前 spec |
| app/conversation.go:437 | bindSource(task) 走 links | P1b3 owner 原来源绑定 primary；结果引用不授来源权 |
| app/app.go:327、:407 | logical context/已审核上下文关联取 links | P1b3 primary 身份与有效 reviewed source，不漏无 stage，不审核 secondary |
| app/completion_notice.go:46 | completion review/通知基于 ListTaskIssues | P1b4 唯一 primary review + acceptance/event/epoch；次级引用不收 owning duty |
| app/background_queue.go:16 | labels 和 links 决定 wake group | P1b4 明确 primary target，与已有 durable batch 接合；P5 再扩 child/owner/deadline 路由 |
| app/issues.go:520 | close 活动 task 只看 stages | P1b2 blocker 契约、P1b4 primary 子树任务闭环 |
| app/work_item_choices.go:52 | choice 任务范围依赖 links | P1b2 legacy guard；P5 canonical attention 独立接入 |
| app/followups.go:242 | followup 范围依赖 links | P1b2 legacy guard；P5 才定义 canonical 条件/回答 |
| tools/agent_conversations.go:195 | artifact 范围依赖 links | P1b3 primary 读及显式 bounded evidence share，不授控制/验收 |

还需覆盖不含上述查询字符串、通过 stage 数组/JSON 取得成员的路径：

| 路径 | 必须落实的 canonical 行为 |
| --- | --- |
| views/issues.go:31、:165；contextbuilder/work_items.go:15；app/observation.go:103 | P1b3 primary tasks/执行计数与受限 manager/worker 观察；空 stage 不隐藏活动工作 |
| store/completion_reviews.go:51；app/completion_reviews.go:34 | P1b3 成员枚举、P1b4 创建/评估 review；空 stage 不自动结算，不重跑历史 backfill |
| app/issues.go:409、:448；app/task_issues.go:139；domain/issues.go 的 closure/stage 图规则 | P1b4 证据、受影响合同 disposition、task 依赖图含全部 primary；legacy reducer 保留，canonical 不塞虚假 stage |
| migrations/0043_agent_conversations.sql:102 | P1b3 追加 migration 替换 context_task_change：canonical task 仅从 primary 推进 context，legacy 保留原 stage 行为；不直接修改 0043 |
| migrations/0037_work_item_completion_reviews.sql | 历史回填不改、不重跑、不据此伪造 canonical owner |

Task outcome 只给 primary manager；child outcome/escalation 给 parent 是另一个事实，不是第二 task owner。Owner label 修订不能重写 primary 或取消 owning completion duty。P1b4 接入已有 outbox/background 的 primary 定向身份，P5 完整实现 durable wake/attention/期限合同，不另起定时器。

保持 `app/work_context.go` 的 workerSpec 角色投影及 acceptanceArtifact narrative 过滤。验收得到冻结共享目标/约束/证据及交付，不取得 implementation_context、其他 consumer 的指令或完整 manager 对话。成员缺失应在上游修复，不能用向验收复制全部聊天来补偿。

## 必须的验证与证据

- P1b1：旧 schema 升级零采用/零重写，原主归属不变；canonical current/两快照同 revision；FK/不可变快照；逐保存阶段故障回滚；tree/显式依赖/组合义务环和有效兄弟排序。
- P1b2：精确 owner 默认、拒绝未注册 Agent、revision/epoch/source 冲突；每个 legacy mutation 完整有效请求的 guard/回滚；parent close 与 child create/reparent/reopen 的真实并发事务；每个持久 turn/unknown blocker；空义务 cancel 可终结，活动义务明确 pending/block。
- P1b3：stage-free primary 的 detail/context/snapshot/observation/artifact 可读，越权失败；来源更正/digest fence；secondary reference 不污染目标/授控制；验收角色只获共享事实；追加 trigger 对 canonical/legacy 分别正确，历史行无伪造 backfill。
- P1b4：从正式内部 canonical create/task/spec1/owner 到实际 claim 的无 stage 闭环；pause 不改变正在执行/等答/human_control/unknown 的 run/action/decision/lease/workspace/占用，原 decision 答案沿正式路径继续；暂停期间实现完成保留产物，resume 后原合同独立验收。
- P1b4：primary subtree close、取消正式控制与回执、unknown 保留、不自动 reopen run；secondary paused/closed/改目标不阻 primary claim；A parent B 且 A depends B 的实际领取顺序；非 achieved endpoint 不放行，验证完成才放行，现有 live spec 不被上游变化改写。
- P1b4：passed acceptance 产生唯一 primary completion review/通知，准确 acceptance/event/assignment；label 更正、重启和重复投递不改责任或重复创建；空 stages 不自动 settle。
- 每片运行对应 domain/store/app/views/工具边界回归、integration tag 编译（不调真实模型）、make check、独立 `codex review --base main -c model='"gpt-6.1-sol"' -c model_reasoning_effort='"xhigh"'`。评审另固定准确 base/head，区分叠加 P0/P1a 内容。PR CI 包括 Windows/macOS 交叉编译；实现者逐条判断 findings，较大修订重审。

执行前核对当前代码和迁移号，发现未覆盖的 active consumer 或 blocker 应先补本计划与测试，保持相应路径不启用。只在实际发生后记录代码/检查/review/CI/合并状态，P9 之前部署和 live 验收始终未完成。


## P1b1 实施快照

当前只新增 protocol 内部数据、domain 纯谓词、store 聚合读写及中央 legacy saver guard，追加 0054 migration；不更改既有 0043 trigger，留 P1b3 与对应上下文消费者同批接入。没有 app/api/tools/scheduler 对 canonical saver 或新控制函数的调用，没有 canonical task 创建、公开 schema 注册或 Web 变更。Assignment reducer 只接受已经解析/对账的领域输入，尚无生产 resolver 或交接入口。

已通过的局部验证包括组合义务图与 epoch 围栏规则、真实 SQLite current/两快照一致性、跨保存阶段 SQL 故障回滚、既有身份不得隐式采用、历史 migration 字节及 primary 保留，以及 integration-tag 全包编译。首轮代码与 r2 fixture 增量均获独立 GPT-6.1-Sol xhigh review 通过，无 findings；纯规则测试不代表 owner/app/worker E2E 已实现。


首轮完整 make check 的 Go race 阶段未通过：新增 schema 54 后，既有 upgradeHarness 的 fixture manifest 仍硬编码支持上限 53，升级/维护回归因正确的生产 schema guard 拒绝而失败；其余 Go 包通过，Web 阶段未执行。修复仅让该 fixture 从实际 schema_migrations 读取当前上限，查询失败直接终止测试，保留 Min 27 及 Max++ 不匹配反例；不修改生产校验、历史 migration 或通信协议。定向升级/维护 race 回归已通过（覆盖 TestUpgrade 与 TestConfigurationMaintenance，包括 schema Max++ 反例）；r2 fixture 增量已获独立 review 通过。首轮完整检查仍明确记录为失败。

第二轮 make check 在包含独立基线测试修复的 base `242ff5200c7bda28c70de3c313ffef7a55d64717` 上通过，退出码 0，日志时段约 31 分 23 秒（app race 1846.234 秒），包含 Go 检查/race 与全部 Web 检查、281 个 Web 测试和构建。该轮前后 12 文件 r2 实施快照字节一致，patch SHA-256 为 `8f99a5d2fbb478ce8a28fec78312a9cdff8a4549d4255e5fa99b867d4d2c06c6`；随后仅更新本计划和总台账状态。原实施起点 `d52d093741da41e78dc60effb0493a67058eded5` 与首轮失败记录保留。尚无本批 PR CI、合并、部署或 live 验收结果。

P1b4 还需验证 Issue 与 Task 的混合等待：A 为 B 的 parent 且 A 显式 depends_on B，再加 B task 依赖 A task，会形成真实派发死锁；仅 B task 依赖 A task、A 没有显式 depends_on B 时合法，不能把父 Issue 的 closure 义务误作父 task 的派发门槛。使用不同完成/执行节点及实际未满足事实建图，保留 live run 和冻结 spec；该接入仍属于 P1b4，不扩大本批基础启用范围。
