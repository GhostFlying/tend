# 阶段 B 细化计划（一）：协议、存储、请求去重、领域状态机（B1–B4）

**日期：2026-10-15**
**依据：`docs/design-v0.1.md` §4、§5、§8、§9；`docs/design.md`；`docs/plans/phase-a-b.md` 阶段 B；约定见 `AGENTS.md`。**

本计划只覆盖 B1–B4。这四项做完后，账本可以在不依赖网络和模型的情况下，安全地接受有副作用的请求并推进任务状态。B5 及以后（outbox 投递、调度、runner 协议等）另写计划。

## 0. 范围与完成标准

- 交付物：`internal/protocol`、`internal/store`、`internal/app`（去重和执行框架，加上第一批用例）、`internal/domain`、`internal/testkit`（时钟和 ID），以及 `Makefile` 和 CI 前的本地检查。
- 完成标准：
  - `make check` 通过（gofmt、vet、staticcheck、`go test -race ./...`）；
  - 第 5 节列出的 `TestFault_*` 和状态机测试全部通过；
  - 没有任何代码路径能绕过 `app` 直接写库。
- 不在本计划内：HTTP API、工具面、outbox 的投递（只写入 outbox 表）、调度器、runner。

## 1. B1 `internal/protocol`

### 1.1 标识

- 所有 ID 都是带类型前缀的字符串：`req_`、`trc_`、`task_`、`act_`、`run_`、`art_`、`acc_`、`dec_`、`apv_`、`msg_`、`evt_`、`obx_`。
- 主体部分是 26 位 Crockford base32，由 48 位毫秒时间加 80 位随机数组成（ULID 布局），自己实现，不引入依赖。前缀方便日志排查，时间前缀让索引大致有序。
- 生成通过 `IDGen` 接口注入（AGENTS.md 约束 5），`testkit` 提供确定性实现。
- 解析和校验：`ParseRequestID` 等函数只检查前缀和格式，不赋予 ID 任何权限含义（ID 不是凭据）。

### 1.2 请求信封

```go
type Envelope struct {
    RequestID               string          // 必填；同一逻辑请求重试时不变
    ParentRequestID         string          // 可选；直接触发本请求的上游请求
    TraceID                 string          // 可选；缺省时由接收方生成，不参与去重
    Operation               string          // 例如 "tasks.create"
    Target                  Target          // {Type, ID}；创建类操作的 ID 可以为空
    ExpectedControlRevision *int64          // 控制类写入必填
    Payload                 json.RawMessage // 业务参数
}
```

- 调用方身份（`Principal`）不在信封里，由接入层从鉴权上下文得到并单独传入：
  - `owner`：tailnet 用户，加上客户端标识 client_id（同一用户可以同时有多个客户端，见 design.md D27 与 §9）；
  - `main_agent`：harness 实例加 thread；
  - `runner`：host；
  - `run`：run_id 加角色（implement 或 accept）；
  - `system`：tend 内部派生的请求。
- 去重域（dedup domain）由 Principal 推导：
  - owner 的所有客户端共用一个域，client_id 不参与；
  - 每个 harness 实例一个域；
  - 每个 host 一个域；
  - system 一个域。
  
  这样不同来源恰好生成相同的 request_id 也不会冲突。

### 1.3 payload_hash

- 对 `{operation, target, expected_control_revision, payload}` 做规范化 JSON 后取 SHA-256。规范化的规则是：对象的 key 排序；数字保持原样（用 `json.Number` 解码再编码），不转成浮点数；不保留无意义的空白。
- 传输层的字段（重试次数、时间戳、trace_id、parent_request_id）不参与计算。parent_request_id 不参与，是因为同一个逻辑请求可能在不同父请求下重放，它的意图并没有变。
- 用 golden 测试固定若干输入对应的哈希值，防止以后无意中改了规范化规则。

### 1.4 错误码与结果

