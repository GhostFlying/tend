# Canonical Issue 上下文投影责任隔离

Refs: D122, D128, D136, D142。

## 问题与边界

`issues_context_read` / `tend issue context` 当前复制历史 work-item 上下文的状态。历史 `logical_turn_contexts` 保留一个全局处理回合接触过的全部上下文；其其他 Issue 消息的分类义务、main 的 recovering 状态以及未初始化的全局 baseline 因此被显示为当前 canonical Issue 的派发前置。正式 `issues_create_task` 则使用当前 Issue/assignment/source CAS 和冻结的 EffectiveContext，不调用这个历史全局 ready gate。投影不能把一次未执行的创建描述为被服务器拒绝。

仅修 canonical 只读投影和其说明。历史 `IssueContext`、`WorkItemContextReady`、`ContextBlockers` 及真实调度、来源授权、native/logical 收敛全部保持不变。不写 baseline，不结清 recovering/unknown，不增加读取跨 Issue 原文的权限，不更改 runner wire 或持久格式。

## 可见语义

- `prepared` 继续表示真实历史 baseline 是否存在；false 不等于 canonical task 不可创建。
- `sync_status` 表示本项已准入来源更正及有效事实的复核状态；不要求创建历史 baseline。
- `coordination_state` 只表示本项执行的实际未完成逻辑状态。执行归属优先使用持久 hosted Issue identity；没有此 identity 的历史执行仅以原请求的 `issue:<id>` conversation 归属，不能从共享上下文或来源关联推断执行归属。
- `dispatch_blockers` / `dispatch_ready` 是本项上下文复核、未决协调证据及既有冻结 task agreement 的诊断。它们不是 `issues_create_task` 的 admission 结果，也不是当前 host、依赖、权限或 worker readiness 的证明；创建必须由原正式命令校验。
- 本项已准入来源更正及 own/global/project 的有效事实复核不消失；未交付的排队 owner 输入不冒充当前回合义务。祖先 EffectiveContext、继承语义与既有 TaskContextBlockers 原样保留。

## 实施与验证

1. 建立仅测试数据库的稳定 RED：其他 Issue 的中断来源、全局 recovering 和共享上下文不能污染当前 canonical 投影；保留原历史 projection 与 ledger 字节。
2. canonical wrapper 使用独立只读聚合，保留本项真正的 recovering/native pending、已准入 source correction、global/project fact review 以及 task agreement；legacy gate 不动。
3. 覆盖 shared main source 的本项义务、原 hosted identity 优先于 conversation、缺失 baseline、排队输入、冻结祖先 binding。补工具及 CLI 文档明确诊断与正式创建的区别。
4. 定向 race 先通过并冻结候选；独立 review、本地完整检查与 PR CI 可并行，全部覆盖最终 source 后按 rebase merge 收口。生产操作由唯一操作者负责，不在此修复中读取生产。
