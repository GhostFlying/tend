# 阶段 C 计划：外挂形态的主 Agent 与 owner API（C1–C10）

**日期：2026-10-15，2026-10-16 按 D45 改写**
**依据：`docs/design-v0.1.md` §6、§8、§9.2、§14；`docs/design.md` §2–§9 与 D45、D50、D51；约定见 `AGENTS.md`。**
**前置：阶段 B 已合并（`docs/plans/phase-b-core.md`、`docs/plans/phase-b-runtime.md`）。**

阶段 B 的闭环只能在测试里驱动：没有给人用的入口。原计划补一个 Web，让 owner 在浏览器里和 tend 托管的主 Agent 对话。按 D45，托管主 Agent 往后放，本阶段先做外挂形态：用户在自己的 Claude Code、Codex 或 traex 会话里工作，tend 通过 MCP 工具和 hook 接入，这个会话就是主 Agent。本阶段要让 owner 在这三种 agent 中的任意一个里跑通 v0.1 §17 阶段 C 的闭环：提出需求 → 主 Agent 建任务 → worker 实现 → 独立验收 → completed 或待决事项，并在之后的对话里自动看到这些变化。

## 0. 范围与完成标准

- **已完成**（按原计划合并，不再改动）：
  - C1 事件与通知补齐（#27）；
  - C2 验收复用任务工作区，D41（#28）；
  - C3 读视图移入 `protocol`、JSON Schema 导出（#29）；
  - C4 owner 身份与 cookie 会话，D42（#30）。cookie 在 C6 改为 token（D49）。
- **#31（owner 写入 API 与停止回复）不合并**：通用的 owner 写入部分在 C6 重新提交；`conversation.submit_message`、`control.interrupt_reply` 和它新增的迁移 `0009` 属于托管形态，随托管一起延后，不在本阶段合入。
- **交付物**：
  - `cmd/tend`（新增）：给人和 agent 用的 CLI，含 MCP 桥和 hook 命令；
  - `internal/api` 的 owner 与 agent 接口；
  - `app`、`store`、`tools`、`contextbuilder` 的相应修改；
  - 托管主会话改为可选、默认不启用（C9）。
- **完成标准**：
  - `make check` 通过；
  - §11 列出的 `TestFault_*` 全部通过；
  - 一个进程内端到端测试经过真实的 `tend hook`、`tend mcp` 和 HTTP，从一次用户输入跑到 completed，并在下一次输入时得到包含结论的增量；
  - 真机验收（§8）：服务端和 runner 部署在宿主机上并加入 tailnet，Claude Code、Codex、traex 各跑通一个真实任务。
- **不在本阶段**：
  - Web、SSE 事件流和通知 channel（D50）；
  - 和 cove 的执行层对接（D51）；
  - worker 支持 traex 以外的 agent；
  - 人工接管、多机器调度（阶段 D）。

## 1. 需要写入 design.md 的决策（提议）

D45、D50、D51 已经写入 design.md。原计划提议的 D40（Web 工程）撤回，D43（owner API 经 `/api/v1/ops`）并入 D49。以下决策在落地的 commit 里写入。