- 错误码：`invalid_request`、`missing_request_id`、`conflict_payload`、`stale_revision`、`forbidden`、`not_found`、`invalid_transition`、`duplicate_effect`、`unknown_outcome`、`internal`。
- `duplicate_effect`：同一规划提交或同一 effect 已经由另一个请求完成；Details 给出原请求或已有动作的 ID，调用方据此查询，而不是重试。
- `Error{Code, Message, Details}`。Message 面向调用方，不包含内部错误文本（约束 8）。
- 有副作用请求的返回值是 `Operation{RequestID, Status, Result, Error}`：
  - Status 取 `succeeded`、`failed`、`accepted`、`unknown` 之一；
  - `accepted` 表示本地事务已提交，但还有外部阶段在进行（例如已写入 outbox），后续状态通过 `requests.read` 查询。

### 1.5 领域枚举

在 protocol 中定义线上可见的枚举，domain 包直接使用：

- TaskLifecycle：`draft` `queued` `running` `awaiting_acceptance` `changes_required` `completed` `cancelled`
- ControlMode：`managed` `paused` `human_control`
- BlockReason：`none` `waiting_resource` `waiting_decision` `waiting_approval` `acceptance_env_unavailable` `budget_exhausted`
- RunObserved：`starting` `running` `waiting` `finished` `failed` `interrupted` `unknown`
- RunDesired：`run` `paused` `stopped`
- RunRole：`implement` `accept`
- ActionKind：`implement` `accept` `deliver`
- ActionStatus：`pending` `ready` `dispatched` `done` `cancelled` `superseded`
- Verdict：`passed` `changes_required` `blocked` `inconclusive`

## 2. B2 `internal/store`

### 2.1 SQLite 约定

- 驱动使用 `modernc.org/sqlite`（新增依赖，理由：纯 Go 实现，runner 交叉编译到 Windows 和 macOS 时不需要 cgo）。
- 打开时设置：WAL、`foreign_keys=ON`、`busy_timeout=5000`、`synchronous=NORMAL`。
- 写连接单独一个连接池（MaxOpenConns=1），所有写事务都用 `BEGIN IMMEDIATE`，避免读锁升级成写锁时死锁。读连接另开一个池。
- 时间一律存 Unix 毫秒整数，来自注入的时钟。JSON 列存 TEXT，写入前用 protocol 的类型校验过。
- 迁移文件是嵌入二进制的 `migrations/NNNN_name.sql`，按序执行，已执行的版本记在 `schema_migrations` 表里。已提交的迁移不再修改（约束 10）。
- 对外提供 `Store.Write(ctx, func(tx *Tx) error)` 和 `Store.Read(ctx, func(tx *ReadTx) error)`。仓储方法挂在 `Tx` 上，只有 `app` 包会调用 `Write`。

### 2.2 `0001_core.sql` 包含的表

