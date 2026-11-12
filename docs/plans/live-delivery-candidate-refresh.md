# 同责任 Task 的交付候选刷新

Refs: D219、D222。原业务目标是负责人沿同一 Task 完成 PR 合入、部署和最终报告。
当前初次 live prepare/bind 已支持自执行，但候选刷新要求整个执行 Task terminal，
让持续负责人的正常重基无法继续。等待只停 Run，不能伪造 Task 完成。

## 范围与行为

1. 沿用 issues.update_delivery 的 prepare/bind，按 source Task 与 execution Task 相同
   的持久业务关系识别同责任刷新，不按 Agent、主机或名义角色设能力白名单。
   保留当前 Issue 归属、assignment epoch、Issue/Task CAS、category/capability、
   实际租约和用户授权范围。另一独立执行 Task 保持原 terminal/停止/资源规则。
2. 刷新前核对正式 provider 仍 open、原绑定及同责任实际升级、可能冲突的 unknown。
   已合入、已启动升级或结果不明时沿原身份对账；不重放，不以空投影代表零效果。
   同责任正常活跃的当前执行不等于另一未停交付进程；不机械要求当前工具调用的
   原生账本全清零，也不伪释放 slot。辅助旧 ACK 只影响依赖其事实的操作。
3. 原子保留旧执行候选/授权/真实来源的历史、撤销旧当前绑定，再沿原 prepare/bind
   审阅、检查与有效范围授权。任一校验失败回滚原绑定和历史；同候选重复调用不
   重复归档。旧 manual/defer 不自动批准，新候选不复制旧 green。
4. 历史保留旧 executing 状态，始终不可作为新执行或完成结算权限；不写假 complete。
   新旧 session 都能从原 Task 的历史索引读取。Issue 不恢复候选缓存。

## 历史、入口与兼容

- 追加迁移，允许现 execution_history 的 artifact_id/run_id 真实缺省，支持无产物的
  live 绑定及尚未执行的 Task；不修改0118，不伪造 Run 或产物。已有真实产物继续
  使用其真实 producing Run。无产物时保存实际刷新 Run（或该 Task 实际最近 Run）
  作为快照来源；真正尚无 Run 时留空、generation=0，明确不是已完成成果。
- 原 Run+duty_revision 选择器不变；沿现材料查询扩展 Task+duty_revision 选择历史，
  解决尚未执行绑定的读取。当前 Task 查询不带 revision 仍返回当前权限；历史选择
  不授予执行/结算权限。同步 CLI 帮助、能力目录及必要 schema 说明，不另建接口。
- 同链 checkDeliveryUpgradeStart 校验真实当前已授权执行及有效租约，支持负责人
  从原 Task 交付；不以 implement 名称代替实际身份/能力。保持旧任务、取消/过期
  租约、错误候选/目标/授权与并发升级保护。
- 此改动扩展既有 owner 入口并保留原输入，runner 通信4/3不变；逐项核对没有收紧
  旧有效请求或新增必填 wire 字段。历史迁移和读取兼容单独验证。

## 验证与交付

- 真实旧源码拒绝的 self-bound rebase/target 变化可在同 Task 刷新；覆盖有/无产物、
  当前 Run 活跃、yield 后继续及未执行绑定，不制造 Task/Run/停止/资源释放事实。
- 重复刷新、多次刷新、旧授权失效、旧历史可读且不可执行/结算；事务失败不半写。
  stale Issue/epoch/Task、撤销/越范围、缺新审阅/检查真实拒绝。
- 已合入、原升级运行/未知或相关外部 unknown 不可刷新；独立 Task 的原规则不退化。
  负责人能以真实授权及租约进入受控升级，错误身份/失效租约/并发真实拒绝。
- 原历史迁移和按 Run 查询不变；Task/revision 及无产物读取覆盖 owner API、CLI、
  Agent 共用读取，跨 Issue 对象边界仍有效。
- PR310 原完整检查已在冻结280上自然通过；原独立审阅及 CI 结果只覆盖该源码。
  追加原子修复后，同 reviewer 核对增量，最终完整本地检查和 exact-head CI 覆盖
  组合源码。一次完整 format2 受控维护升级，先对账原 PR/业务升级，保留未知。
  实际进程、配置及 ACK 证明后，通过 Tend chat 让原负责人继续；不代做业务收尾。