| 编号 | 决策 | 落地 |
| --- | --- | --- |
| D46 | `tend` CLI：一个二进制，子命令包括 `login`、`mcp`（stdio MCP 桥）、`hook <event>`（hook 命令）、`setup <agent>`（输出该 agent 的 MCP 和 hook 配置），以及 owner 命令（状态、任务、待决、回答、重试、暂停与恢复、取消）。MCP 桥和 hook 命令只做转发：工具定义、上下文渲染和全部规则都在服务端，CLI 不缓存账本。MCP 只实现 stdio 下 `initialize`、`tools/list`、`tools/call` 需要的最小子集，用标准库实现，不引入 SDK。 | C8 |
| D47 | 上下文由 hook 注入（修订 D17、§2.2）：服务端按 `(agent, session_id)` 记录会话的基线和水位。SessionStart（任何来源，包括压缩之后）返回完整快照；UserPromptSubmit 在有需要提醒的变化时返回增量，否则什么都不返回（规则见 §4.3）。hook 只负责提醒 agent 账本里有什么，不参与写入：写入工具不依赖 hook，没有 hook 的 agent 也能完整使用工具。服务端返回 hook 响应即推进水位。hook 有硬超时，tend 不可达时放行用户输入，不阻塞 agent。 | C7 |
| D48 | 外挂形态下规划写入的去重（D28、§3 的第二层去重仍用于托管形态）：外挂时 tend 看不到用户消息，不使用 `(source_message_id, plan_commit_seq)` 这一层，也不引入替代它的对话身份。MCP 没有稳定的原生调用身份：`tend mcp` 为每次 `tools/call` 生成 request_id，传输失败时用同一个 ID 重试。模型重新调用工具是新的请求，由各操作已有的业务约束拦住：`tasks_create` 的工作区名唯一（agent 调用时必填），`tasks_update_intent`、`actions_create` 带 `expected_control_revision`，`actions_create` 还带代次，`decisions_resolve` 带待决事项的版本。spec 的来源记录调用方（client 和 agent 种类）以及模型给出的简短理由，不再指向某条消息。 | C7 |
| D49 | owner 与 agent 的凭据（修订 D42）：`tend login` 领取 bearer token，替代 cookie；token 带 scope（`owner` 或 `agent`）。每个请求仍然要求 WhoIs 的登录名在 `owners` 里，并且等于 token 里的登录名。不再有 cookie，因此去掉 `X-Tend-Client` 的跨站防护，client_id 从 token 里取。`agent` scope 只能调用工具面的操作。 | C6 |

## 2. C5 spike：三个 agent 的 hook 与 MCP

先用实际的二进制确认 D45 的前提，结论写进 `docs/findings/agent-hooks.md`。机器相关的信息留在 `.local/`。

对 Claude Code、Codex、traex 分别确认：

1. **配置**：stdio MCP 和 command hook 的配置位置与格式（用户级、项目级），以及是否需要开启实验开关。
2. **SessionStart**：
   - 来源取值（startup、resume、clear、compact 等）；
   - 返回的附加上下文模型是否看得到；
   - 压缩之后是否一定会触发。
3. **UserPromptSubmit**：
   - 输入里是否有 `session_id` 和用户输入的原文；
   - 返回的附加上下文模型是否看得到，放在本轮用户输入之前还是之后。
4. **失败与超时**：
   - hook 超时、非零退出、输出格式错误时，agent 的行为是放行、报错还是阻塞；
   - 超时上限能否配置。
5. **MCP 进程**：
   - 每个会话一个进程还是共享；
   - MCP 进程能否得知所属会话（环境变量或请求的 `_meta`）。这不是必需的，能拿到就记进工具调用的来源，便于排查。
   - MCP 子进程和 hook 命令是否继承 agent 进程的环境变量；不继承时，怎样在配置里转发（traex 0.209.1 的 stdio MCP 需要 `env_vars`）。
6. **大小限制**：附加上下文的长度上限，以及超过上限时截断还是丢弃。

**结论（2026-10-16）**：三个 agent 都满足接入要求，详见 `docs/findings/agent-hooks.md`。对本计划的影响已经并入 §4.2、§4.3、§5：上下文预算缩小，hook 输出统一为一种 JSON 且不使用退出码 2，`tend setup` 要处理 hook 信任、MCP 审批和环境变量转发。

如果某个 agent 不满足 D45 的接入要求，在 findings 里写明缺口，再和 owner 确认是否从支持列表中去掉。写入不依赖 hook，所以缺少 hook 只影响提醒：这样的 agent 仍可以完整使用工具，只是要靠说明（AGENTS.md 或 skill）让模型在开始工作前先调用 `tend_context`。

## 3. C6 token 与 owner API（D49）

### 3.1 token

- `POST /api/v1/session`：
  - body 是 `{client_id, scope}`；
  - 服务端用 WhoIs 确认调用方是 owner，签发 token，内容是 `{login, client_id, scope, issued_at}` 加 HMAC；
  - 派生标签和 run 凭据（D29）、旧的 cookie 都不同；有效期 30 天。
