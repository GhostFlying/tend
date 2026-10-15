# 阶段 B 细化计划（二）：投递、调度、runner、验收、工具面、上下文、traex 接入（B5–B12）

**日期：2026-10-15**
**依据：`docs/design-v0.1.md` §6、§7、§9–§12、§14；`docs/design.md` §2–§6；`docs/findings/spike-a.md`；`docs/plans/phase-a-b.md` 阶段 B；约定见 `AGENTS.md`。**
**前置：B1–B4 已合并（`docs/plans/phase-b-core.md`）。**

本计划做完后，不经过 Web，服务端和 runner 可以在测试里跑通：用户消息 → 主 Agent（fake 或真实 traex）→ 动态工具建任务 → runner 领取并实现 → 独立验收 → completed 或待决事项。阶段 C 只需要在此之上接 HTTP API、SSE 和 Web。

## 0. 范围与完成标准

- 交付物：`internal/dispatch`、`internal/scheduler`、`internal/runnerapi`、`internal/netx`、`internal/runner`、`internal/harness`（接口和 fake）、`internal/harness/traex`、`internal/tools`、`internal/contextbuilder`、`internal/mainthread`（新增，见 §8.3），以及 `cmd/tend-server`、`cmd/tend-runner`。
- 完成标准：
  - `make check` 通过；CI 的交叉编译覆盖 `cmd/tend-runner`；
  - §10 列出的 `TestFault_*` 全部通过；
  - 一个进程内端到端测试（真实 SQLite、真实 HTTP、fake harness）从消息跑到 completed；
  - 一个 `integration` 标签的端到端测试用真实 traex 跑通同样的流程（`make it`，不进 CI）。
- 不在本计划内：浏览器 HTTP API 与 SSE（`internal/api`，阶段 C）、人工接管、多机器调度策略、任务范围的后台推理 thread、Electron。
- `phase-a-b.md` 工具链里列的 MCP go-sdk 依赖不再需要（D20 改为动态工具），本计划不引入。

## 1. 需要写入 design.md 的决策（提议）

以下是实现时需要定下来的设计点，在对应的 commit 里写进 `docs/design.md` 并引用编号。

| 编号 | 决策 | 落地 |
| --- | --- | --- |
| D28 | 外部系统的原生身份（harness 的 thread/turn/callId、runner 的事件序号）不直接当 request_id。服务端用 `native_requests(source, native_key)` 映射表，第一次见到时分配一个 `req_` ID，之后同一原生身份总是得到同一个 ID。修订 design.md §3 中 `harness:<instance>:...` 的写法（request_id 必须是 `req_` + ULID）。 | B10 |
| D29 | run 级凭据由服务端密钥派生：`token = HMAC-SHA256(server_secret, lease_id)`。数据库和请求记录里都不存 token；claim 重放时重新计算。`server_secret` 通过 env file 注入（约束 13）。 | B7 |
| D30 | 仓库预先注册。服务端维护仓库表：逻辑名、远程地址、基线候选（D36）；host 声明自己服务哪些仓库。runner 把仓库 bare clone 到 `$TEND_HOME/repos/<repo>.git`，所有工作区的 worktree 都从这里派生。账本只记录逻辑名和相对路径，不记录机器上的绝对路径。 | B6、B8 |
| D31 | MVP 没有后台推理 thread：实现 run 失败一律开启待决事项（重试、修改 spec、取消）；验收 run 失败按 `blocked` 处理。 | B9 |
| D32 | 配置文件使用 HuJSON（带注释的 JSON），解析用 `github.com/tailscale/hujson`。它已经是 tsnet 的间接依赖，不增加新的 module。 | B7 |
| D33 | 工作区由 runner 统一管理。`$TEND_HOME/workspaces/<name>/`（`TEND_HOME` 默认 `~/.tend`）是工作区根目录，参与的每个仓库是其中一个子目录（默认用仓库逻辑名，例如 `feature_a/douyin`、`feature_a/template-assembler`）；只有一个仓库时也用同样的结构。worker 以工作区根目录为 cwd 开始工作。工作区跟着任务走：同一任务的各代实现复用同一个工作区，因此固定在创建它的 host 上；不迁移，host 离线时等待（v0.1 §10）。worker 不自己开 worktree，临时副本只能放在 `$TEND_HOME/scratch/<name>/`，不算产物。 | B6、B8 |
| D34 | 工作区由一组 mount 组成：`mounts[{repo, path, base, profile}]`，阶段 B 就支持多个 mount。artifact 记录所有 mount 的 commit（包括没有修改的），验收按这个组合重建工作区。仓库自带工具同步的只读依赖（例如 LCM 的 DEPS）不是 mount，由 prepare 处理（D35）。 | B6、B8 |
| D35 | prepare 可插拔：checkout 之后，先按各 mount 的 profile 执行 runner 配置中的命令（例如 template-assembler 的 `lcm sync`、`hab sync`），再执行工作区级的 prepare（把多个仓库连起来，例如让 douyin 使用本地的 template-assembler）。可以声明哪些输入变化时需要重新执行。prepare 失败按环境问题处理，不算实现缺陷。 | B8 |
| D36 | 基线解析：每次分配工作区或新增 mount 之前，涉及的仓库都先 fetch。默认基线是远端最新的主干，按仓库配置的候选顺序解析，没有配置时按启发式顺序：远端 HEAD 指向的分支 → `main` → `master`。主 Agent 可以为每个 mount 指定分支或 commit，指定了就不再 fallback。spec 只保存意图，解析出的 commit sha 作为事实记录在工作区上。 | B6、B8 |
| D37 | 工作区说明文件：工作区根目录本身不在任何 git 仓库里，traex 不会加载各仓库的 AGENTS.md（Codex 只加载从 `.git` 所在根目录到 cwd 这条路径上的 AGENTS.md）。runner 在工作区根目录生成 `AGENTS.md`，写明布局（子目录、仓库、基线、分支）、工作规则，以及“在某个仓库里工作之前先读它的 AGENTS.md”。这个文件不属于任何仓库，不进 artifact。 | B8 |
| D38 | worker 暂不托管：runner 直接使用所在机器上已安装的 traex 和用户的默认配置（home、鉴权、skills、plugins、用户级 AGENTS.md、默认模型）。runner 只覆盖无人值守运行必须改的项：cwd、`approvalPolicy=never`、sandbox（D39），以及 tend 的 developerInstructions 和动态工具。托管的 worker 配置（专用 home、裁剪工具）留到以后。主 Agent 仍然按 spike A5 托管。 | B12 |
| D39 | 部署形态与 sandbox（修订 D26）：服务端、主 Agent、runner 和 worker 都直接运行在各自的机器上，不在 Docker 里；dev container 只用于开发和单元测试。第一期主 Agent 和 worker 一律使用 `danger-full-access`，sandbox 设置做成配置项，sandbox（大概率是 auto mode）后续单独跟进。主 Agent 只通过 tend 工具写账本、worker 不碰 git 历史，在第一期都是约定加事后检查，不是强制边界。 | B12 |

