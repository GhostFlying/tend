# 执行能力的管理与恢复

本指南依据 owner 范围修订 v1 和 D174：Capability 是用户对具体 Agent 入口显式配置的有版本标签。category 只用于委托角色；模型或 CLI 名称不会自动授予标签。声明和普通 adapter 可用性共同决定调度资格，均不表示工具调用成功。旧 native 实证要求已撤销；本功能不需要工具枚举、StartWithCapabilities、真实生图或费用观测。

## 定义与声明

```sh
tend capability list --state all --search image --limit 50 --json
tend capability show image_gen --definition-revision 1 --json
tend capability history image_gen --limit 50 --json
tend capability put render --revision 0 --data '{"title":"Render","description":"User-declared label","state":"active","contract_version":1,"verification_policy":"declaration_v1"}' --dry-run > original.json
tend request replay --file original.json
tend request show "$REQUEST_ID" --json
```

put 使用完整 JSON，创建 revision=0，后续更新/退休使用原 revision。重复动作使用原 request_id/Envelope；stale 不自动刷新 CAS，同 ID 改 payload 冲突。查询定义的 revision 参数在 CLI 中叫 `--definition-revision`，区分写操作的 CAS `--revision`。

本机配置增加 `"capability_version":1`，在具体 Agent 内增加 `"capabilities":[{"capability_id":"image_gen","definition_revision":1}]`。使用已有 `agents local validate/reload/register/registration/reconcile` 流程，按既有 Agent/runtime 修订规则更新；validate 不生效，register 登记的是声明，不确认工具。可以为用户明确选中的官方登录 Codex Agent 逐项或批量声明 image_gen，不能根据品牌自动赋值；完整配置路径和命令见 [Agent 配置指南](agent-definitions.md#显式-capability-声明d174)。这些是操作示例，不表示任何生产配置已经应用。未提供该扩展的旧快照和 hash 原样保留。声明不能被 category 或 skills 自动填入。

## 声明、修订与查询

```sh
tend agents capabilities list --environment "$ENVIRONMENT" --category implementation --capability image_gen --state pending --limit 50 --json
tend agents capabilities show "$ENVIRONMENT" "$AGENT" --json
tend agents capabilities revisions "$ENVIRONMENT" "$AGENT" --agent-revision 2 --limit 50 --json
# 以下仅查询可选历史诊断：
tend agents capabilities history "$ENVIRONMENT" "$AGENT" --kind tool_inventory --limit 50 --json
tend agents capabilities evidence "$EVIDENCE_ID" --json
```

list 支持 environment、agent（需 environment）、runtime、category、purpose、capability、state 和 after/limit。当前 state 是 eligible/pending/unavailable；原证据 state 则是 unverified/confirmed/unavailable，历史查询支持 kind/state/capability。定义和证据历史使用固定水位游标；Agent 当前目录和 Task 候选变化时返回 invalid_request，重新开始第一页。as_of 是查询水位/checked-at，不是新的实证时间。

revisions 查询声明的 Agent revision、定义 revision、登记版本和 snapshot/hash；当前目录显示显式声明及普通 executor 可用性。无声明、声明修订不匹配或旧协议不支持声明为 unavailable，离线/容量/普通 adapter 等待为 pending。Task 候选 current 是声明资格，scheduling_reason 保留普通调度阻塞；不可用入口的身份和声明仍可读。

历史 evidence 保留原 pin、incarnation、来源版本及 SHA、scope、observed/expires、原 request 和 kind。它们不会授予或撤回 declaration_v1 标签；过期、模拟或工具缺失不成为新声明任务的 gate。私有配置和可执行路径不公开。

## 历史诊断与旧请求对账（可选）

```sh
tend agents local capabilities verify --socket "$RUNNER_CONTROL_SOCKET" --file verify.json --dry-run
tend agents local capabilities verify --socket "$RUNNER_CONTROL_SOCKET" --file verify.json
tend agents local capabilities status "$REQUEST_ID" --socket "$RUNNER_CONTROL_SOCKET"
tend agents local capabilities reconcile "$REQUEST_ID" --socket "$RUNNER_CONTROL_SOCKET"
```

verify.json 完整字段：request_id、expected_revision（原本机登记修订）、pin（从目录读回，禁止重建默认身份）、capability（ID/definition_revision）、probe_kind=tool_inventory、timeout_ms（1..30000）、max_attempts=1。CLI 不隐式生成 verification 身份、启动模型或生图；同请求重复只读原状态。rejected 保留错误码；unknown/report_pending 保留原请求，先 status/reconcile，不以新 ID 绕过。reconcile 不重发模型/枚举；已确认原 incarnation 被新进程围栏且 server 没有原报告时，明确返回 not_recorded/interrupted，之后才能显式新动作。

这些命令保留旧工具实证的诊断和原请求对账。旧生产 adapter 可能返回 adapter_evidence_unsupported，不能伪造 confirmed；该结果不阻止声明匹配或普通 Start。新的声明任务不要求 CapabilityWorkers、StartWithCapabilities、工具 source/config/session 或预算观察。

缺省 match_policy 的历史 evidence-only 运行保留原格式恢复适配：只对同原 pin 的有效新鲜证据追加 revalidation，过期/错绑定不能授权旧运行恢复，原选择 binding 不改写。此兼容义务不扩展到 declaration_v1 的新运行。

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

无匹配时先读 explain：显式配置并登记 required 的精确 revision，恢复同一 executor 的普通可用性或等待共享容量；需要改变任务时显式修订未来 spec。unknown 先查原请求，不重发、不静默更换 provider/身份。十分钟仍等待只生成一次持久提示，不自动调用工具验证。

目前没有真实 image_gen 成功凭据，实际生图调用为零。声明覆盖 required 且普通 executor 可用、身份及其他调度约束满足时即可领取；未声明者不得领取 required 任务。声明匹配通过不表示任何生产 Agent 已配置或上线。