- `tend login` 一次领取 `owner` 和 `agent` 两个 token，存到 `~/.config/tend/credentials.json`（权限 0600）。
- **每个请求**：
  1. 带 `Authorization: Bearer <token>`；
  2. WhoIs 的登录名在 `owners` 里，并且等于 token 里的登录名；
  3. 按 scope 得到 Principal：
     - `owner`：`{kind: owner, subject: login, client: client_id}`；
     - `agent`：`{kind: main_agent, subject: client_id, agent: <claude|codex|traex>, thread: <session_id>}`。agent 种类由 hook 和 MCP 桥在请求里带上（桥从启动参数得知）；会话 ID 只在调用方知道时填写，只用作来源记录。MCP 桥不一定知道会话（§2），所以 `protocol.Principal` 为 main_agent 新增 `Agent` 字段，`Validate` 改为：托管来源要求 subject（harness 实例）和 thread，外挂来源要求 subject（client_id）和 agent，thread 可以为空。
- **吊销**：本阶段只能通过轮换服务端密钥整体吊销，单个 token 的吊销等需要时再加。
- **说明**：两个 token 都在同一台机器上，agent 能读到 owner token。scope 的作用是防止误操作，不是安全边界（D39：agent 视为可信）。
- **删除**：cookie、`X-Tend-Client` 校验和对应的测试。

### 3.2 owner 写入（`POST /api/v1/ops`）

请求体是信封，响应是 `{operation}` 或 `{error}`，HTTP 状态码的映射和 runnerapi 相同。白名单如下：

| 操作 | 用途 |
| --- | --- |
| `decisions.respond` | 回答待决事项 |
| `actions.create` | 待决之后重试实现或验收（D31） |
| `control.set_mode` | 暂停、恢复任务 |
| `tasks.cancel` | 取消任务 |

### 3.3 owner 读取

| 接口 | 内容 |
| --- | --- |
| `GET /api/v1/snapshot` | 未结束的任务摘要、待决事项、hosts 概况和当前的 `event_seq` |
| `GET /api/v1/tasks/{id}` | 任务详情：spec、动作、run、artifact、验收结论、待决事项、工作区的解析结果 |
| `GET /api/v1/tasks?finished=1&before=` | 已结束的任务，分页 |
| `GET /api/v1/runs/{id}/logs?after=<seq>&wait=<s>` | run 日志分页；带 `wait` 时没有新日志就长轮询，最多 25 秒 |
| `GET /api/v1/requests/{id}` | 某个请求的状态（`requests.read`） |

### 3.4 Schema

JSON Schema 的输出从 `web/src/generated/` 移到 `internal/protocol/generated/api.schema.json`。生成命令和来源写在同目录的 README 里（约束 11），删除 `web/` 目录。

## 4. C7 agent 接口（D47、D48）

### 4.1 数据

新迁移 `0009_agent_sessions.sql`：

- `agent_sessions`：`(agent, session_id)` 唯一，记录 client_id、首次和最近一次出现的时间。基线和水位沿用 `thread_baselines` 和 `context_packets`，`thread_key` 取 `<agent>:<session_id>`。

### 4.2 hook 接口

`POST /api/v1/agent/hook`，agent scope。

- body：`{request_id, agent, event, session_id, source?, prompt?}`。同一个 request_id 重试时返回同一个结果。
- **`session_start`**：
  - 作废该会话的基线，生成完整快照（角色约束、未完成的任务与承诺、待决事项索引）；
  - 快照开头写明它取代之前所有的 tend 上下文：三个 agent 的压缩摘要都会带上压缩前注入的内容（C5），模型可能同时看到新旧两份状态；
  - 返回 `{context}`，水位推进到快照的 `event_seq`；
  - 稳定的角色说明也放在这里，因为 agent 没有和 `developerInstructions` 等价的入口。
- **`user_prompt_submit`**：
  - 按 §4.3 的规则返回增量或空；
  - 基线无效时（例如服务端第一次见到这个会话）返回完整快照；
  - 不保存用户输入：hook 输入里的原文只在本次请求里使用（例如以后按内容挑选相关任务），不落库。
- **水位**：响应返回即推进，tend 无法确认 agent 把上下文交给了模型。接受这个风险，原因有两点：
  - 漏掉的变化在写入时会被 `expected_control_revision` 拦住，错误结果里带当前状态；
  - 会话恢复或压缩时，SessionStart 会重建基线。

  外挂用一步完成；托管形态原来的 Prepare/Confirm 两步保留（C9）。