## 2. 执行框架的两处补充（B5 之前的准备）

1. **无副作用的结果不记录**：`Outcome.NoEffect`。命令发现没有任何事要做（典型的是 `runner.claim` 长轮询没有领到工作）时返回它，`Execute` 回滚整个事务，不写请求记录。用同一个 ID 重试是安全的，因为什么都没有发生；也避免每台 host 每 30 秒往 `request_records` 里写一行。
2. **system 主体的维护命令**：租约过期扫描、启动对账等由 tend 自己发起的写入，每次尝试用新的 request_id，幂等性靠状态前置条件保证（例如“仅当 lease 仍为 active 且已过期”），不靠去重表。这条写进 `app` 包的文档注释，评审时检查。

## 3. B5 outbox 投递（`internal/dispatch`）

runner 是 pull 模式（D19），服务端不主动连 runner，所以 outbox 的目的地只有服务端进程内的组件。MVP 中只有一个：`main_thread`（把用户消息投递给主会话，见 §8）。设计成通用的，阶段 D 的推送通知等可以直接接入。

### 3.1 状态

`outbox.status`：`pending` → `sending` → `delivered` | `failed` | `unknown`。

- 投递前，在一个短事务里把行置为 `sending`，`attempts + 1`。
- 投递后，在另一个事务里写结果。结果的应用和后续派生（例如消息已开始处理的事件）在同一个事务里。
- `failed` 只用于确定没有生效的失败（例如 handler 明确拒绝）；可以重试的错误回到 `pending`，按指数退避设置 `next_attempt_at`（上限可配，默认 1 分钟）。

### 3.2 Handler 接口

```go
type Handler interface {
    // Deliver sends the message. It must use msg.RequestID as the
    // receiver-side idempotency key.
    Deliver(ctx context.Context, msg Message) Result
    // Reconcile decides what happened to a message found in sending state
    // after a restart: delivered, not delivered, or unknown.
    Reconcile(ctx context.Context, msg Message) Result
}
```

- `Result` 取 `Delivered`、`Retry`（确定没生效）、`Failed`、`Unknown` 之一。
- **重启恢复**：启动时，处于 `sending` 的行一律先调用 `Reconcile`，不直接重发。`Unknown` 写成 `unknown` 状态并产生事件，由上层决定（约束 4）。
- 接收方按 `msg.RequestID` 去重，所以 `Reconcile` 能确认“没生效”时，用同一个 child ID 重发是安全的。

### 3.3 唤醒与并发

- `app` 提交带 outbox 的事务后，通过一个进程内的 `Notifier`（带缓冲的 channel，合并多次通知）唤醒 dispatcher，同时保留定时扫描兜底。
- 每个目的地串行投递（main_thread 本身就要求串行，见 v0.1 §6.2），不同目的地之间并行。
- `Notifier` 在 `app` 包里定义为接口，避免 `app` 依赖 `dispatch`。

### 3.4 inbox

`inbox(source, source_event_id)` 用于外部系统自带身份的事实：runner 上报的事件以 `(run_id, runner_seq)` 去重（§5.4）。inbox 的写入、事件应用和派生在同一个 `app` 事务里完成（v0.1 §9.1）。

## 4. B6 调度（`internal/scheduler`）

### 4.1 资源模型（迁移 `0003_hosts.sql`）

| 表 | 关键列 |
| --- | --- |
| `hosts` | `host_id` PK、`os`、`arch`、`labels_json`、`repos_json`（该 host 可用的逻辑仓库名）、`slots`、`token_hash`、`node_json`（允许的 tailnet 节点：node key 或 tag 加主机名）、`status`、`last_seen_at` |

- host 由服务端配置文件静态定义，启动时由一个 system 命令同步进表；runner 的 `hello` 只上报实际能力和持有的 run，不能给自己加 slot 或标签。
- slot 占用不单独存，由 `runs` 中该 host 上未结束的 run 数量计算。

### 4.2 仓库、工作区与 TaskSpec 扩展（D30、D33、D34、D36）

同一个迁移里增加：

| 表 | 关键列 |
| --- | --- |
| `repos` | `repo` PK（逻辑名，`[a-z0-9][a-z0-9_-]*`）、`remote_url`、`base_candidates_json`（可空，默认基线的候选分支，按顺序尝试）、`created_at`。和 host 一样由服务端配置同步进表。 |
| `workspaces` | `workspace_id` PK、`task_id` UNIQUE、`name` UNIQUE、`host_id`、`status`（`assigned`、`ready`、`retained`、`removed`）、`created_at`、`updated_at` |
| `workspace_mounts` | 主键 `(workspace_id, path)`；`repo`、`base_json`（spec 中的意图）、`resolved_base_sha`、`resolved_from`（命中的是哪个候选或显式指定）、`branch`、`added_at` |

