# 部署前阶段结果与同 Task 候选接续

## 故障起点与目标

正常业务链已经授权并绑定一个候选，PR 真实合入后 main 检查失败，尚未启动升级。同一 Task 产生必要的新候选与检查报告。现有 worker 结果登记却无条件要求 upgrade 引用，候选刷新又仅允许旧 PR open，导致无法保存可独立验收的阶段结果，也无法继续原交付责任。

本次只恢复这条正常链，不生成历史产物、不结束原交付、不迁移旧记录、不提供强制解绑。旧 Run 的拒绝和已合入事实保留。

## 最小方案

1. 报告新增可选的 typed `predeployment` 候选身份（delivery_id/head/base）。它明确表示部署前阶段，必须匹配当前 Issue 的真实交付观察及提交仓库状态；沿当前 Run/Task/授权/版本校验登记真实新 artifact。只有尚无升级、无未知外部操作的原 self-bound 部署责任可使用，不允许同时声明升级完成。
2. 阶段 artifact 可以接受原冻结的独立验收；它不完成 deployment duty，不充当最终安装证据。同目标 Task 完成阶段后可正式 continue，保留原 binding，实际后续 Run 继续执行。
3. 候选 prepare 在现有 CAS/epoch/授权/独立审阅/检查校验下，允许旧 PR 已确定 merged、target 包含该 merge、尚无受控升级、无 unknown 的 self-bound 责任接续一个以观测当前 main 为 base/target 的新 PR。通过已有历史归档保留旧冻结 candidate/duty，不原地改写。
4. 最终部署结算明确排除 predeployment artifact；仍需要真实唯一 upgrade、最终结果和独立验收、安装/健康/normal ACK。缺失或伪造 upgrade、旧 Run、候选不匹配、越权和 unknown 继续拒绝。

## 调用路径与验证

worker submit_result -> runs.submit_artifact -> typed 阶段验证/真实 artifact -> 原独立 acceptance -> tasks.continue -> issues.update_delivery prepare/bind -> 最终真实升级结果 -> 独立 acceptance -> complete。Issue owner 自执行仍只登记本人的实际 Run，不冒充 worker。

新增业务回归从真实获准/bound 和真实 merge 观察开始，覆盖 main 检查失败后新候选阶段登记、独立验收、同 Task 接续与最终结算防线；原 unknown/版本/权限/升级保护回归保留。根据实际协议 delta 同步 schema/CLI/tool 输入和文档；动态 submit_result 同时补齐 ResultReport 已有的 reviewed_artifact_id/verdict，确保真实 owner 产物能沿原审阅责任报告独立结论；不增加依赖、无 DB 手改或通用迁移。先定向 race，再必要 make check 和完整 PR CI；Maple 持续责任独立审阅真实增量。现有诊断候选的原件及验证直接引用，不重复其已完成诊断。

## 原 Maple 第二轮 R3–R6 修正计划

829 候选的独立审阅已实际复现三个缺口，另确认主机集合比较错误；全绿 CI 未覆盖这些边界。旧全量本地检查已取消并确认退出，保留部分检查、取消原因和缺失最终 receipt 的事实；829 及其同树 CI 制品不安装。

- R3：独立阶段验收令 Task completed，但 deployment duty 仍 executing。该状态的 prepare 必须拒绝并要求先 continue，不能走旧终态替换分支清空 binding 或缩小部署范围；原最终已结算执行的替换能力保留。
- R4：只在原 self-bound、已独立验收的阶段、当前授权/代次、无 upgrade/unknown、旧 merge 与当前 target lineage 已确定的情况下，允许领取准备新候选的后续 Run。该例外仅放在调度准入；安装仍走原 deliveryExecutionAuthorized / deliveryDeploymentSource，不给旧候选安装更新 main 的权限。回归同时证明可 claim 与旧安装权限仍被拒，覆盖 unknown/未验收/升级阻断。
- R5：阶段及最终报告可以记录真实但未变化的仓库状态。runner 只在报告明确为 typed stage 或有唯一 upgrade 引用、Collect 返回 ErrNoChanges 时读取 Snapshot，保留真实 Changed=false；服务端 typed stage 仍完整验证。最终未变化的 commit 结果须精确复用同 Task 已独立验收阶段的全部仓库身份，并匹配当前冻结候选/授权与真实唯一 upgrade，不泛化放宽普通 commit Task。新增真实 gitws Prepare/Collect/Snapshot 的阶段→continue→最终报告→验收/结算链及 runner 补检，覆盖无 grant、未验收、不同 SHA/快照、伪造 upgrade 和普通任务拒绝。
- R6：完整 host 范围按集合比较，副本排序或集合验证均可；不改变输入数组，重复、缺少、额外主机仍拒绝。

先按原审阅 probe 的实际触发状态编写回归、修正并定向 race 验证，再固定实际新候选、必要完整检查与精确 CI，并在同 Maple Task 按 delta 复查。保留旧轮 verdict/失败，不新建 Task/Issue/worktree，不安装旧候选，也不为通过检查增加无业务必要的源码提交或伪造 Changed。

## 原 Maple 第三轮 R7–R9 修正计划

cfda 的原 R3–R6 触发路径已经修正，独立审阅又以三条 race 探针证实了负面验收回路和实际安装门槛的缺口。本轮在编辑前补充方案；cfda full 已取消并确认进程退出，996 个 pass 事件与 child -15/1753.497秒的正常取消 receipt 保留，不算全量通过。cfda/c6d 制品不安装。

1. R9：在实际 CheckUpgradeStart 与 App StartUpgrade 共用的最终验证边界，重新要求当前 duty 所贡献的 whole-PR 审阅和精确 PR head 的必要检查；保留原无 PR policy 的历史手动合同。准备阶段有真实未通过验收，或阶段候选不等于当前冻结候选时，不能借准备 Run 安装。许可、当前 lease/Issue epoch、原 Source、完整 targets 与最终写事务检查保持。回归直接调用实际 App StartUpgrade，而不仅检查 preflight；审阅 unlink、CI失败/缺失/unknown、负面 stage 均无批次，合法准备仍可用。
2. R7：current_artifact_id 是生命周期指针，不能作为阶段存在的唯一证据。当前指针为空时，从本 Task/spec 的真实验收记录读取其原 immutable artifact；验证原 Task/Run/action/spec 和 typed stage，不修改历史或恢复数据库指针。真实 changes_required 等负面验收只允许修正准备，不当作 passed；当前调度、预算、owner contract/grant、no-upgrade/no-unknown及已确定 merge/lineage保护保持。
3. R8：最终报告按当前冻结 candidate 的 delivery/head/base 与当前 spec 读取真实历史阶段及其独立验收。只接受精确相同候选的最近阶段，未通过阶段不回退到更老的 passed；不借其他 Task/spec/candidate。原唯一 upgrade、frozen授权、全部仓库 base/commit/无snapshot以及最终独立验收保持。已安装后的报告修正直接重用真实已验收的源码来源，不新增 predeployment、源码提交或伪造 Changed。
4. 同时覆盖首次最终报告、负面验收后重报、重复负面修正、阶段负面后 main drift、明确未验收不能安装、原 accepted history 不被改写。provider/installer仍明确是外部边界替身；保留实际命令路径及旧失败 probe 原件。无新接口、DB迁移、强制解绑或旧Run补造。按实际 delta 定向 race及既有授权/安装回归，固定新提交后交同一 Maple 持续责任复查；完整门槛与原授权不变。