- 注入内容的开头沿用 `[tend context packet <packet_id> epoch=<n> watermark=<event_seq>]`。

### 4.3 提醒的发送时机

提醒只在 agent 自己的 hook 触发时发出：tend 不能主动推送，agent 空闲时什么都不发。具体时机如下。

| 时机 | 发送内容 | 说明 |
| --- | --- | --- |
| SessionStart，任何来源（新会话、恢复、清空、压缩、fork） | 完整快照 | 账本为空时也发：角色说明和“当前没有未完成的任务”，让 agent 知道 tend 存在、该用哪些工具 |
| UserPromptSubmit，自水位以来有“需要提醒”的事件 | 增量 | 见下表 |
| UserPromptSubmit，以上都不满足 | 不发 | |
| 其他 hook（工具调用前后、Stop 等） | 不发 | 不在 agent 工作中途插入内容，也不阻止它结束 |

增量里放哪些事件：

| 类别 | 事件 | 处理 |
| --- | --- | --- |
| 需要处理 | `decision.opened`；`run.failed`、`run.unknown`、`run.lost`；结论不是 passed 的 `acceptance.submitted`；`outbox.failed`、`outbox.unknown` | 总是发，放在受保护的段落，超出预算时最后才截断 |
| 结果 | `task.completed`、`task.cancelled`；结论是 passed 的 `acceptance.submitted` | 发 |
| 账本意图的变化 | `task.created`、`task.spec_updated`、`task.priority_changed`、`task.control_changed`、`task.implement_requested`、`task.acceptance_requested`、`decision.answered` | 发，每项一行。它们可能来自另一个会话或 CLI；本会话自己写的也会出现，因为 tend 不一定知道工具调用来自哪个会话（C5 确认后可以过滤掉） |
| 进度 | `run.started`、`run.assigned`、`run.observed`、`run.released`、`run.reassigned`、`run.reconciled`、`workspace.ready`、`artifact.submitted`、日志 | 不发，需要时用工具读 |

其他规则：

- **预算**：增量最多约 4000 字符，完整快照最多约 8000 字符（C5：Claude Code 超过约 1 万字符就改为存文件、只注入预览，Codex 和 traex 默认 2,500 token）。超出时先压缩“账本意图的变化”和“结果”两类，改为“另有 N 项变化，用 `tasks_list` 查看”；“需要处理”一类不省略。
- **多个会话**：每个会话有自己的水位，互不影响。
- **过时的写入**：不依赖提醒。模型基于旧状态写入时，服务端返回 `stale_revision` 和当前状态（design.md §2.2 第 5 条）。

相比阶段 B 现有的相关事件列表（`contextbuilder.relevant`），这里去掉了 `run.started`。

未处理的待办（未回答的待决事项、受阻的任务）只在出现时提醒一次，之后不重复，除非会话重新开始或压缩。真机验收时观察模型在长会话里会不会忘记它们，再决定是否需要定期重复（§12）。

### 4.4 工具接口

- `GET /api/v1/agent/tools`：返回工具定义（名称、描述、参数的 JSON Schema），由 `tools.Specs()` 生成。
- `POST /api/v1/agent/tools/{name}`：
  - body 是 `{request_id, agent, session_id?, arguments}`；没有会话 ID 时照常执行（§3.1）；
  - 服务端用 `tools.Service` 执行，返回 `{success, content}`，格式和原来动态工具的结果一致。
- **`tools` 包的修改**：
  - `Call` 改为由调用方给出 Principal、request_id 和 arguments；托管来源的 request_id 仍由原生调用身份映射得到（D28），外挂来源用桥给的 request_id；
  - 外挂调用不要求处在某条消息的处理过程中，参数里没有 `commit_seq`（`requireMessage` 只对托管来源生效）；
  - `tasks_create` 在 agent 调用时 `workspace_name` 必填，重名时返回已有任务的 ID 和摘要，由模型判断是不是自己刚才建过的；
  - 写入工具增加可选的 `reason`（一两句话），和调用方一起记进 spec 或事件的来源；
  - 外挂调用不写 `plan_commits` 和对话绑定；托管调用（C9）保留原来的 `(source_message_id, commit_seq)` 去重，`Call` 同时支持两种来源。