**TaskSpec**：`domain.Spec` 增加 `Workspace` 和 `Requirements{OS, Labels}`。

```go
type Workspace struct {
    Template string  // provenance only; mounts are always explicit
    Mounts   []Mount // at least one
    Profile  string  // workspace-level prepare profile, optional
}
type Mount struct {
    Repo    string // registered logical name
    Path    string // sub-directory in the workspace; defaults to Repo
    Base    Base   // empty means the remote trunk (D36)
    Profile string // per-mount prepare profile, optional
}
type Base struct {
    Branch string // a remote branch, such as "release/3.2"
    Commit string // a full commit sha; at most one of Branch and Commit
}
```

- 校验：仓库必须已注册；`Path` 是单层目录名，在工作区内唯一；`Branch` 和 `Commit` 至多一个；`Commit` 必须是完整的 sha；同一个仓库在工作区内只能出现一次（各 mount 共享该仓库的 bare clone，分支都叫 `tend/<name>`，重复会冲突）。`Spec.Equal` 同步比较这些字段。
- **工作区模板**：服务端配置可以定义常用组合（例如 `douyin-with-lynx`：两个 mount 加一个工作区级 prepare profile）。`tasks.create` 可以只给模板名，由服务端展开成显式的 mount 写进 spec，并在 `Template` 记下来源。之后改模板不影响已有任务。
- **spec 修改**：可以新增 mount（下一个 run 开始前由 runner 添加）；删除 mount、修改已有 mount 的仓库或基线，阶段 B 直接拒绝，因为这会丢弃或隐式变基已有的工作。需要换基线时新建任务。
- **工作区名称**：`tasks.create` 接受可选的 `workspace_name`（规划方给出的短名，例如 `feature_a`），校验格式和唯一性；不给时用 `task-<task_id 后 8 位>`。每个仓库里的分支都是 `tend/<name>`，对人可读，以后提 MR 也直接可用。
- **生命周期**：工作区记录在第一个 implement run 被领取时创建（同一事务），此后该任务的 implement run 都使用它。基线在 runner 上解析（它负责 fetch），runner 在启动 worker 之前用 `runs.report_workspace` 上报每个 mount 的解析结果，写入 `workspace_mounts`。任务 completed 或 cancelled 后工作区置为 `retained`；runner 按保留期（配置，默认 7 天）清理后上报 `removed`。人工接管（阶段 D）时可以直接给出“哪台 host 上的哪个工作区”。

### 4.3 选择规则

纯函数 `scheduler.Pick(host HostView, usage Usage, candidates []Candidate) (Candidate, bool)`，不做 IO，由 `runner.claim` 在领取事务内调用（§5.2）。MVP 不需要单独的调度循环。

候选动作需要同时满足：

1. 动作状态为 `pending`，kind 是 implement 或 accept，`target_spec_version` 等于任务当前版本；
2. 任务处于 `managed`，没有阻塞原因，生命周期不是终态；依赖的任务都已 completed；
3. host 满足 `Requirements`（OS、标签全部包含），所有 mount 的仓库都在该 host 的仓库列表里；host 还有空闲 slot；
4. **工作区亲和**：任务已有工作区时，implement 和 accept 动作都只能被该工作区所在的 host 领取（D33）。accept 也受限，是因为 artifact 的 commit 只存在于产出它的 host 的 bare 仓库里，跨 host 传输留到 §12；
5. **工作区独占**：implement 动作要求该任务没有任何 implement run 处于 `starting`、`running`、`waiting`、`interrupted`、`unknown`。`unknown` 也算占用，这就是“心跳丢失但 runner 仍在运行时不重新分配可写工作区”（v0.1 §9.3）。accept 动作在独立 worktree 中运行，不受这条限制。

排序：优先级降序，然后动作创建时间升序。不做抢占（v0.1 §10）。

## 5. B7 runner 协议服务端（`internal/runnerapi`、`internal/netx`）

### 5.1 传输与鉴权

- HTTP + JSON，`POST /runner/v1/{hello,claim,heartbeat,events,complete}`，请求体是 v0.1 §8.3 的信封。只挂在 tsnet listener 上（D21、D23）。
- `internal/netx` 提供 `Listener` 和 `WhoIs(remoteAddr)` 两个接口；正式实现封装 tsnet，测试实现是本地 listener 加一张固定的身份表。
- 两层身份（design.md §6.3）：
  1. WhoIs 得到的节点必须与该 host 配置的 `node_json` 匹配；
  2. `Authorization: Bearer <host token>`，与 `hosts.token_hash` 比对（常量时间比较），得到 `Principal{runner, host_id}`。
- run 级操作（events、complete）使用 run token（D29），得到 `Principal{run, run_id, role}`，同时要求它来自持有该 run 的 host 的节点。

### 5.2 接口语义

| 接口 | 主体 | 语义 |
| --- | --- | --- |
| `runner.hello` | runner | 上报能力和 journal 中持有的 run 列表 `[{run_id, lease_id, observed, last_seq}]`，返回每个 run 的对账结论（§5.3）。有副作用，走去重。 |
| `runner.claim` | runner | 长轮询（默认最长 25 秒）。在一个事务里调用 `scheduler.Pick`，创建 run（`(action_id, generation)` 唯一）、lease 和事件，动作置为 `dispatched`；返回 run_id、lease_id、TTL、run token、不可变 spec 正文、workspace 和（accept 时）artifact 引用。没有工作时返回 `NoEffect`（§2）。长轮询期间等待 `Notifier`，不忙等。 |
| `runner.heartbeat` | runner | 批量续租该 host 的所有 lease，返回每个 run 的期望状态（`run`、`paused`、`stopped`）和 lease 是否仍然有效。只续租和读取期望状态，不写请求记录（续租以 lease 状态为前置条件，天然幂等）。 |
| `runner.events` | run | 批量上报 `{seq, native_id, kind, body}`。按 `(run_id, seq)` 进 inbox 去重；`kind` 在白名单里的（`run.running`、`run.waiting`）改变观测状态，其余写进 `run_logs`（新表，不进 `events`，避免日志淹没 SSE）。 |
| `runs.report_workspace` | run | 实现角色在启动 worker 之前上报工作区：每个 mount 的 `{path, resolved_base_sha, resolved_from, branch}`。只能在 run 处于 starting 时调用一次（重放返回原结果）。 |
| `runner.complete` | run | 实现角色：映射到 `runs.submit_artifact`（payload 改为 `{mounts:[{repo, path, base_sha, branch, commit_sha, changed}]}`，覆盖工作区的全部 mount；digest 是按 path 排序后列表的哈希），或者失败 `{reason}`。验收角色：映射到 `acceptances.submit`，或者失败。run 观测状态同时置为 finished 或 failed。 |

