# 声明式 Capability

Capability 是用户显式配置的业务标签，与 Category、runtime、model、effort 和工具实际可用性分开。
`image_gen@1` 仅表示声明标签；它不证明工具暴露、生成成功或费用。匹配不探测工具或启动模型。

## 定义与配置

定义不可变历史依据真实 capability ID、不可变输入及 revision CAS 追加；request_id 仅用于追踪。新增 ID 从 revision 0 开始；
`active` 可被新声明或新 Task 引用，`retired` 保留历史读取和原有冻结引用。

```sh
tend capability list --state all --search image --limit 50 --json
tend capability show image_gen --definition-revision 1 --json
tend capability history image_gen --state all --limit 50 --json
tend capability put render --revision 0 --data '{"title":"Render","description":"Configured rendering label","state":"active","contract_version":1,"verification_policy":"declaration_v1"}' --dry-run
```

配置沿现有 Agent 注册路径提交，配置顶层设置 `capability_version: 1`，在对应 Agent 上
增加 `capabilities: [{"capability_id":"image_gen","definition_revision":1}]`。
变更声明必须递增该 Agent 的 revision；删除再添加同一身份不能复用旧 revision 改声明。
顶层 extension 缺省和空声明保留旧配置/snapshot 的规范化 hash。提交仍使用现有
`agents local validate`、`agents local reload`、`agents local register` 流程和配置 CAS，
只读状态及原请求对账保持不变。不同 runtime/model 的名称不会自动赋予声明。

## 双角色需求与冻结

Task 原有完整 JSON 中的 `agent_selection.implementation.required_capabilities` 与
`agent_selection.acceptance.required_capabilities` 分别保存 exact ID/revision 列表。
两个角色独立；仅 reviewer 需要的标签不会加给 implementer。无 required 时沿用原调度。
每一角色新增引用检查当前 active 定义，保留的 exact reference 不因定义后来退役而失效。
跨角色移动、增加或更换 definition revision 是新的引用。

claim 在同一事务中保存原 Run、lease、具名 execution pin 和声明匹配绑定。
恢复读取原 snapshot 的声明，保留原 Agent/runtime/model/effort/backend；不能切换 provider。
runner 在重置原停止证明及增加 attempt 前验证绑定，继续调用普通 Workers.Start。
缺少可选协议协商的旧 runner 继续领取无 required 的任务；有 required 的任务等待支持
`executor-capability-declarations-v1` 及 journal 19 的原执行器。

## 查询与调度解释

```sh
tend agents capabilities list --category implementer --capability image_gen --state eligible --limit 50 --json
tend agents capabilities show example-host example-agent --json
tend agents capabilities revisions example-host example-agent --agent-revision 2 --limit 50 --json
tend task capability-explain TASK --role implement --spec-version 1 --limit 50 --json
tend task capability-explain TASK --role accept --json
tend task list --all --capability image_gen --capability-role accept --json
```

无 Task 要求的 Agent 目录分别表达三个事实：

- `declaration_state=empty/declared` 和 `declared`：接受配置是否声明额外标签，及精确引用。
  空声明不妨碍执行没有额外 capability 要求的普通任务，不需要补造声明。
- `executor_readiness.state=ready/unavailable/unknown`：普通执行器是否有匹配当前接受
  配置的观察；`reason`、`explanation`、`observed_at_ms`（最近 runner 心跳，0 表示
  未观察）、`checked_at_ms` 解释来源及时间。未观察到匹配执行器是 unknown；明确
  adapter 不可用或心跳过期是 unavailable；明确禁用的接受配置返回 unavailable，
  如 binding_disabled，需要先启用精确绑定，再观察执行器。恢复后可重新显示 ready。
- 目录 `current.required` 为空，`current.state=eligible` 仅表示观察到普通执行器 ready，
  其他情况为 pending；`--state` 筛选此汇总，`--capability` 仅筛声明。该目录未指定
  Task，不承诺任意任务的容量、租约、workspace、host 或共享资源准入，也不验证
  capability 对应的原生工具。

具体 Task explanation 读取实际 spec/role 冻结的 required。候选 `current` 是精确声明
匹配，`scheduling_reason` 单独解释普通准入限制；顶层 `state` 使用真实 scheduler
的角色、原 pin、backend、依赖及容量边界。`pending` 是可恢复的 readiness/容量等待；
`unavailable` 表示没有符合冻结要求的候选。空要求仍保留普通调度准入，不无条件
返回 eligible；没有具名 inventory 的普通 legacy Task 也按原 scheduler 解释临时容量
等待为 pending，永久要求不符仍为 unavailable。只读查询不预留资源；独立审阅需求
继续由原 Task 合同决定。

Agent list/show 和 Task explanation 候选的 `declared` 保留原配置的精确引用；
`definition_statuses` 另列该引用对应定义的当前 revision/state 及说明。例如 Agent 声明
`image_gen@1`，定义当前为 revision 2 / `retired` 时，两项同时显示，并明确新引用被拒绝。
具体 Task 的 `current.state=eligible` 是既有冻结需求的声明匹配结果，不表示可以向新声明或新任务添加退役引用。
原始 Agent revisions 历史仍返回当时接受的配置，不用当前定义状态改写旧声明或 pin。
过期等待由现有 sweep 记录一次每 Task/spec/role 的通知，不新建独立定时器或模型调用。

所有分页返回 `next_after` 与 `as_of`，续页传 `--after` 原 cursor 和原筛选条件。
定义历史使用固定水位；Agent/候选可变事实改变后旧 cursor 明确拒绝，重新开始分页。
候选排序包含完整 pin，保留 current 与 retained 的不同 snapshot/revision。

HTTP 对应 `/api/v1/capabilities`、`/{id}`、`/{id}/revisions`，
`/api/v1/agent-capabilities`、`/{environment}/{agent}`、`/{environment}/{agent}/revisions`，
以及 `/api/v1/tasks/{id}/capability-match`。写操作为 owner `capabilities.put`。
主协调/Issue manager 的只读工具使用同一应用查询及原 Issue 权限边界。

写入前用 `--dry-run` 保存完整 Envelope，其中包含真实业务对象 ID、消息/子任务/规则 ID、输入与原 CAS。
未知结果先通过 `issue show`、`task show`、`message original`、`run maintenance-reconciliation` 等具体命令读取业务事实；
保留未知状态，不自动重复外部操作。需要显式提交保存的输入时使用 `tend operation submit --file ORIGINAL_ENVELOPE`；
该辅助命令只提交一次，不查询请求账本、不补造缺失业务 ID、不刷新 revision，也不替代具体业务命令。

## 存储与交付

server migration 98 与 journal migration 19 只追加，原迁移不改。
旧请求、unknown、quarantine、retained resume、验收文件及 native 停止证明保留。
非 main 的冲突迁移历史在写入新 SQL 前拒绝，不自动修复或冒充当前部署。
代码检查、独立验收、merge/deploy 和线上功能验证分别记录。

Refs: D183；[实施计划](plans/capability-current-main.md)。