- 新增只读工具 `tend_context`：返回当前的完整快照，不推进水位。给 hook 被跳过或者模型想刷新时使用。

## 5. C8 `tend` CLI（D46）

`cmd/tend` 只做参数解析和组装，逻辑放在 `internal/cli`（新增，只依赖 `protocol` 和标准库，不 import 服务端的包）。

- **`tend login [--server <url>]`**：领取两个 token，把服务端地址写进 `~/.config/tend/config.json`。
- **`tend mcp`**：
  - stdio 上的 MCP server，按启动参数得知自己服务的是哪个 agent；
  - 会话 ID：Claude Code 从环境变量 `CLAUDE_CODE_SESSION_ID` 取，Codex 和 traex 从每次 `tools/call` 的 `_meta.x-codex-turn-metadata.session_id` 取（C5）；取不到时不带（§3.1）；
  - `tools/list` 从服务端取工具定义，`tools/call` 转发；
  - 传输失败时用同一个 request_id 重试，最多 3 次；
  - 服务端明确拒绝时，把错误作为工具结果返回给模型，不当成 MCP 协议错误。
- **`tend hook <agent> <event>`**：
  - 从 stdin 读 agent 的 hook 输入，转换成 §4.2 的请求，输出 `{"hookSpecificOutput":{"hookEventName":...,"additionalContext":...}}`；三个 agent 用同一种格式（C5）；没有内容时什么都不输出；
  - 硬超时 3 秒，可以配置；agent 侧配置的 hook 超时比它长（例如 5 秒），保证由 `tend hook` 自己先放弃；
  - 超时或服务端不可达时：退出码为 0，输出一行“tend 暂时不可达”，作为附加上下文。这样不会阻塞用户输入，模型也知道写入可能失败；
  - **任何情况都不以退出码 2 退出**：三个 agent 都会因此拦下用户输入（C5）。其他错误同样以 0 退出。
- **`tend setup <claude|codex|traex>`**：
  - 打印该 agent 需要的 MCP 和 hook 配置片段，以及应该放到哪个文件；
  - 默认不改用户的配置文件，`--write` 时才写入，写入前显示 diff；
  - 各 agent 的配置内容（依据 `docs/findings/agent-hooks.md`）：
    - Claude Code：settings 里的 SessionStart 和 UserPromptSubmit hook；`claude mcp add` 添加 `tend mcp --agent claude`；permissions 放行 `mcp__tend__*`；
    - Codex / traex：`[mcp_servers.tend]` 带 `env_vars = ["TEND_ADDON"]` 和 `default_tools_approval_mode = "approve"`；两个 hook 带 `additionalContextLimit = 0`（预算由 tend 控制）；提示用户首次启动 TUI 时信任这两个 hook；
  - hook 命令保持稳定（`tend hook <agent> <event>`，不带版本号或临时路径）：Codex 和 traex 按命令的 hash 记录信任，命令一变就要重新信任。
- **owner 命令**（owner token）：
  - `tend status`：未结束的任务和待决事项；
  - `tend task <id>`、`tend logs <run_id> [-f]`；
  - `tend decide <decision_id> <option> [--note]`、`tend retry <task_id> --kind implement|accept`；
  - `tend pause|resume|cancel <task_id>`。

  默认输出给人看的文本，`--json` 输出原始结构。写命令自动带上读到的 `expected_control_revision`，遇到 `stale_revision` 时打印当前状态并退出，不自动重试。
- **worker 不接入**：worker 使用用户的默认配置，可能继承用户级安装的 tend MCP 和 hook（D38、D45），因此：
  - 环境变量 `TEND_ADDON=off` 时，`tend hook` 不发请求、不输出任何上下文、退出 0；`tend mcp` 正常完成握手，但工具列表为空；
  - runner 在启动 worker 的 app-server 时设置这个变量（`internal/harness/traex` 的 worker 启动参数）；
  - agent 不一定把自己的环境传给 MCP 子进程：traex 0.209.1 的 stdio MCP 默认不继承这个变量，配置里要写 `env_vars = ["TEND_ADDON"]` 才会转发。所以 `tend setup` 生成的 MCP 配置必须显式转发它（Codex 同样如此，Claude Code 的 MCP 默认继承）；hook 命令在三个 agent 中都继承环境（C5）；
  - 测试要走真实的进程链（app-server → MCP 子进程、app-server → hook 命令），不能只直接调用 `tend mcp` 和 `tend hook`。