### 5.3 租约与对账（迁移 `0004_leases.sql`）

- `leases`：`lease_id` PK、`run_id` UNIQUE、`host_id`、`expires_at`、`status`（`active`、`lapsed`、`released`）。MVP 一个 run 只有一个 lease。
- TTL 默认 60 秒，runner 每 15 秒心跳。响应里返回 **TTL 时长** 而不是绝对时间，runner 用本地单调时钟计时，避免两边时钟偏差。
- **过期扫描**（system 命令，§2）：lease 过期仍未续租 → lease 置为 `lapsed`，run 观测状态置为 `unknown`，写事件。**不释放工作区**，调度规则把 `unknown` 视为占用。
- **hello 对账**：

  | runner 报告 | 服务端记录 | 结论 |
  | --- | --- | --- |
  | 持有 run，lease 一致 | active | 继续 |
  | 持有 run | lapsed，run 为 unknown，任务没有新的 implement run | 重新激活 lease，run 按 runner 报告的状态离开 unknown（`reconciling=true`） |
  | 持有 run | 动作已被 superseded 或 cancelled，或者 run 已结束 | 期望状态 `stopped`，runner 丢弃该 run |
  | 不持有 | run 已分配给该 host，处于 starting | 把这个分配重新交给 runner（返回内容与 claim 的结果相同，run token 由 lease_id 重新派生），runner 写入 journal 后照常开始。runner 在开始工作前先写 journal，所以“不持有”说明它从未开始，不需要新建 run：这样不会占用第二个 slot，也不会碰到 `(action_id, generation)` 唯一约束 |
  | 持有 | 服务端没有这个 run | 期望状态 `stopped`，写事件（数据库从旧备份恢复的情形，v0.1 §17.2 最后一项） |

### 5.4 run_logs（同在 `0004`）

`run_logs`：主键 `(run_id, seq)`；`native_id`、`kind`、`body_json`、`created_at`。阶段 C 的 Web 按需分页读取。

## 6. B8 runner 客户端（`internal/runner`、`cmd/tend-runner`）

### 6.1 结构

- `runner` 不 import 服务端的任何包（AGENTS.md §1.1），只依赖 `protocol`、`harness`、`netx`。
- **journal**：runner 自己的 SQLite 文件（同样用 modernc，嵌入一份很小的迁移，代码放在 `internal/runner/journal`）。记录：待发送的 claim request_id、持有的 run（run_id、lease_id、角色、worktree、观测状态、已上报的最大 seq）、待上报的事件。
- **先写 journal 再行动**：发 claim 前先记下 request_id；收到分配后先写 journal，再创建 worktree 和启动 harness。重启时先用 journal 中未完成的 claim ID 重发，再 `hello`，然后才领新工作。

### 6.2 执行一个 run

1. 准备工作区（§6.5）。implement 使用任务的工作区；accept 使用一次性的验收工作区，以 detached 方式检出 artifact 中各 mount 的 commit。
2. 通过 `harness.Worker` 启动 worker（§8.4），把事件转成 `runner.events`。
3. turn 结束后，runner 先检查 git 状态（§6.5），再由 runner 执行 `git add -A` 和 commit，把每个 mount 的 `{repo, path, branch, commit_sha}` 作为 artifact 提交；工作区没有变化则按失败上报（`no_changes`）。accept 读取 worker 通过 `submit_verdict` 工具给出的结论（§8.4），没有调用就是 `inconclusive`。accept 工作区里的修改一律丢弃。
4. 期望状态变为 `stopped` 或 `paused` 时中断 turn；`paused` 的恢复留到阶段 D。

### 6.3 自我 fencing

- 每次心跳成功后，本地截止时间 = 发送时刻（单调时钟）+ TTL − 安全余量（默认 TTL 的 1/4）。
- 超过截止时间仍没有续租成功：中断 harness turn，停止对工作区的写入，journal 中把该 run 标为 `fenced`，然后不断尝试 `hello`，按对账结论继续或丢弃。
- 由 runner 自己执行 fencing，这才是有效的 fencing（v0.1 §9.3）。

### 6.4 配置

HuJSON 文件（D32）：服务端地址、host_id、`TEND_HOME`、tsnet 主机名、每个仓库的 prepare profile 和可选的 `reference`（§6.5）、工作区保留期、worker 的 harness 设置。host token 和 tsnet auth key 从 env file 读取（约束 13）。

### 6.5 仓库与工作区（D30、D33–D37）

目录结构：

```
$TEND_HOME/                         默认 ~/.tend
  repos/<repo>.git                  bare clone，同一仓库的所有工作区共享对象库
  workspaces/<name>/                工作区根目录，worker 的 cwd
    AGENTS.md                       runner 生成（D37）
    <path>/                         每个 mount 一个 worktree，例如 douyin/、template-assembler/
  workspaces/.accept/<run_id>/      验收工作区，结构相同，run 结束后删除
  scratch/<name>/                   worker 的临时目录，不算产物
  runner/                           journal、tsnet state
```

