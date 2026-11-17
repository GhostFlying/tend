# 普通 Issue 取消的已结清快速路径

## 业务起点与终点

普通 `issues.cancel` 经 owner HTTP/CLI、owner tools 或当前 Issue manager 的
hosted tool 进入 `CanonicalOwnerIssueCommand`，在 revision/assignment epoch 校验后
调用 `applyCanonicalOwnerLifecycle`。当前总是保存 cancelling；外部取消再由
`App.recordEvents` 生成 issue.cancelling 背景消息，等待中的 manager 因而再次领取。
看板/CLI `issues.set_status(cancelled)` 已查询 endpoint facts 并在一次事务内结清。
目标是普通取消在同样无未结责任时直接保存 cancelled，不产生额外取消消息或模型领取。

## 实施路径

1. 复用 `canonicalOwnerEndpointFacts`、领域 Begin/FinishIssueCancellation 以及
   现有保存、终态事件和 manager retiring/receipt 机制；提取共用结清转换。
2. 普通 cancel 保持现有 primary/子树任务取消控制，然后读取事务内真实 facts。
   无 blocker 时一次 revision 保存 cancelled；否则保存 cancelling 并保留必要通知。
   board 入口仍拒绝未结义务，不替用户隐式处置承诺。
3. 保留子责任、delivery duty、blocking attention、承诺、primary task/run、
   workspace、unknown 和待消费 receipt 的现有 gate。manager 的已知活动 Run/lease
   沿既有 retiring 路径等待实际停止；Issue 终态不伪造进程停止，不增全 manager 退出门槛。
4. 同步 CLI 与工具帮助、设计说明。整理旧的空责任 cancel→finish_cancel 用例，
   已终态的迟到来源使用现有 review_sources；仍有责任的场景继续真实 finish_cancel。

## 验证与恢复

从普通 cancel 与 set_status 的真实 App/API/hosted producer 进入，验证单 revision、
无 cancelling source、无新 coordinate action/Run，旧 epoch/revision 拒绝且不写入。
等待 manager 用真实 native close 与 runner finish 驱动；未结承诺保留唤醒，结清
manager 不再领取。并复用/扩展真实 primary 取消、子责任、delivery、业务答复、unknown、
lease/workspace/receipt 回归，明确 fixture 只预置隔离所需边界。
故障时读取原 Issue/task/run/receipt；cancelling 可继续取消下属、回答原问题和
finish_cancel，unknown 沿原身份对账，cancelled 仅观察原进程收尾或 review_sources。

按 diff 运行定向新鲜回归和 make check 受影响验证，独立 codex review
(gpt-6.1-sol/xhigh)，最终候选/基线所需 CI 后按保护规则 rebase 合入。
PR 使用 tend 来源标签。提供 merge 和生产验收步骤给统一部署责任，本 Issue 不部署。
不改变 wire/schema，不新增状态机或账本。