| 表 | 关键列与约束 |
| --- | --- |
| `request_records` | `domain`、`request_id`、`operation`、`payload_hash`、`principal`、`parent_request_id`、`trace_id`、`status`、`result_json`、`error_code`、`created_at`、`updated_at`；主键 `(domain, request_id)` |
| `tasks` | `task_id` PK、`title`、`lifecycle`、`control_mode`、`control_revision`、`spec_version`、`priority`、`block_reason`、`created_at`、`updated_at` |
| `task_specs` | 主键 `(task_id, spec_version)`；`body_json`（目标、范围、约束、验收条件、交付要求、预算）、`source_message_id`、`created_at`；只插入，不更新 |
| `task_dependencies` | 主键 `(task_id, depends_on)` |
| `plan_commits` | 主键 `(source_message_id, commit_seq)`；`request_id`、`created_at`。同一来源消息的同一次规划提交只能有一条 |
| `plan_actions` | `action_id` PK、`task_id`、`kind`、`effect_key` UNIQUE、`trigger_json`、`target_spec_version`、`target_artifact_id`、`authorization_json`、`generation`、`status`、`plan_commit_ref`、`created_request_id` |
| `runs` | `run_id` PK、`task_id`、`action_id`、`generation`、`role`、`host_id`、`native_json`、`desired_state`、`observed_state`、`result_json`、`last_confirmed_at`；UNIQUE `(action_id, generation)` |
| `artifacts` | `artifact_id` PK、`task_id`、`run_id`、`kind`、`ref_json`（仓库、分支、commit sha）、`digest`、`created_at` |
| `acceptances` | `acceptance_id` PK、`task_id`、`run_id`、`spec_version`、`artifact_id`、`verdict`、`evidence_json`、`issues_json`、`created_at` |
| `decisions` | `decision_id` PK、`task_id`（可空）、`question`、`options_json`、`scope`、`version`、`status`、`asked_message_id`、`answered_message_id`、`answer_json` |
| `approvals` | `approval_id` PK、`action_id`、`params_digest`、`spec_version`、`control_revision`、`expires_at`、`status` |
| `conversation_messages` | `message_id` PK、`seq` UNIQUE 自增、`author`、`body`、`reply_to`、`request_id`、`created_at` |
| `conversation_bindings` | 主键 `(message_id, object_type, object_id)`；`kind`（explicit、resolved、candidate） |
| `events` | `event_seq` INTEGER PK 自增（用作 SSE 游标）、`event_id` UNIQUE、`source`、`object_type`、`object_id`、`object_version`、`kind`、`body_json`、`request_id`、`trace_id`、`created_at` |
| `outbox` | `outbox_id` PK、`request_id` UNIQUE（预先分配的 child request_id）、`destination`、`operation`、`payload_json`、`status`、`attempts`、`next_attempt_at`、`created_at` |
| `inbox` | 主键 `(source, source_event_id)`；`received_at` |

`effect_key` 的格式是 `kind|task_id|spec_version|artifact_id|generation`，同一件事在数据库层只能有一个动作。runs 上的 `(action_id, generation)` 唯一约束保证同一个动作代次只能创建一个 run（v0.1 §8.6）。

hosts、leases、context_packets、thread_baselines 等表，留给 B7 和 B11 各自的迁移。

## 3. B3 请求去重与执行框架（`internal/app`）

### 3.1 用例接口

```go
type Command interface {
    Operation() string
    Authorize(p protocol.Principal, tx store.ReadView) error           // 每次都执行，包括重放
    Apply(tx *store.Tx, env protocol.Envelope, now time.Time) (Outcome, error)
}

type Outcome struct {
    Result json.RawMessage
    Events []store.EventDraft
    Outbox []store.OutboxDraft   // child request_id 在这里预先生成
    Status protocol.OpStatus     // succeeded 或 accepted
}
```

### 3.2 执行流程 `App.Execute(ctx, principal, env, cmd)`

1. 校验信封：request_id 缺失或格式非法、operation 不匹配，直接拒绝，不留记录，也不产生副作用。
2. 计算 payload_hash 和去重域。
3. 开启 `BEGIN IMMEDIATE` 事务，查询 `(domain, request_id)`：
   - **已存在，哈希不同** → `conflict_payload`，不覆盖原记录。
   - **已存在，哈希相同** → 重新执行 `Authorize`（撤权之后不能凭旧 ID 读到结果），然后返回记录里的状态和结果，不重复执行。
   - **不存在** → 执行 `Authorize`，失败就返回 `forbidden`，**不记录**（权限之后可能被授予）。通过后执行 `Apply`：
     - 成功：在同一事务里写入业务变化、events、outbox 和 request_records（状态 succeeded 或 accepted）；
     - 业务失败（`invalid_request`、`stale_revision`、`invalid_transition`、`not_found`、`duplicate_effect` 等确定性的错误）：Apply 在 SAVEPOINT 内执行，失败时回滚到 SAVEPOINT，**在同一事务中**记录失败状态、错误码和 Details，让同一 ID 重试时拿到相同的失败结果。同一事务完成，避免两个事务之间被其他请求插队；
     - 内部错误：回滚，不记录。调用方可以用同一个 ID 安全重试，因为没有任何东西提交。