**仓库**

- 第一次需要某个仓库时 `git clone --bare`，补上 `+refs/heads/*:refs/remotes/origin/*` 的 fetch refspec（bare clone 默认不设置）。
- 已经有本地 clone 的大仓库（template-assembler 的 `.git` 约 10GB），runner 配置可以给出 `reference` 路径，clone 时用 `--reference --dissociate`，节省首次下载。
- 每个 bare 仓库一把进程内的锁，同一仓库的 fetch 和 worktree 操作串行，不同仓库并行。

**fetch 与基线解析**（D36）

1. 分配工作区或新增 mount 之前，对涉及的每个仓库执行 `git fetch --prune origin`，并刷新远端 HEAD（`git ls-remote --symref origin HEAD`）。fetch 失败时 run 以 `fetch_failed` 失败（环境问题），不悄悄使用本地的旧 ref。
2. 每个 mount 的基线：
   - 指定了 `Commit`：fetch 后对象必须存在；不存在就再试一次 `git fetch origin <sha>`，仍然没有则以 `base_not_found` 失败；
   - 指定了 `Branch`：使用 `refs/remotes/origin/<branch>`，不存在就以 `base_not_found` 失败，不 fallback；
   - 都没指定：依次尝试仓库配置的 `base_candidates`，然后是远端 HEAD 指向的分支、`main`、`master`，第一个存在的胜出。
3. 解析结果（sha 和命中的候选）通过 `runs.report_workspace` 写入账本。之后的各代实现在同一分支上继续，不再重新解析基线；fetch 只用于新增的 mount。
4. 解析逻辑是纯函数（输入：意图、候选列表、远端 ref 列表；输出：sha 和来源），单独穷举测试；git 调用只负责提供 ref 列表。

**创建工作区**

- 建工作区根目录，对每个 mount 执行 `git -C repos/<repo>.git worktree add -b tend/<name> workspaces/<name>/<path> <sha>`。
- 生成工作区根目录的 `AGENTS.md`（D37）：工作区布局表（子目录、仓库、基线 sha 和来源、分支），工作规则（不得 commit、切换分支或创建 worktree；临时文件放 scratch），以及各 mount 下 AGENTS.md 的路径和“在该仓库工作之前先读它”的要求。mount 变化时重新生成。
- B12 用真实 traex 确认：以工作区根目录为 cwd 时，`instructionSources` 里只有这个文件和 host 用户级的 AGENTS.md；同时确认 traex 0.209.1 是否已经有上游的 `runtimeWorkspaceRoots` 和 turn 级 `environments`，有的话评估能否用来让各仓库的 AGENTS.md 按原生方式加载。

**prepare**（D35）

- 先按 mount 的 profile 在各 mount 目录执行 runner 配置中的命令列表，再执行工作区级 profile（cwd 为工作区根目录）。输出写入 run 日志。
- profile 声明 `rerun_if_changed`（例如 `.lcm`、`dependencies/**`）；runner 记录这些输入的哈希，复用工作区时只有哈希变化才重新执行。
- prepare 失败：implement run 以 `workspace_prepare_failed` 失败；accept run 按 `blocked` 处理（D31）。

**turn 结束后**

- **完整性检查**：第一期 worker 使用 `danger-full-access`（D39），spike A7 中“sandbox 不允许写 `.git`”的保证不成立，“不碰 git 历史”只是约定，靠这项检查兜底。runner 对每个 mount 检查：HEAD 和分支没有移动，`.git` 配置没有改动；bare 仓库里没有新增 runner 不知道的 worktree。违反时 run 以 `git_tampered` 失败，不提交 artifact。
- **提交**：runner 对每个有改动的 mount 执行 `git add -A` 和 commit；artifact 包含全部 mount 的 `{repo, path, base_sha, branch, commit_sha, changed}`。没有任何 mount 有改动时以 `no_changes` 失败。
- **验收工作区**：在 `.accept/<run_id>/` 下按 artifact 中各 mount 的 commit 以 detached 方式重建同样的布局，重新生成 `AGENTS.md`（验收角色的说明），执行 prepare。修改一律丢弃。
- **sandbox 设置**：第一期固定为 `danger-full-access`（D39），scratch 和 prepare 用到的缓存目录不受限制。
- **清理**：工作区 `retained` 超过保留期后，runner 逐个 `git worktree remove`、删除分支，再删工作区根目录。已经被 artifact 引用的 commit 保留 `refs/tend/artifacts/<artifact_id>/<path>` 引用，避免对象被 gc。

已知风险（template-assembler）：一个准备好的工作区约 14GB，`lcm sync` 和 `hab sync` 耗时长。验收工作区如果每次从头准备，成本不可接受。阶段 C 接入 template-assembler 时，需要为验收工作区做预热池或复用，见 §12。

## 7. B9 验收流程

大部分规则已在 B4 的 domain 中实现（artifact → accept 动作、只有当前版本的结论能推进任务）。本项补齐：

- **重试验收**：现有的 accept effect key 固定为第 1 代，待决事项里的“retry acceptance”目前没有实现路径。新增 `domain.RequestAcceptance(t, generation, expectedRevision)`，为当前 artifact 创建下一代 accept 动作；`actions.create` 放开 `kind=accept`，并清除 `acceptance_env_unavailable` 阻塞。
- **run 失败**（D31）：新增 `domain.RunFailed`。implement 失败开启待决事项，阻塞原因 `waiting_decision`；accept 失败等同 `blocked` 结论。只有当前代次的失败才推进，旧代次只记录事实。
- **取消或 spec 升版后仍在运行的 run**：通过心跳把期望状态改为 `stopped`；之后到达的结果只作为事实保存（已由 domain 保证）。
- **决策的回答**：`decisions.respond`（owner）和 `decisions.resolve_from_message`（主 Agent，附 source_message_id），都校验决策的 `version`。回答本身只关闭决策；后续做什么（重试、改 spec、取消）由回答方显式调用对应命令，不在回答里隐式触发。

