# P1b3/b4：primary 读取、上下文和 owner task 执行闭环

状态：已正式纳入 owner task 执行闭环，与 b3 读取/context 及 P1c 公开 owner API/CLI 同批交付。r1 独立预审两项缺陷已在 r2 修复并通过红绿回归及独立复核；正式组合定向 race/vet、schema/types 与真实 CLI 路径检查已通过，正式 r1 CLI 两项 P2 已修复，r1 完整检查主动未完成；r2 已正式应用，定向 race/vet、独立增量复核及完整 make check 均通过；CI/合并及发布未完成。Refs: D121。固定 base 与本片证据见 [组合计划](issue-owner-delivery.md)。

## 交付范围和边界

采用已有 task_work_item_owners 作为唯一 primary，不新建成员关系表。canonical task 没有 stage 也能出现在 detail、context、来源修订、调度和完成核对中；其他成果链接只提供证据，不转移 task 控制、来源责任或完成核对。legacy 的多阶段共享责任保持原语义。显式 stage 保留原 ParentID/DependsOn 准入约束，仍是同一负责人内部的轻量步骤，不获得独立负责人、运行时或交接身份。

新增 `issues.create_task`（P1c 注册 owner API/CLI），只接受 authenticated owner，在同一 App 请求事务检查 Issue revision/assignment epoch、保存冻结 spec、primary、可选 stage、action 和 Issue 两份不可变历史。owner 明确提交 spec 就是本次执行授权，允许在 baseline0、main 忙或 Issue 尚有待澄清来源时先创建调查 task。来源引用作为出处记录，不附着主会话执行责任；服务端冻结当前 Issue/祖先定义与已选共享事实。单用户 owner 与 owner 控制的本机配置、数据库是可信输入，不增加管理员篡改证明或 OS 沙箱。

复用现有 Claim、run/lease、worker role、SubmitArtifact、SubmitAcceptance、workspace host pin 和正式验收模型能力校验。当前 run 继续其冻结 spec/generation/artifact 合同，结果回写不受 canonical 缺少全局 baseline 影响。新的实现或验收领取检查 Issue/祖先状态、本项明确完成依赖、task 先决条件、可选 stage 与真正来源 digest；标题、排序、任意 Issue revision 不阻挡冻结合同。reparent 改变祖先集合时，新领取需要 owner 显式更新 task spec 重冻继承上下文。更新 spec 清除旧 artifact，复用原代次规则，不能让新 spec 借旧实现产物直接验收。实现推理继续只进 implement，独立 accept 只接收共享事实与必要交付物。

## 生命周期和完成核对

Issue pause 只停止新领取，不改变已开始 task/run/control/lease/workspace。cancel 持久取消意图并复用现有 task Cancel 停止本树 primary；finish_cancel 必须等实际 run/unknown/lease/workspace/问题和既有来源、子项、承诺分别收束。实际停止通过既有 runner release/hello 对账及 workspace release，不能因 cancel 命令成功就伪造停止。完成或已取消 primary 可正常关闭、重开、再规划，不保留原“存在 primary 即 unsupported”的永久封锁。

task 独立验收通过后，只为其 primary 持久一份 completion review。owner 用 `issues.review_completions` 或 close/finish_cancel 内联逐项核对 exact pending event，并保存理由、Issue revision 和请求去重回执。失败 endpoint 事务回滚这些核对。通知仍是原事件/outbox 内的事实；canonical 不启动旧 background runtime，owner 可见待核对。EnsureCompletionReviews 使用 primary 补偿枚举，legacy secondary 成果引用不领取该核对。

组合等待图区分：Issue 完成等待 child terminal、primary task 终结和自己的显式完成依赖；task 派发只等待自身 Issue 的明确依赖、task prerequisites 和明确关联 stage。A parent B 且 A depends B 时，B task 仍能执行；若 B task 又依赖 A task，则拒绝构成真实等待环的事务。只有 B task 依赖 A task、A 无显式 depends B 时合法。历史 task_dependencies 暂无写 API，相关测试明确使用已有调度事实 fixture，不声称新入口。

## 未启用项

无 owner HTTP、tools、CLI、manager 或 UI 注册，主 agent 仍无 canonical 执行/合同写入口。既有 main/legacy task 的行为和正式 worker 事实、owner 回答路径保留。旧 backgroundThreadConfig 会删除 Isolation 并授予 workspace-write，不能当新 issue manager 使用；P4 必须替换真实运行配置及角色工具面才能启用。Agent/category 选择、固定运行时快照和恢复由 P2/P3 接入，不在本片冒称已完成。P1c 才开放产品 API/CLI，P7 继续逐页面设计审批。

b3 候选迁移号按正式最新 schema 重定，只追加本批未提交的新迁移，不修改任何历史迁移或预留空号。本片 b4 不增加迁移。

## 验证

候选用真实临时 App 链测试 owner create → implement → artifact → 独立 accept → primary completion review → owner 核对 → close/reopen；覆盖同请求重放、stale revision/epoch 和事务回滚，source 修订 → 旧 run 结果保留 → 阻止新领取 → owner spec 2 → 新实现及独立验收，pause/waiting worker/原问题回答的身份与占用保持，以及 unknown 对账后取消收尾。显式 stage、无 stage、混合依赖正反例和 reparent 上下文刷新分别验证。旧 completion notice、结果 context fence、task stage 依赖与验收角色投影作为定向回归。候选验证后冻结实际 patch/文件 hash；正式组合还需规定独立 CLI review、唯一 make check、CI、线性合并及最终整体部署 E2E。

候选定向结果：新增 owner/primary/context 链普通测试通过（app 2.544s），选定原有 completion 原子/回滚、source fence、stage 等待图与验收隔离回归通过（app 1.234s）。最终选定新增链、既有 canonical lifecycle/terminal share 与上述 legacy 回归的 race 通过（app 157.448s、domain 1.036s），app/domain/views/store 四包 vet 通过；尚未进行本组合正式独立 review 或完整 make check。日志和精确固定基准由私有协调证据保存，正式提交需更新总台账。

## r1 独立预审与 r2 修复

r1 的定向检查通过不代表行为已获独立验收。实际预审发现：无 source 引用的 review_completions 落入 no-op shortcut，核对记录指向 current+1 却没有对应 Issue 修订、两份历史及事件；父项 Intent/Background 改变时只校验祖先集合和来源 digest，遗漏冻结的继承定义。两项均为正常 owner 操作可达缺陷。

r2 仅将完成核对排除 no-op，保持当前修订、两份不可变历史、事件、reviewed_revision 同事务一致，原 request 重放不重复写。新领取另用已有冻结祖先 revision 的不可变计划与当前 Intent/Background 做语义比较，不引入全 revision 门槛或新快照账本；标题等元数据放行，primary 的显式 continue_existing 保持，active run 结果继续原合同。真实红绿回归同时验证 owner 刷新后必须完成 spec2 新实现与独立验收。精确候选测试、delta/whole patch 与文件 hash 由冻结 manifest 保存，正式组合验收另行更新。