4. 提交事务。

SQLite 只有一个写入者，同一 ID 的并发请求会被串行化：后到的那个读到已提交的记录，直接返回原结果。本地操作因此看不到“处理中”的状态。只有带外部阶段的操作会处于 `accepted`，它的后续状态由 outbox 和 run 推进。

### 3.3 只读请求

只读请求同样必须带 request_id，但不持久化记录，执行时也不经过去重表。同一 ID 的并发读用进程内的 singleflight 合并。

### 3.4 保留

有副作用请求的记录永不自动删除（v0.1 §8.5）。以后如果要清理，必须保留最小的“ID 已使用”标记。

### 3.5 第一批用例（用来验证框架，不追求完整）

- `tasks.create`：创建 task 和第一版 spec，写入 events。payload 必须包含 `source_message_id` 和 `commit_seq`，同时写入 `plan_commits`。
- `tasks.update_intent`：修改优先级、约束或 spec；校验 `expected_control_revision`；spec 有实质变化时生成新版本。
- `actions.create`：MVP 中规划方能显式创建的只有下一个 implement 代次（`domain.RequestImplement`），accept 和 deliver 动作由规则派生。请求已有代次时返回 `duplicate_effect`，受 effect_key 约束。
- `control.set_mode`：切换 managed、paused 或 human_control，control_revision 加 1。
- `runs.report_started`、`runs.submit_artifact` 与 `acceptances.submit`：只实现领域部分和事务，run 由测试替身创建（调度器在 B6）。被验收的 spec 版本和 artifact 取自验收 run 的动作，不取自请求。真正的接入在 B7 和 B9。
- 请求记录的 `error_details` 列由 `0002_request_error_details.sql` 增加（已合并的 0001 不修改）。

## 4. B4 `internal/domain`

### 4.1 形式

纯函数，只接收当前状态和命令参数，返回决定，不做 IO：

```go
type Decision struct {
    Task    *TaskState        // 变化后的状态；nil 表示不变
    Events  []EventDraft
    Actions []ActionDraft     // 需要新建的动作（带 effect_key）
    Supersede []string        // 需要标记为 superseded 的 action_id
}
```

### 4.2 状态与转换

- **任务生命周期**：

  | 从 | 到 | 条件 |
  | --- | --- | --- |
  | draft | queued | spec 可执行（目标和验收条件都不为空） |
  | queued | running | 当前 spec 版本的 implement run 已开始 |
  | running | awaiting_acceptance | 实现 run 提交了 artifact |
  | awaiting_acceptance | completed | 见 4.3 |
  | awaiting_acceptance | changes_required | 针对当前 spec 和 artifact 的验收结论为 changes_required |
  | changes_required | queued | 新的 implement 动作代次已创建（在预算内） |
  | 任意未结束状态 | cancelled | 用户明确取消 |
  | running、awaiting_acceptance、changes_required | queued | spec 版本提升（旧的动作被标记为 superseded） |

- **阻塞原因**和**控制模式**与生命周期正交，单独存储。paused 和 human_control 时不派生任何新动作。
- **run 观测状态**：
  - 允许的转换：`starting → running | failed | unknown`、`running ↔ waiting`、`running | waiting → finished | failed | interrupted | unknown`；
  - `unknown` 只能通过对账转到确定的状态；
  - finished 和 failed 是终态。
- 期望状态（desired）和观测状态（observed）分开存储，允许出现“暂停已请求、run 仍在运行”这样的组合（v0.1 §5.1）。

### 4.3 完成条件

只有同时满足以下三条时，才派生 completed，不存在无条件的 completed 写入：