## 8. B10–B12：主会话、工具面、上下文、traex

### 8.1 B10 工具面（`internal/tools`）

- 工具以 namespace `tend` 注册为动态工具，名称用下划线（模型的函数名不允许点号）：`tasks_list`、`tasks_read`、`tasks_create`、`tasks_update_intent`、`actions_create`、`decisions_open`、`decisions_resolve`、`artifacts_read`、`requests_read`，以及只读的 `repos_list`（已注册的仓库、工作区模板，以及哪些 host 服务哪些仓库，供主 Agent 在建任务时选择 mount）。名称到 `operation` 的映射是一张静态表。
- 每个工具有 JSON Schema 输入定义，从 Go 结构体生成，单元测试检查 schema 与结构体一致。
- **身份**：`Principal{main_agent, harness_instance, thread}` 由连接和 threadId 决定（design.md §3），不读 arguments。
- **request_id**（D28）：`native_requests` 表（迁移 `0005`），`source = "harness:<instance>"`，`native_key = "<threadId>/<turnId>/<callId>"`。映射在执行命令的同一个事务里创建。
- **source_message_id 由服务端填**：`mainthread` 知道当前 turn 处理的是哪条用户消息，调用工具层时作为参数传入（`tools.Call{Principal, NativeKey, SourceMessageID, ...}`），工具层不读模型传入的值，也不反查 `mainthread` 的表。`commit_seq` 由模型提供（同一条消息的第几次规划写入），A5 中观察到的“相同参数、不同 callId”的重复调用会被 `plan_commits` 拦下。
- **返回**：`{success, contentItems:[text]}`，text 是紧凑 JSON。失败时带错误码；`stale_revision` 附带当前状态摘要，让模型在同一个 turn 里拿到新事实（design.md §2.2 第 5 条）。
- 主 Agent 没有 `acceptances.submit`、`control.set_mode`（human_control）和任何能写 completed 的工具。

### 8.2 B11 上下文构建（`internal/contextbuilder`）

迁移 `0006_context.sql`：

| 表 | 关键列 |
| --- | --- |
| `context_packets` | `packet_id` PK、`thread_key`、`epoch`、`kind`（full、delta）、`from_seq`、`to_seq`、`body`、`status`（built、injected、failed、unknown）、`created_at` |
| `thread_baselines` | `thread_key` PK、`epoch`、`watermark_seq`、`last_packet_id`、`valid`、`updated_at` |

流程（每个前台 turn 开始前，由 `mainthread` 调用）：

1. 读取 baseline。不存在或 `valid=0` → 生成完整快照，epoch 加 1。
2. 否则取 `watermark_seq` 之后的相关事件（白名单：生命周期跨越边界、待决事项新增或解决、约束或优先级变化、验收结论、unknown 和阻塞）。没有 → 不注入。有 → 生成 delta。
3. packet 先落库（`built`），再 `thread/inject_items`；注入成功且随后 `turn/start` 成功，才把 packet 置为 `injected` 并推进水位。任一步失败或结果不确定：packet 置为 `failed` 或 `unknown`，baseline 置为无效，**下一次生成新的完整快照，不重放旧 packet**（design.md §2.2、spike A2）。
4. 失效来源：观察到 `contextCompaction` item、thread 新建、thread 恢复后发现最后一个 packet 停在 `built`（tend 在注入途中崩溃）。

内容与裁剪：

- 渲染是纯函数（输入快照结构，输出文本），开头是 `[tend context packet <packet_id> epoch=<n> watermark=<seq>]`。
- 分区优先级（v0.1 §7.3）：约束与范围 > 当前待决事项 > 阻塞和 unknown > 讨论焦点 > 其他未结束任务的索引 > 普通状态。超出预算（默认完整快照 4k、delta 1.5k token，按字符数 / 4 估算）时从低优先级整块删除，不截断单条约束；被删的部分留一行引用，提示用工具读取。
- **讨论焦点**来自最近的会话消息和它们的 binding，不来自最近的系统事件（v0.1 §6.1）。
- worker 的自由文本和日志不进入 packet（v0.1 §7.6）。

### 8.3 B12a 主会话（`internal/mainthread`，新增包）

`AGENTS.md` §1 的结构里没有这一层，同一个 commit 里补上：`mainthread` 位于 `cmd` 之下，依赖 `app`、`tools`、`contextbuilder`、`harness`。

- **入口**：新命令 `conversation.submit_message`（owner）在一个事务里写入 `conversation_messages`（带 `client_id`、`reply_to`、`last_seen_seq`）、事件，以及一条目的地为 `main_thread` 的 outbox 消息。
- **投递**（实现 §3.2 的 `Handler`）：串行处理，一条消息一个 turn。
  1. 以 outbox 的 child request_id 为键查 `main_turns`：已经有记录且 turn 已开始 → `Delivered`。
  2. 调用 contextbuilder（§8.2）。
  3. `turn/start`，输入为用户原文，前面加一行 `[message <message_id>, reply_to <id>]`，让模型能在工具调用里引用。记录 `main_turns(request_id, message_id, thread_id, turn_id, status)`（迁移 `0007_main_thread.sql`，同时包含 `main_threads`）。
  4. 收集 `item/completed` 中的 agent 消息，写成 `author=main_agent` 的会话消息和事件。`turn/completed` 后状态置为 completed。
- **Reconcile**：`turn/start` 发出后、响应前崩溃时，结果无法确认。B12 验证 `thread/resume` 返回的历史能否找到这条消息（按第 3 步的前缀匹配）；找得到就是 `Delivered`，找不到并且确认 thread 历史完整就是 `Retry`，其余情况 `Unknown`，在会话里标出“这条消息是否已处理未知”，由用户决定是否重发（不自动重发）。
- **thread 生命周期**：单个前台 thread，id 存在 `main_threads` 表。启动时 `thread/resume`，失败则新建并让基线失效。thread 配置采用 spike A5 的隔离设置（专用 home 和 cwd、关闭的功能列表），sandbox 按 D39 第一期使用 `danger-full-access`。
- 停止按钮等控制操作走 `control.*` 命令，不排在前台队列里（v0.1 §6.2）；本计划只实现 `turn/interrupt` 的接口，UI 在阶段 C。

