# 执行能力的管理与恢复

Capability 表示一个具体执行入口的有版本能力。category 只用于委托角色；模型或 CLI 名称、已登记状态、adapter Available、工具 schema 暴露都不是调用成功证明。

## 定义与声明

```sh
tend capability list --state all --search image --limit 50 --json
tend capability show image_gen --definition-revision 1 --json
tend capability history image_gen --limit 50 --json
tend capability put render --revision 0 --data '{"title":"Render","description":"Exact executor tool inventory","state":"active","contract_version":1,"verification_policy":"tool_inventory_v1","tool_name":"render_tool"}' --dry-run > original.json
tend request replay --file original.json
tend request show "$REQUEST_ID" --json
```

put 使用完整 JSON，创建 revision=0，后续更新/退休使用原 revision。重复动作使用原 request_id/Envelope；stale 不自动刷新 CAS，同 ID 改 payload 冲突。查询定义的 revision 参数在 CLI 中叫 `--definition-revision`，区分写操作的 CAS `--revision`。

本机配置增加 `"capability_version":1`，在具体 Agent 内增加 `"capabilities":[{"capability_id":"image_gen","definition_revision":1}]`。使用已有 `agents local validate/reload/register/registration/reconcile` 流程，按既有 Agent/runtime 修订规则更新；validate 不生效，register 不确认工具。未提供该扩展的旧快照和 hash 原样保留。声明不能被 category 或 skills 自动填入。

## 证据与查询

```sh
tend agents capabilities list --environment "$ENVIRONMENT" --category implementation --capability image_gen --state pending --limit 50 --json
tend agents capabilities show "$ENVIRONMENT" "$AGENT" --json
tend agents capabilities history "$ENVIRONMENT" "$AGENT" --kind tool_inventory --limit 50 --json
tend agents capabilities evidence "$EVIDENCE_ID" --json
```

list 支持 environment、agent（需 environment）、runtime、category、purpose、capability、state 和 after/limit。当前 state 是 eligible/pending/unavailable；原证据 state 则是 unverified/confirmed/unavailable，历史查询支持 kind/state/capability。定义和证据历史使用固定水位游标；Agent 当前目录和 Task 候选变化时返回 invalid_request，重新开始第一页。as_of 是查询水位/checked-at，不是新的实证时间。

证据保留完整 pin、incarnation、来源版本及 SHA、scope、observed/expires、原 request 和 kind。expired、换绑定、旧 incarnation、simulation、session-only 都不能授权新 session；过去 execution 成功也不能刷新工具 inventory。source 和最近执行可读，私有配置和可执行路径不公开。

## 本机一次验证

```sh
tend agents local capabilities verify --socket "$RUNNER_CONTROL_SOCKET" --file verify.json --dry-run
tend agents local capabilities verify --socket "$RUNNER_CONTROL_SOCKET" --file verify.json
tend agents local capabilities status "$REQUEST_ID" --socket "$RUNNER_CONTROL_SOCKET"
tend agents local capabilities reconcile "$REQUEST_ID" --socket "$RUNNER_CONTROL_SOCKET"
```

verify.json 完整字段：request_id、expected_revision（原本机登记修订）、pin（从目录读回，禁止重建默认身份）、capability（ID/definition_revision）、probe_kind=tool_inventory、timeout_ms（1..30000）、max_attempts=1。CLI 不隐式生成 verification 身份、启动模型或生图；同请求重复只读原状态。rejected 保留错误码；unknown/report_pending 保留原请求，先 status/reconcile，不以新 ID 绕过。reconcile 不重发模型/枚举；已确认原 incarnation 被新进程围栏且 server 没有原报告时，明确返回 not_recorded/interrupted，之后才能显式新动作。

当前生产 adapters 不提供受信任 profile 工具全集和原子启动合同，verify 如实登记 adapter_evidence_unsupported，不生成假 confirmed。session 枚举且不能 adopt 时同样不支持。一个新的 adapter 必须实现 harness.CapabilityWorkers 的完整只读清单和 StartWithCapabilities；仅实现普通 Start/Available 不够。

## Task 约束与解释

在 `issue create-task/update-task --file` 的完整 spec 中为两 role 分别填写：

```json
{"agent_selection":{"implementation":{"category_id":"implementation","required_capabilities":[{"capability_id":"image_gen","definition_revision":1}]},"acceptance":{"category_id":"formal-review"}}}
```

此片段需放入既有完整 Task 合同；原 request/CAS/epoch 和独立验收政策不变。acceptance 不自动要求生图。未知、重复或退休能力拒绝新 spec。现有 run 继续读取其原 spec/pin/binding。

```sh
tend task list --capability image_gen --capability-role implement --json
tend task show "$TASK_ID" --json
tend task capability-explain "$TASK_ID" --role implement --spec-version 1 --limit 50 --json
```

explain 与 claim 共用 matcher 和调度围栏；支持候选 environment/agent/runtime/category/purpose/capability/state 及 after/limit。顶层 state 表示全部当前候选的实际调度资格，候选 current 表示其能力资格，scheduling_reason 另列 host/category/profile/workspace/owner/共享容量等阻塞。查询不探测、不分配 lease；历史 spec 不能冒充当前待调度动作。十分钟仍阻塞只产生一次持久行动提示，不自动更换 provider、身份或无限验证。

目前没有真实 image_gen 成功凭据。本功能能保守阻止无证据任务、保留原因并恢复查询；真实生图需要具体入口、来源/config/session、原请求及合理可观察预算，schema 或替身测试不能替代这些证据。