1. 存在一条 verdict 为 passed 的验收，它的 spec_version 等于任务当前版本，artifact_id 等于当前的 artifact；
2. 这条验收由 accept 角色的 run 提交；
3. 当前 spec 要求的交付条件已满足（MVP 中只有“产出 commit”一种交付，验收通过即满足）。

### 4.4 派生规则

| 事件 | 派生 |
| --- | --- |
| 实现 run 提交 artifact（匹配当前 spec 和动作代次） | 创建 accept 动作，effect_key 为 `accept|task|spec|artifact|1` |
| 验收 passed（匹配当前版本） | 任务进入 completed |
| 验收 changes_required | 在预算内创建 `implement|task|spec|-|generation+1`；预算用完则开启待决事项，阻塞原因设为 `budget_exhausted` |
| 验收 blocked 或 inconclusive | 阻塞原因设为 `acceptance_env_unavailable`，并开启待决事项；不算实现缺陷，也不算通过 |
| 结果的 spec_version、artifact 或动作代次与当前不匹配 | 只写入事实事件，不派生任何动作（v0.1 §9.4） |

### 4.5 测试

- 生命周期转换表的穷举测试：每一对 (状态, 命令) 要么得到表中的结果，要么返回 `invalid_transition`。
- 不变量的随机序列测试（用标准库的 `testing/quick` 或手写的随机命令序列）：在任意命令序列下，completed 都只能在满足 4.3 时出现；同一个 effect_key 最多只派生一个动作。

## 5. 必须通过的测试（本阶段子集）

| 测试 | 场景（v0.1 §17.2） |
| --- | --- |
| `TestFault_RetrySameRequestID` | 同一 ID 重试：只创建一次，返回原结果 |
| `TestFault_ConcurrentSameRequestID` | 两个并发请求用相同 ID 和参数：只执行一次，另一方拿到同一结果 |
| `TestFault_SameIDDifferentPayload` | 相同 ID、不同参数：`conflict_payload`，原记录不变 |
| `TestFault_ChildRequestsDistinct` | 一个父请求派生的多个 outbox 消息各有不同的 child ID，重放父请求时 ID 不变 |
| `TestFault_NewCallIDSamePhase` | 用新的 request_id 重复创建同一阶段的动作：被 effect_key 拦下 |
| `TestFault_DuplicatePlanCommit` | 同一来源消息的同一次规划提交用不同 request_id 重放：被 `plan_commits` 拦下 |
| `TestFault_StaleAcceptance` | spec 升版后才到的旧验收通过：只记录事实，不关闭任务 |
| `TestFault_AcceptanceEnvFailure` | 验收结论为 blocked：任务进入阻塞，不是 changes_required，也不是 completed |
| `TestFault_StaleRevisionRejected` | 控制版本过期的写入：`stale_revision`，用同一 ID 重试返回相同错误 |
| `TestFault_ReplayAfterRevoke` | 撤权之后用旧 ID 重放：被 `Authorize` 拒绝 |
| `TestFault_InternalErrorRetrySafe` | Apply 中途出现内部错误：没有任何提交，同一 ID 重试正常执行 |

## 6. 实施顺序与提交拆分

1. `build: add makefile with check, test and it targets`：Makefile 以及 staticcheck 的安装方式（`go run honnef.co/go/tools/cmd/staticcheck@<版本>`，不进 go.mod）。
2. `feat: impl protocol identifiers, envelope and payload hash`：B1。
3. `feat: impl testkit clock and deterministic id generator`。
4. `feat: impl sqlite store with embedded migrations`：B2，新增 modernc 依赖。
5. `feat: impl domain lifecycle, run states and derivation rules`：B4（不依赖存储，可以和第 4 步并行）。
6. `feat: impl request dedup, command execution and first commands`：B3 框架、第一批用例和 `TestFault_*`。框架的测试离不开具体用例，因此原计划的第 6、7 步合为一个 commit。

每一步结束时 `make check` 都要通过。