### 8.4 B12b harness 接口与 traex 实现

`internal/harness`（服务端和 runner 共用）：

```go
type Session interface {            // one app-server process
    StartThread(ctx, ThreadConfig) (Thread, error)
    ResumeThread(ctx, threadID string, ThreadConfig) (Thread, error)
    Close() error
}
type Thread interface {
    ID() string
    Inject(ctx, items []json.RawMessage) error
    StartTurn(ctx, input TurnInput) (turnID string, err error)
    Interrupt(ctx, turnID string) error
    Events() <-chan Event           // items, turn lifecycle, compaction
}
type ToolHandler func(ctx, ToolCall) ToolResult   // registered in ThreadConfig
```

- `testkit` 提供 fake harness：按脚本产出 item、调用工具、修改工作区文件、模拟 compaction 和崩溃。B5–B11 的测试都用它。
- `internal/harness/traex`：
  - stdio 上的逐行 JSON 客户端（从 `spikes/appserver/rpc.go` 整理而来），处理请求/响应关联、通知，以及 server request（`item/tool/call`）。
  - 手写消息子集（spike A8 的决定）。`generated/` 下保存从 `traex app-server generate-json-schema --experimental` 抽出的相关定义、traex 版本和抽取脚本；测试校验手写结构体的字段与 schema 的 `properties`、`required` 一致。
  - 主 Agent：从专用 `TRAE_HOME`、二进制路径和模型（`TEND_TRAEX_MODEL`，默认 GPT-5.6-Sol）构造。worker：使用机器上的默认 traex 和 home（D38），二进制路径和模型可以在 runner 配置里覆盖；测试中用 `TEND_TRAEX_MODEL`。
- **worker 驱动**（runner 使用）：以工作区根目录为 cwd 启动 thread，`approvalPolicy=never`，其余配置继承机器上的用户配置（D38）。accept run 额外注册动态工具 `submit_verdict{verdict, evidence, issues}`，由 runner 实现，结论经 run token 提交。worker 的 developerInstructions 写明角色、不得修改 git 历史、accept 时不得修改被验收产物。

### 8.5 B12c 组装与故障验证

- `cmd/tend-server`：读取 HuJSON 配置和 env file，打开 store，启动 tsnet（或测试 listener）、runnerapi、dispatcher、过期扫描、mainthread。启动顺序：迁移 → 对账（`sending` 的 outbox、`built` 的 packet、过期 lease）→ 开始服务。启动后预取一次 TLS 证书（spike A9）。
- `cmd/tend-runner`：读取配置，启动 tsnet，执行 §6.1 的恢复顺序，然后进入 claim 循环。
- **spike A4 遗留的故障验证**（`integration` 标签）：工具调用执行期间杀掉 tend 一侧的连接，重启 app-server 并 resume，观察这次调用是丢失、重发还是挂起。结论写入 `docs/findings/`，并据此确定 `mainthread` 的处理方式；在有结论之前，工具副作用按“结果未知”对待，依赖 D28 和业务去重保证重放安全。
- **第一期的安全前提**（D39）：所有 agent 都是 `danger-full-access`，主 Agent 和 worker 都能读写 tend 的数据库、env file 中的密钥（server_secret、host token、tsnet auth key）和 runner 的 journal。第一期把它们视为可信（它们本来就以用户身份运行），这一点写进 design.md。账本的正确性依赖 tend 工具是唯一的写入路径（主 Agent 的 developerInstructions 写明），以及 runner 的事后检查。

## 9. 包依赖的补充

在 AGENTS.md §1.1 的基础上：

```
cmd → mainthread → app, tools, contextbuilder, harness
cmd → runnerapi  → app, scheduler, netx
cmd → dispatch   → app（handler 由 cmd 注入）
tools、contextbuilder → app、store（只读查询）
scheduler → protocol（纯函数，不依赖 store）
runner → harness, protocol, netx
```

`tools` 和 `contextbuilder` 可以直接用 `store` 的只读查询，所有写入仍然只经过 `app`。

## 10. 必须通过的测试（本阶段子集）

