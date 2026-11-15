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
