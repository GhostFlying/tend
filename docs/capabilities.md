# 声明式 Capability

Capability 是用户显式配置的业务标签，与 Category、runtime、model、effort 和工具实际可用性分开。
`image_gen@1` 仅表示声明标签；它不证明工具暴露、生成成功或费用。匹配不探测工具或启动模型。

## 定义与配置

定义不可变历史由 owner request_id 去重及 revision CAS 追加。新增 ID 从 revision 0 开始；
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
tend task list --capability image_gen --capability-role accept --json
```

`eligible` 使用真实 scheduler 的角色、pin、backend、依赖及容量边界；`pending` 表示可恢复
的普通 readiness/容量等待；`unavailable` 提供缺失声明、revision 不匹配或冻结选择不满足的原因。
只读查询不预留资源。首次分配所需的 reviewer 仍为原调度前提；waiver 不引入额外 reviewer 要求。
过期等待由现有 sweep 记录一次每 Task/spec/role 的通知，不新建独立定时器或模型调用。

所有分页返回 `next_after` 与 `as_of`，续页传 `--after` 原 cursor 和原筛选条件。
定义历史使用固定水位；Agent/候选可变事实改变后旧 cursor 明确拒绝，重新开始分页。
候选排序包含完整 pin，保留 current 与 retained 的不同 snapshot/revision。

HTTP 对应 `/api/v1/capabilities`、`/{id}`、`/{id}/revisions`，
`/api/v1/agent-capabilities`、`/{environment}/{agent}`、`/{environment}/{agent}/revisions`，
以及 `/api/v1/tasks/{id}/capability-match`。写操作为 owner `capabilities.put`。
主协调/Issue manager 的只读工具使用同一应用查询及原 Issue 权限边界。

写入前用 `--dry-run` 保存完整 Envelope。未知结果先用 `tend request show ORIGINAL_ID` 读取，
再通过 `tend request replay --file ORIGINAL_ENVELOPE` 对账原 request/CAS/payload；
不得为未知请求新造 ID 或提升 revision 重复动作。

## 存储与交付

server migration 98 与 journal migration 19 只追加，原迁移不改。
旧请求、unknown、quarantine、retained resume、验收文件及 native 停止证明保留。
非 main 的冲突迁移历史在写入新 SQL 前拒绝，不自动修复或冒充当前部署。
代码检查、独立验收、merge/deploy 和线上功能验证分别记录。

Refs: D183；[实施计划](plans/capability-current-main.md)。