| 测试 | 场景（v0.1 §17.2） | 项 |
| --- | --- | --- |
| `TestFault_CrashAfterCommitBeforeDispatch` | 事务提交后、投递前进程退出：重启后用原 child request_id 投递，接收方只处理一次 | B5 |
| `TestFault_CrashDuringDispatch` | 投递中途退出：重启后先 Reconcile，不能确认的置为 unknown，不盲目重发 | B5 |
| `TestFault_ClaimReceiptLost` | runner 已领取但没收到回执：用同一 ID 重发得到同一个分配；runner 的 journal 里没有这个 run 时，hello 对账把同一个分配重新交给它，不会创建第二个 run，也不会多占 slot | B7、B8 |
| `TestFault_HeartbeatLostRunnerAlive` | 心跳中断但 runner 仍在运行：服务端把 run 置为 unknown，不把工作区分给新 run；runner 超时后自我 fencing，恢复连接后对账继续 | B7、B8 |
| `TestFault_StaleRunAfterSpecChange` | spec 升版后旧 run 仍在运行：心跳返回 stopped；之后到达的 artifact 只记为事实 | B9 |
| `TestFault_AcceptanceRunFailure` | 验收 run 自身失败：任务阻塞为 `acceptance_env_unavailable`，可以通过重试验收恢复 | B9 |
| `TestFault_DuplicateToolCall` | 主 Agent 用不同 callId、相同参数重复调用 `tasks_create`：只创建一个任务，第二次得到 `duplicate_effect` | B10 |
| `TestFault_ToolRetrySameCall` | 同一个 callId 的工具调用被重发：映射到同一个 request_id，返回原结果 | B10 |
| `TestFault_InjectFailureInvalidatesBaseline` | 注入失败或结果未知：水位不推进，下一轮注入新的完整快照 | B11 |
| `TestFault_CompactionMidTurn` | turn 中途发生 compaction：基线失效，下一轮得到完整快照，约束和待决事项不只存在于压缩摘要里 | B11、B12 |
| `TestFault_BackgroundEventKeepsFocus` | “讨论两个方案 → 收到 WPT 完成通知 → 用户说‘按第二个来’”：packet 的讨论焦点仍是方案决策，通知只出现在状态变化里 | B11 |
| `TestFault_TurnStartUnknown` | `turn/start` 发出后崩溃：Reconcile 能确认则不重发，不能确认则标为 unknown | B12 |
| `TestFault_RestoreFromOldBackup` | runner 报告服务端不存在的 run：要求 runner 停止，不重放 | B7 |
| `TestFault_WorkspaceHostAffinity` | 任务的第 2 代实现和验收在原 host 离线时都不会被其他 host 领取；原 host 回来后在同一工作区继续 | B6 |
| `TestFault_WorkerTamperedGit` | worker 自己 commit 或切换分支：run 以 `git_tampered` 失败，不产生 artifact | B8 |
| `TestFault_FetchFailureNoStaleBase` | 分配工作区时某个仓库 fetch 失败：run 以环境问题失败，不使用本地旧 ref 作为基线 | B8 |
| `TestFault_PrepareFailure` | prepare 失败：implement 进入待决事项，accept 视为 blocked，都不算实现缺陷 | B8、B9 |

此外：

- 基线解析的穷举测试（显式 commit、显式分支、候选顺序、远端 HEAD、全部缺失），以及双仓库工作区的端到端用例：只改其中一个仓库时，artifact 仍然包含两个 mount，验收按同一组合重建。
- 一个进程内端到端测试 `TestEndToEnd_FakeHarness`：消息 → 主 Agent 工具建任务 → claim → artifact → 验收 changes_required → 第 2 代实现 → 验收 passed → completed。检查全过程中 `request_records`、`runs`、`events` 没有重复。
- 一个 `integration` 测试 `TestIntegration_TraexLoop`：同样的流程，主 Agent 和 worker 都用真实 traex，在临时 git 仓库上完成一个小改动。

## 11. 实施顺序与提交拆分

每个 PR 一个或几个原子 commit，按 AGENTS.md §5 合并。

| # | 提交 | 内容 |
| --- | --- | --- |
| 1 | `docs: add phase b runtime plan` | 本文件 |
| 2 | `feat: support no-effect outcomes in command execution` | §2 |
| 3 | `feat: impl outbox dispatcher with restart reconciliation` | B5 |
| 4 | `feat: impl hosts, repos, workspaces and scheduling rules` | B6，迁移 0003，D30、D33、D34、D36 的账本部分，工作区模板展开 |
| 5 | `feat: impl runner protocol server with leases` | B7，netx，迁移 0004，D29、D32 |
| 6 | `feat: impl runner client with journal and self fencing` | B8 的 claim、心跳、fencing，fake harness，`cmd/tend-runner` |
| 6a | `feat: impl runner repo and workspace management` | B8 的 §6.5：bare clone、fetch 与基线解析、多 mount 工作区、AGENTS.md 生成、prepare、完整性检查，D35–D37；测试用本地临时 git 仓库作为 remote |
| 7 | `feat: impl acceptance retry and run failure rules` | B9，D31；进程内端到端测试的 runner 部分 |
| 8 | `feat: impl harness interface and traex client` | B12b 的协议层与 schema 一致性测试 |
| 9 | `feat: impl main agent dynamic tools` | B10，迁移 0005，D28 |
| 10 | `feat: impl context packets and baselines` | B11，迁移 0006 |
| 11 | `feat: impl main thread manager and conversation submit` | B12a，迁移 0007，更新 AGENTS.md 结构；`TestEndToEnd_FakeHarness` |
| 12 | `feat: impl traex worker driver and server assembly` | B12b worker、B12c、`cmd/tend-server`、integration 测试 |
| 13 | `docs: record tool call crash findings` | §8.5 的故障验证结论 |

第 8 步提前到工具面之前，是因为工具注册和回调的形态由 harness 接口决定；它不依赖 traex 运行，只用 schema 测试。

## 12. 之后的阶段要跟进的事项

- **template-assembler 接入（阶段 C 开头）**：prepare profile（按 LCM target 选择 DEPS）、验收工作区的预热池或复用、构建缓存目录的共享策略，以及 MR/CQ 形式的交付（新的 delivery 类型，提交带 `AutoSubmit: True` footer）。
- **LCM 管理的依赖本身需要修改时**：例如同一个任务既改 template-assembler，又改它通过 DEPS 引用的某个仓库。这时该仓库既是 mount 又是 prepare 的输出，需要让 prepare 使用 mount 中的版本，届时单独设计。
- **在已有分支上继续**：阶段 B 中指定分支只作为基线，任务总是在 `tend/<name>` 上工作。直接在别人的分支或已有 MR 上继续，和 MR 交付一起设计。
- **跨 host**：验收或后续代次可能在另一台 host 上运行时，artifact 需要先推送到远程（或在 host 之间传输），阶段 B 只支持同一 host。
- **`workspace_request` 工具**：worker 需要额外仓库时，由 runner 代为准备并记账。
- **sandbox 与权限收紧**（大概率是 auto mode）：主 Agent 和 worker 的 sandbox 或自动审批策略、限制可读范围（数据库、密钥、journal）、服务端与 agent 使用不同的系统用户，以及托管的 worker 配置（D38）。