## 6. C9 托管主会话改为可选

托管主 Agent 往后放，但不放弃（D45），所以已有的实现保留，只是默认不启用：

- `tend-server` 的 `main_agent` 配置改为可选。没有配置时：
  - 不启动主 harness，不注册 outbox 的 `main_thread` 目的地；
  - 对话相关的操作（`conversation.submit_message` 等）返回 `invalid_request`，错误信息说明托管主 Agent 没有启用。
- `internal/mainthread` 和相关的表保持不变，随 C7 对 `tools`、`contextbuilder` 的修改一起调整，继续编译并通过现有测试：
  - `tools.Call` 同时支持托管来源（原生调用身份、来源消息）和外挂来源（桥的 request_id）；
  - `contextbuilder` 保留托管用的 Prepare/Confirm 两步，外挂用一步。
- 托管形态本阶段不加新功能（停止回复、Web 对话等），以后恢复托管时再做。
- 更新 `AGENTS.md` 的仓库结构：去掉 `web/`，加上 `cli`，说明 `mainthread` 默认不启用；更新依赖方向。

## 7. C10 组装与端到端

- **`tend-server`**：
  - tailnet 模式下，同一个 mux 提供 `/runner/v1/*` 和 `/api/v1/*`，监听 tailnet 的 `:443`（`ListenTLS`）和 `:80`，两类接口各自鉴权；
  - `listen` 模式用一个明文 listener，owner 是 `local_owner`。
- 更新 `docs/deployment.md`：owner 配置、`tend login`、三个 agent 的 `tend setup`、ACL。
- **端到端测试 `TestEndToEnd_AgentAddon`**：进程内的服务端、fake runner，用 `internal/cli` 的真实 hook 和 MCP 代码，由一个脚本化的 fake agent 驱动：
  1. SessionStart 得到完整快照；
  2. 通过 MCP 的 `tools/call` 调用 `tasks_create`；
  3. 下一次 UserPromptSubmit 的增量里出现刚建的任务；
  4. fake runner 实现并验收；
  5. 下一次 UserPromptSubmit 得到的增量里有任务 completed。

## 8. 真机验收

最后一个 PR 合并后，和 owner 一起做，结论写进 `docs/findings/phase-c-acceptance.md`。

1. **部署**：在宿主机上构建 `tend-server`、`tend-runner` 和 `tend`，服务端和 runner 加入 tailnet，配置一个真实仓库。
2. **基本闭环**：在 Claude Code、Codex、traex 里分别执行 `tend setup` 并开一个会话，提出一个需要改代码的需求，确认：
   - 会话开始时看到账本快照；
   - 主 Agent 建出任务，`tend status` 能看到；
   - run 的进展和日志可以通过 `tend logs` 查看；
   - 验收结论到达后，下一次输入时主 Agent 自己提到这个变化；
   - artifact 的分支和 commit 在 runner 上可以查到。
3. **故障演练**：
   - 会话中途停掉服务端：用户输入不被阻塞，写入被拒绝并说明原因；恢复后继续；
   - 主动压缩会话：之后重新得到完整快照；
   - 两个会话同时修改同一任务：后到的得到 `stale_revision`；
   - 用 `tend decide` 回答一个待决事项，主 Agent 在下一次输入时看到它已解决；
   - 暂停再恢复任务。

## 9. 测试

单元测试不调用真实 agent：hook 和 MCP 的输入输出用 C5 中记录的样例做 golden 测试。

## 10. 实施顺序与提交拆分

