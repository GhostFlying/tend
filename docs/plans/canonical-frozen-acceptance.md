# Canonical 冻结产物验收准入

基线：`2fe21baa6eb8014b01ebb565a04d466ed5df1acd`。Refs: D123、D136。

## 问题与边界

Canonical task 已按冻结 spec 提交产物后，祖先 Issue 新增独立范围或来源事实会让原 acceptance 一直被 `ContextPending` 拦截。来源复核不改变冻结 spec；刷新 spec 则废弃当前产物并排新实现，因此不能保留原产物完成原合同的独立验收。

本次只修正此验收准入，不改变交付确认、Issue 重开、卡片统计或生产数据。验收对象始终是原 spec 和当前 artifact；新 Issue 义务不被旧验收结清。实现领取仍须核对当前继承语义和来源。

## 实施

1. 在 canonical admission 中保留负责人、reviewer、assignment、Issue 生命周期、依赖、task control/spec/action 和当前 artifact 的既有围栏；仅让独立验收不再用当前 Issue 语义内容替换冻结输入。
2. 用真实 app/store 调度链回归原 accept action 可领取且冻结输入未变；覆盖祖先 plan 与 sourced facts 的变化，确认新增责任仍保留。
3. 覆盖实现领取继续被过期输入阻挡，以及 owner/reviewer 变更、暂停/取消、spec 更新、旧 artifact/spec 的既有拒绝行为。调整直接表达旧验收阻塞语义的测试，避免删除有效对照。
4. 在 D123/D136 设计约定中明确冻结验收与新范围的边界；不新增 API、迁移或 wire 字段。

## 验证与交付

先运行能重现原阻塞的针对性测试并记录红绿结果，再运行相关 app/views/scheduler/domain 回归及仓库要求的完整 `make check`。按仓库要求独立 review，处理实际缺陷后准备可审阅 PR，记录 head/base 和验证边界。合入、部署及原生产 action 恢复均等待明确批准，不在本实现中执行。

主要风险是放松验收准入时误绕过控制或把新目标视为已验收；测试必须通过原 artifact/spec 的身份、旧输入和 Issue 剩余义务进行对照。
