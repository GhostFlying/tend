# 普通任务规则问题的不可变代次来源

Refs: D168。真实依赖 PR219 的终态问题语义；本批不新增迁移。

## 缺口与边界

任务失败产生的普通 decision 只有 asked control revision；pause、resume 也推进该值，
因此不能由 revision 差推定问题被替代。相反，同一 Task 更新 spec 或明确替换 action
后，旧失败问题仍可能保持 open，误导当前阶段。已有 worker_questions 保存精确来源，
本批为新普通任务规则问题补等价的、可证的来源；不改调查验收策略或恢复原未知执行。

## 实施

- 在 applyDecision 创建规则问题的原事务内，将当时真实 Task/spec 写入不可变
  decision.opened 事件的版本化 origin。只有从真实原 Run/trigger 确认的 action 才记录
  action ID/kind/generation/artifact；不按最新 run 或 display name 反推。
- 不新增表或回填历史；未带此格式的旧问题保持来源 unknown。规划者自由问题不被
  伪装成任务规则问题。保留 asked_revision 与原请求 hash，旧成功/失败回放不改写。
- 复用既有问题收敛事务：只取消具有该来源且 spec 已替代，或原 action 已被明确
  superseded/cancelled、implementation generation/artifact 已改变的问题；对应 Attention
  沿既有责任通路撤回。新代次问题、未归属问题和已经回答的问题保留。
- AnswerDecision 在创建回答来源或执行 effect 之前再核来源适用性。过期输入返回
  stable stale_revision，原 ID 重试回放原失败；既有已接纳答案仍作为事实返回。
- pause/human-control 不是替代证据。既有决策 CLI/只读视图显示真实取消状态和原错误；
  不新增通用 RPC、用户参数或业务控制入口，不改变默认自由问题的含义。

## 验证

真实 App/Store 覆盖失败问题→spec2→新运行/新问题，旧问题和 Attention 精确失效；
原 Run/action 代次不可互换；暂停不失效；旧无来源/自由问题/已接纳答案与原收据保持。
原过期回答在任何新消息/effect 之前拒绝，同 ID 回放稳定。覆盖取消 action 和新 budget
问题不被父 Task blanket 关闭；缺来源/伪 source 格式不作为确定证据。定向 race、CLI
恢复输出、规定独立 review、官方 full 与 CI 是独立门槛，生产继续停机。

问题的 cause 与 applicability 分开冻结：acceptance 的 changes_required 可以产生当前
implementation budget 问题，该问题不属于已结束 acceptance 的 artifact 责任。保留
真实 source_action_id 作为来源，只在规则效应与原 action 角色一致时绑定 applicability
的 action_id；implementation generation / acceptance artifact 从该规则当时的真实
Task 状态冻结。后续 pause 不能误取消新 budget，换代也不能保留旧 retry 问题。

既有 Attention 投影也必须读相同来源适用性：新有源问题在 pause 后仍可为当前，不能
单靠 AskedRevision 差误判。已精确退休的旧代次问题退出 open 列表，但原 Attention、
decision 和事件在 includeArchived 历史/按原 ID 读取中完整保留；只有当前问题可行动。
旧无来源问题不据新来源回填，保留其原历史/版本提示边界。