| 步骤 | commit | 内容 |
| --- | --- | --- |
| 1 | `docs: plan the agent add-on for phase c` | 本计划；design.md 写入 D45、D50、D51 |
| 2 | `spike: verify agent hooks and mcp` | C5，`docs/findings/agent-hooks.md` |
| 3 | `feat: replace owner cookies with bearer tokens` | C6 §3.1，D49 |
| 4 | `feat: impl owner api` | C6 §3.2–§3.4，取代 #31 |
| 5 | `feat: impl agent hook and tool endpoints` | C7，D47、D48 |
| 6 | `feat: add the tend cli` | C8，D46；runner 为 worker 设置 `TEND_ADDON=off`，`tend setup` 的配置转发它 |
| 7 | `feat: make the hosted main thread optional` | C9 |
| 8 | `feat: assemble the agent add-on end to end` | C10 |
| 9 | `docs: record phase c acceptance` | §8 的结论 |

每一步一个 PR，按 AGENTS.md §5 经过 codex review。C5 的结论如果改变了 D47 或 D48，先更新本计划再继续。

## 11. 必须通过的测试（本阶段子集）

| 测试 | 场景 | 项 |
| --- | --- | --- |
| `TestFault_WorkspaceExclusive` | 同一任务的实现和验收不会同时使用工作区；unknown 的 run 也算占用 | C2（已完成） |
| `TestFault_DomainDecisionVisible` | run 失败开出的待决事项出现在快照和下一次增量里 | C1、C7 |
| `TestFault_TokenWithoutOwnerNode` | 合法 token 来自不是 owner 的 tailnet 用户：拒绝 | C6 |
| `TestFault_AgentScopeOwnerOp` | agent token 调用 owner 操作或提交验收：拒绝，不产生副作用 | C6 |
| `TestFault_ConcurrentDecisionAnswers` | 两个调用方同时回答同一待决事项：先到的生效，后到的得到 `stale_revision`；同一答案重复提交返回原结果 | C6 |
| `TestFault_StaleControlFromCLI` | 用旧的 control_revision 暂停任务，得到 `stale_revision` 和当前状态 | C6 |
| `TestFault_ToolRetrySameRequest` | MCP 桥用同一 request_id 重发 `tasks_create`，只建一个任务，返回原结果 | C7、C8 |
| `TestFault_ToolRecallSameWorkspace` | 模型用同一工作区名重新调用 `tasks_create`（新的 request_id）：不重复建任务，返回已有任务 | C7 |
| `TestFault_ToolRecallStaleAction` | 模型重新调用 `actions_create`（新的 request_id，旧的代次和版本）：得到 `stale_revision`，不重复派发 | C7 |
| `TestFault_HookServerDown` | 服务端不可达：hook 在超时内退出 0，输出提示，不阻塞输入 | C8 |
| `TestFault_ProgressNotInjected` | 只有进度事件（run 开始、心跳、日志）时，UserPromptSubmit 不返回内容；有需要处理的事件时超出预算也不省略 | C7 |
| `TestFault_HostedMainThreadDisabled` | 没有配置 `main_agent` 时服务端照常运行，对话操作被拒绝且不产生副作用；配置了时，阶段 B 的托管闭环测试仍然通过 | C9 |
| `TestFault_CompactRebuildsBaseline` | 压缩后的 SessionStart 作废基线，返回完整快照；之后的增量从新水位算起 | C7 |
| `TestFault_WorkerIgnoresAddon` | 用户级安装了 `tend setup` 生成的 MCP 和 hook 配置时，worker 的 app-server 带 `TEND_ADDON=off`：经过真实的子进程链，hook 不发请求、不注入上下文，MCP 不暴露工具（需要 traex，标 `integration`） | C8 |
| `TestFault_ToolWithoutSession` | MCP 调用不带会话 ID：照常执行，来源记录 client 和 agent 种类 | C7 |

## 12. 之后的阶段要跟进的事项

- Web（D50）：移动端访问的便利入口，带 SSE 事件流和本地发件箱，原计划 §7–§8 的设计作为出发点。
- 待办的定期重复提醒：根据真机验收和日常使用中模型的表现决定是否需要（§4.3）。
- 通知 channel：主会话不在前台时，把待决事项和验收结论推送出去。
- 恢复托管主 Agent（D45）：停止回复、对话入口等，和 Web 一起考虑。
- 和 cove 的执行层对接（D51），在 cove M1 合入之后设计。
- worker 支持 traex 以外的 agent。
- 交付：验收通过后的交付 run（D44）。
- 大仓库工作区的保留与清理策略。
- 阶段 D：人工接管、`paused` 的恢复、多机器调度。
