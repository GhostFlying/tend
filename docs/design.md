# tend：系统设计 v0.2（相对 v0.1 的调整）

**日期：2026-10-14**
**基线：《中心化 Agent 任务管理服务：系统设计 v0.1》（下称 v0.1，原文应放在 `docs/design-v0.1.md`）。**

本文只记录 v0.2 相对 v0.1 的新增决策和修订。没有提到的部分仍按 v0.1 执行，D01–D14 保持不变。
标“已确认”的是用户已同意的内容；标“待验证”的需要阶段 A spike 用实际二进制确认。

## 1. 新增决策

| 编号 | 决策 | 状态 |
| --- | --- | --- |
| D15 | 项目名 `tend`，使用独立仓库。 | 已确认 |
| D16 | （D45：托管主 Agent 延后，当前 traex 只用于 worker）主 harness 先接 traex（Codex fork），实现与协议细节参考上游 Codex 源码，以 traex 实际生成的协议为准。 | 已确认 |
| D17 | （D45、D47：外挂形态下上下文改由 agent 的 hook 注入；托管形态延后，届时仍按本条）主会话上下文以 app-server 协议注入为主：由服务端在 turn 之间通过 `thread/inject_items` 写入 developer 消息。Hook 只覆盖我们无法发起的时机（compact 之后、harness 自行恢复）。 | 已确认，接口已验证（spike A2） |
| D18 | 不做每轮注入。只有基线失效或账本发生相关变化时才注入（见 §2）。 | 已确认 |
| D19 | 执行节点一律用 pull 模式的 runner：runner 主动连控制服务，领取任务、续租、上报。控制服务不主动连节点。 | 已确认 |
| D20 | （D45：外挂形态下工具改由 MCP 提供，主 Agent 的 sandbox 由用户的 agent 决定；托管形态延后，届时仍按本条）主 Agent 唯一的写入路径是 tend 提供的工具，通过 app-server 的动态工具（`dynamicTools` / `item/tool/call`）实现，经由主会话同一条 stdio 连接回调 tend。主 harness 使用只读 sandbox，看不到应用数据库和根凭据。 | 已确认 |
| D21 | 远程访问走 tailscale。服务只监听 tailnet 地址，应用层仍保留鉴权与角色（tailscale 身份不替代角色权限）。 | 已确认 |
| D22 | 技术栈：服务端和 runner 用 Go，单个 module；Web 用 TypeScript。SQLite 用 `modernc.org/sqlite`（纯 Go，便于交叉编译）。harness 适配是 Go 包，服务端与 runner 共用。领域与 API 类型以 Go 为准，导出 JSON Schema 后生成 TS 供 Web 使用。 | 已确认 |
| D23 | 服务端和 runner 都内置 `tsnet` 加入 tailnet，不依赖设备上安装或运行 tailscaled。 | 已确认 |
| D24 | Web 采用响应式设计，以移动端为一等场景，桌面端是同一套布局的展开。 | 已确认 |
| D25 | 日常开发在 dev container 中进行；最终客户端（Electron 等桌面外壳）和 Windows、macOS 上的 runner 实机验证不在容器内。 | 已确认 |
| D26 | （已由 D39 修订）开发和早期测试阶段，主 Agent 和 worker 都运行在 Docker 中，以容器作为隔离边界，暂不依赖 traex 的 sandbox。worker 使用 `danger-full-access`；主 Agent 保留 `read-only` 设置，但不把它当作安全边界。实际部署测试时再评估各平台的 sandbox。 | 已确认 |
| D27 | 同一个用户可以同时使用多个客户端（多台设备上的浏览器，以后的 Electron 外壳）。所有客户端共享同一个入口、同一段会话和同一份账本视图，协议按多客户端并发来设计（见 §9）。 | 已确认 |
| D28 | 带原生身份的调用（harness 工具调用的 thread/turn/callId 等）不直接用原生身份当 request_id：`native_requests` 表在第一次见到某个原生身份时分配一个 `req_` ID，之后同一身份总是得到同一个 ID。映射单独一个事务写入，因为它只是身份记录：命令内部出错回滚后，重试同一个调用仍然拿到同一个 ID。 | 已确认 |
| D29 | runner 协议的凭据分两种：host 凭据（`X-Tend-Host` 加 bearer token，服务端只保存 token 的 SHA-256）用于 hello、claim、heartbeat；run 凭据（`X-Tend-Run` 加 bearer token）用于 run 自己的上报，token 由服务端密钥对 lease_id 做 HMAC-SHA256 派生，不落库，claim 重放时重新计算；lease 释放后失效。两种凭据都要求请求来自该 host 配置的 tailnet 节点。 | 已确认 |
| D30 | 仓库预先注册：服务端配置定义仓库（逻辑名、远程地址、默认基线候选），启动时同步进账本。要停止新任务挂载某个仓库、同时让已有任务做完，在配置里把它标记为 `retired`，host 仍可继续服务它；从配置中直接删掉的仓库同样变为 retired，但不再有 host 能服务它。runner 把仓库 bare clone 到 `$TEND_HOME/repos/<repo>.git`。账本只记录逻辑名和相对路径。 | 已确认 |
| D31 | MVP 没有后台推理 thread，run 的失败一律交给 owner：当前的实现 run 失败（runner 放弃、worker 崩溃、没有改动等）后，任务回到 queued 并以 `waiting_decision` 阻塞，同时开启待决事项，下一代实现需要显式请求；验收 run 自身失败等同 `blocked` 结论，任务以 `acceptance_env_unavailable` 阻塞，可以显式请求下一代验收。待决事项的回答只关闭问题，后续动作由回答方用对应命令显式发起（tend 自己开出的待决事项由 D53 修订）。 | 已确认 |
| D32 | 服务端和 runner 的配置文件使用 HuJSON（允许注释和尾随逗号的 JSON），解析用 `github.com/tailscale/hujson` 标准化后再严格解码（拒绝未知字段）。它已经是 tsnet 的间接依赖，不增加新的 module。配置里不放密钥：服务端 secret、host token 和 tailnet auth key 从环境变量读取。 | 已确认 |
| D33 | 工作区由 runner 管理：`$TEND_HOME/workspaces/<name>/` 是工作区根目录，每个仓库是其中一个子目录，worker 以工作区根目录为 cwd。工作区名称在建任务时保留（规划方可以指定，例如 `feature_a`），分支名为 `tend/<name>`。工作区跟着任务走，同一任务各代实现复用，并固定在创建它的 host 上；验收也在这台 host 上进行。 | 已确认 |
| D34 | TaskSpec 的 `workspace` 由一组 mount 组成（`repo`、`path`、`base`、`profile`），可以引用服务端配置的工作区模板，建任务时展开成显式的 mount。同一仓库在一个工作区内只能挂载一次。工作区在 host 上创建之后，spec 只能新增 mount，不能删除或修改已有 mount 的仓库和基线，也不能修改 host 要求。 | 已确认 |
| D35 | 工作区准备可插拔：checkout 之后，runner 先按各 mount 的 profile、再按工作区级 profile 执行 runner 配置中的命令（例如 template-assembler 的 `lcm sync`、`hab sync`）。profile 可以声明 `rerun_if_changed`，复用工作区时只有这些输入变化才重新执行。prepare 失败按环境问题处理，不算实现缺陷。 | 已确认 |
| D36 | 基线：mount 可以指定分支或完整 commit；不指定时由 runner 在 fetch 之后按仓库配置的候选、远端 HEAD、`main`、`master` 的顺序解析。spec 只保存意图，解析结果作为事实记在工作区上。 | 已确认 |
| D37 | 工作区根目录本身不在任何 git 仓库里，harness 不会自动加载各仓库的 AGENTS.md（Codex 只加载从 `.git` 所在根目录到 cwd 路径上的文件）。runner 在工作区根目录生成 `AGENTS.md`，写明布局（子目录、仓库、分支、基线）和工作规则，并要求在某个仓库里工作之前先读它自己的 AGENTS.md。这个文件不属于任何仓库，不进 artifact。 | 已确认 |
| D38 | worker 暂不托管：runner 直接使用所在机器上已安装的 traex 和用户的默认配置（home、鉴权、skills、plugins、用户级 AGENTS.md、默认模型），每个 run 一个 app-server 进程。runner 只覆盖无人值守必须改的项：cwd、`approvalPolicy=never`、sandbox（D39）、tend 的角色说明，以及验收用的 `submit_verdict` 工具。实现 run 的说明包含完整的 spec；后续代次还带上一次验收要求修改的内容。 | 已确认 |
| D39 | 部署形态与 sandbox（修订 D26）：服务端、主 Agent、runner 和 worker 都直接运行在各自的机器上，不在 Docker 里；dev container 只用于开发和测试。第一期主 Agent 和 worker 一律使用 `danger-full-access`，sandbox（大概率是 auto mode）后续跟进。“主 Agent 只通过 tend 工具写账本”“worker 不碰 git 历史”在第一期是约定加事后检查（runner 的完整性检查），不是强制边界；agent 视为可信。 | 已确认 |
| D41 | 验收在任务自己的工作区里进行（修订 D33 与阶段 B 计划 §6.2 的一次性验收工作区）。验收的独立性来自独立的 run 和 agent，不来自独立的工作区。同一任务任何角色的 run 还可能在使用工作区时（包括 unknown），不派发新的 run。验收 agent 只判断任务是否完成：可以构建、运行测试，但不修改代码、不清理、不提交，这只通过给验收 agent 的说明约束；tend 不在验收之后做任何清理，也不对验收后的工作区状态做承诺。验收开始前确认各 mount 停在 artifact 的分支和 commit 上，否则验收失败。 | 已确认 |
| D42 | （会话凭据已由 D49 修订：cookie 改为 bearer token）owner 身份：服务端配置 `owners` 列出 owner 的 tailnet 登录名，每个请求都用 WhoIs 取得登录名并检查。浏览器先领取会话 cookie：内容是登录名、client_id 和签发时间，用服务端密钥派生的会话密钥签名，HttpOnly、SameSite=Strict，有效期 30 天。cookie 只有和同一 owner 的 tailnet 身份一起才有效。领取会话时同样要在 `X-Tend-Client` 头里重复 client_id；写请求必须在该头里带上 cookie 中的 client_id，防止跨站请求。没有有效会话返回 `unauthenticated`（401），不是 owner 返回 `forbidden`。不加入 tailnet 的开发模式下，所有调用方都视为配置的 `local_owner`。 | 已确认 |
| D44 | 交付由 worker agent 完成：push、MR、CQ 跟进、提交信息规范等仓库特定的流程，tend 不内置，也不提供交付 profile。`deliver` 动作类型保留，具体机制（例如验收通过后的交付 run）在需要时设计。 | 已确认 |
| D45 | 主 Agent 先做外挂形态（调整 v0.1 的 D01、D09，以及 D16、D17、D20 的实施顺序）：由 tend 托管主 Agent 的工作（自己的主会话、对话入口、聊天界面）没有放弃，只是降低优先级、往后放。当前先做外挂：用户在自己选择的 agent 里工作，tend 以 MCP 工具和 hook 接入，接入了 tend 的 agent 会话就是主 Agent；这时 tend 不持有对话。已有的托管实现保留但默认不启用。支持的 agent 显式列举：Claude Code、Codex、traex；接入要求是 stdio MCP，以及 SessionStart（含压缩后的来源）和 UserPromptSubmit 两个 hook 能向模型追加上下文。推荐长期使用一个主会话，但不强制；多个会话并发写入账本，由 `expected_control_revision` 解决冲突。外挂形态下，主 Agent 的配置与 sandbox 由用户的 agent 决定，D39 中关于主 Agent 的部分不适用。主 Agent 仍然拿不到验收提交和 completed 的写入路径，由服务端按调用方身份检查。worker 仍按 D38 运行，但不能当作主 Agent：worker 使用用户的默认配置，可能继承用户级安装的 tend MCP 和 hook，因此 runner 为 worker 进程设置 `TEND_ADDON=off`，tend 的 hook 和 MCP 桥看到它时不做任何事。 | 已确认（2026-10-16）；三个 agent 的 hook 细节待阶段 C 实测 |
| D50 | Web 延后：阶段 C 不做 Web（阶段 C 计划里提议的 D40 撤回），以后作为移动端访问的便利入口，后端是 owner API；SSE 事件流随 Web 一起做。通知 channel 同样延后：只有一个主会话时，找到它的成本很低。 | 已确认（2026-10-16） |
| D51 | 与 cove 的关系：tend 是 cove 之外独立的编排服务，对应 cove 设计中预留的独立看板系统；两者通过显式接口和外部 ID 映射集成，不互写数据库，cove 不依赖 tend 运行。执行层的方向：工作区由 cove 创建，tend-runner 在 cove 管理的工作区里驱动 worker，人工接管就是在 cove 里对同一个工作区开终端。具体接口在 cove M1 合入之后设计，在此之前 runner 按 D33–D37 自己管理工作区。 | 方向已确认（2026-10-16），接口待设计 |
| D49 | owner 与 agent 的凭据（修订 D42）：`POST /api/v1/session` 为 owner 的某个客户端签发 bearer token，内容是登录名、client_id、scope 和签发时间，用服务端密钥派生的密钥签名（派生标签与 run 凭据不同），有效期 30 天。scope 是 `owner`（owner 自己的命令，例如 tend CLI）或 `agent`（owner 使用的 agent 的 hook 和工具）。每个请求带 `Authorization: Bearer`，并且 WhoIs 的登录名必须在 `owners` 里、等于 token 里的登录名；token 拿到别的设备上无效。没有 cookie 这类自动携带的凭据，因此不再需要 `X-Tend-Client` 防跨站。只需要 owner 的操作拒绝 agent token。单个 token 暂不能吊销，轮换服务端密钥会让全部 token 失效。两种 token 在同一台机器上，scope 用来防误操作，不是安全边界（D39）。 | 已确认（2026-10-16） |
| D47 | 外挂形态的上下文由 hook 注入（修订 D17、§2.2）：服务端按 `(agent, session_id)` 记录会话的基线和水位。SessionStart（任何来源，包括压缩之后）返回完整快照，开头声明取代之前所有的 tend 上下文，并附带外挂用的角色说明；账本为空时也发。UserPromptSubmit 只在自水位以来有需要提醒的事件时返回增量：需要处理的事件（新待决、run 失败或失联、未通过的验收、投递失败）总是发，超出预算也不省略；结果和账本意图的变化在预算内发；进度事件不发；本会话自己的请求直接产生的事件不发（按事件的 request_id 对应请求记录里的调用方判断，没带会话 ID 的调用不过滤）。其他 hook 不发。服务端答复即推进水位：tend 无法确认 agent 把上下文交给了模型，基于漏看状态的写入由版本校验拦下，会话恢复或压缩时重建基线。hook 不参与写入，没有 hook 的 agent 也能完整使用工具；同一 request_id 重试返回同一答复。预算：完整快照约 8000 字符，增量约 4000 字符（C5）。不保存用户输入。 | 已确认（2026-10-16） |
| D48 | 外挂形态下规划写入的去重（D28、§3 的第二层去重仍用于托管形态）：外挂时 tend 看不到用户消息，不使用 `(source_message_id, plan_commit_seq)`，也不引入替代它的对话身份。MCP 桥为每次 `tools/call` 生成 request_id，传输失败时沿用。模型重新调用工具是新的请求，由各操作已有的业务约束拦住：`tasks_create` 的工作区名唯一（agent 调用时必填，重名时返回已有任务供模型判断），`tasks_update_intent`、`actions_create` 带 `expected_control_revision`，`actions_create` 的同一代次只建一次，`decisions_resolve` 带待决事项的版本。写入可以带一句理由（`reason`），和调用方一起记在请求记录里，作为来源。 | 已确认（2026-10-16） |
| D46 | `tend` CLI：一个二进制（`cmd/tend`，逻辑在 `internal/cli`，只依赖 `protocol` 和标准库）。子命令：`login`（领取 owner 和 agent 两个 token）、`mcp --agent <agent>`（stdio MCP 桥）、`hook <agent> <event>`（hook 命令）、`setup <agent>`（显示该 agent 的 hook 和 MCP 配置的 diff，`--write` 时写入），以及 owner 命令 `status`、`task`、`logs`、`decide`、`retry`、`pause`、`resume`、`cancel`。MCP 桥和 hook 命令只做转发：工具定义、上下文渲染和全部规则都在服务端，CLI 不缓存账本（工具定义不属于账本：MCP 桥缓存最近一次取到的工具列表，tend 不可达时用它回答 `tools/list`）；MCP 只实现 stdio 上 `initialize`、`ping`、`tools/list`、`tools/call`，用标准库实现，不引入 SDK。hook 命令有硬超时（默认 3 秒，`TEND_HOOK_TIMEOUT` 可调），任何情况都以 0 退出，不可达时注入一行提示。owner 写命令带上读到的版本，遇到 `stale_revision` 打印当前状态后退出，不自动重试。配置和凭据在 `~/.config/tend/`（`TEND_CONFIG_DIR` 可改），凭据文件只有 owner 可读。`TEND_ADDON=off` 时 hook 不发请求、MCP 不暴露工具；Codex 和 traex 的 MCP 配置用 `env_vars` 转发它和 `TEND_CONFIG_DIR`（D45）。hook 读 stdin 也受同一个超时限制；MCP 桥在进程收到中断时不再等 stdin 关闭。 | 已确认（2026-10-16） |
| D52 | project 维度（提议，阶段 D 实施）：project 是 tend 自己的分类，不等于仓库，一个 project 可以涉及多个仓库，一个仓库也可以属于多个 project。任务属于一个 project；project 带默认的仓库、模板和权重。外挂会话按 hook 输入里的 cwd（或显式选择）绑定到 project，快照和增量只含该 project 的内容，需要处理的事件仍然跨 project 提醒，以减少模型在多条工作线之间的混淆。owner 可以持续往 project 里投入 backlog（issue、feature），通常在派发时给出评级。排序按规则：project 权重、评级和等待时间；调度器在有空闲的机器或 agent 时按顺序领取，不依赖后台推理（D31）。没有评级的 backlog 由 hook 提醒主 Agent 补上：外挂的主 Agent 只在用户输入时运行，派发时用户就在场。恢复托管主 Agent（D45）后可以由它接手自动排序。 | 提议（2026-10-16），owner 同意规则排序加 hook 提醒；细节在阶段 D 计划中确定 |
| D53 | 待决事项回答的效果（修订 D31）：tend 自己开出的待决事项，选项里 tend 能直接执行的在开出时带上效果——“retry the implementation”和“raise the budget”请求下一代实现，“retry acceptance”请求下一代验收，“cancel the task”取消任务；“revise the spec”等需要判断的选项不带效果，planner 开出的待决事项也不带。回答选中带效果的选项时，tend 在记录回答的同一个事务里执行效果，走和显式命令相同的领域规则。效果只作用于开出时的任务：开出时记下任务的控制版本，回答时版本变了或规则拒绝，回答照常记录，效果不执行并说明原因。回答连同效果的结果进入增量：已执行的告诉 agent 不必再做，未执行的归为 attention。这样 owner 用 `tend decide` 回答后任务立即继续，不必等 agent 会话。 | 已确认（2026-10-16） |
| D55 | 增量写明变化的来源（细化 D47）：事件写入时记下发起请求的 principal（`events.actor_json`），没有请求的事件（runner 上报、租约过期等）不记。增量每一行带上来源：owner、某个 agent 会话（种类和 session ID 前缀）、托管主 Agent；runner、run、规则和没有记录的统一写作 tend。让 agent 分清 owner 有意的操作、其他会话的操作和 tend 自己的推进。 | 已确认（2026-10-16） |

## 2. 上下文注入（修订 v0.1 §7）

> D45：当前先做的外挂形态里，上下文由各 agent 的 hook 追加，水位、完整快照与增量的规则不变，具体机制见阶段 C 计划。本节描述的 `thread/inject_items` 方式属于托管形态，随托管一起延后；记录的 app-server 能力也适用于 worker。

### 2.1 已观察到的能力（traex 0.209.1 生成的协议）

- `thread/start` 接受 `developerInstructions`、`baseInstructions`，以及按 thread 覆盖的 `config`。
- `thread/inject_items { threadId, items: JsonValue[] }`：把原始 Responses API item 追加到模型可见历史。turn 进行中也可以注入；重复注入会追加多份，app-server 不去重。
- `turn/start` 的 `input` 只有用户输入类型（text、image、skill、mention），没有 developer 角色，因此不用它来承载上下文。
- Compaction 以独立的压缩 turn 进行，其中包含 `contextCompaction` item；压缩会替换历史，之前注入的原始条目会被丢掉。traex 0.209.1 不发 `thread/compacted` 通知。Hook 事件包含 `postCompact`、`sessionStart`、`userPromptSubmit`。
- `thread/compact/start` 可以主动触发压缩，便于测试。

### 2.2 注入策略

基本前提是：模型在同一个上下文里不会轻易忘掉已经注入的事实。因此 tend 把“模型上下文中已有的账本视图”看作一条有水位的基线，只补变化的部分。

1. **建立基线（完整快照）**：thread 新建、恢复，以及检测到 compaction 之后，下一次 turn 开始前注入一份有界的完整快照：角色约束、未完成承诺、待决事项索引、近期讨论关联。稳定的角色约束放在 `developerInstructions`，不重复注入。
2. **turn 之间（按需增量）**：用户消息进入前台队列后，Context Builder 比较账本与该 thread 的已注入水位：
   - 自上次水位以来没有相关变化：**不注入**。
   - 有相关变化：注入一条简短的 delta 消息，带上新水位。相关变化包括：任务状态跨越生命周期边界、新增或已解决的待决事项、约束或优先级变更、已到达的验收结论、unknown 或受阻状态。
   - 普通进度、心跳和日志永远不注入；模型需要时通过工具读取。
3. **水位推进的条件**：只有 `thread/inject_items` 返回成功、并且随后的 `turn/start` 成功时，才推进该 thread 的水位。任一步失败，下次重算 delta，或者退回完整快照。
4. **基线失效**：观察到 `contextCompaction` item、`postCompact` hook、thread 被 harness 重建或回滚，或者 tend 无法确认历史状态时，基线作废，下一轮注入完整快照。如果 compaction 发生在 turn 中途，并且 traex 允许在 turn 进行中 inject，可以立即补注入；否则等下一个 turn 前补（待验证）。
5. **turn 进行中的状态变化**：不依赖注入来拦截。所有写入类工具都携带 `expected_control_revision`，版本不符时服务端拒绝，并在错误结果里附上当前状态摘要。这样模型在同一 turn 内就能拿到新事实。

ContextPacket 仍按 v0.1 §7.3 的内容组织。注入的 developer 消息以 `[tend context packet <packet_id> epoch=<n> watermark=<event_seq>]` 开头，便于 trace 和排查重复注入。

### 2.3 Hook 的剩余用途

- `postCompact` / `sessionStart(source=compact)`：通知 tend 基线已失效；如果 hook 支持回传 additionalContext，也可以直接补一份快照。
- Hook 进程只携带 thread 级的短期凭据回调 tend，不读全局“当前任务文件”。
- traex 的 hook 配置使用 tend 专用的 home 目录（不修改用户的 `~/.trae`），具体环境变量待验证。

## 3. 主 Agent 的工具面（细化 v0.1 §14.3）

> D45：外挂形态下工具集合不变，承载方式是 MCP，调用方身份、request_id 和去重规则见阶段 C 计划。本节描述的动态工具方式属于托管形态，随托管一起延后。

- 工具以动态工具的形式在 `thread/start` 的 `dynamicTools` 中注册（initialize 时声明 `experimentalApi`）。模型调用时，app-server 通过主会话的 stdio 连接向 tend 发出 `item/tool/call`，参数为 `{threadId, turnId, callId, namespace, tool, arguments}`；tend 返回 `{success, contentItems}`。
- 调用方身份由连接决定：只有 tend 自己启动的 app-server 进程能发起调用，tend 根据连接和 threadId 确定是哪个 harness 实例、哪个 thread，不信任 arguments 里自报的身份。
- 应用 request_id 由原生身份映射得到（D28）：来源 `harness:<harness_instance>`，键 `<threadId>/<turnId>/<callId>`。callId 当作不透明字符串使用（code mode 下是复合字符串）。
- 第二层业务去重：规划写入以 `(source_message_id, plan_commit_seq)` 唯一；run 创建以 `(action_id, generation)` 唯一（v0.1 §8.6）。
- 第一批工具（namespace `tend`，名称用下划线）：`tasks_list`、`tasks_read`、`tasks_create`、`tasks_update_intent`、`actions_create`、`decisions_open`、`decisions_resolve`、`artifacts_read`、`requests_read`、`repos_list`。主 Agent 拿不到 `acceptance.submit`，也没有 completed 的写入路径。
- 之后补上的工具：`tend_context`（C7）；`tasks_set_control`（暂停，或交还给 tend）和 `tasks_cancel`（阶段 C 预验收发现：v0.1 §14.3 的控制权操作和取消一直允许主 Agent 调用，只是没有对应的工具，owner 在 agent 里说“暂停”或者用待决事项选择“取消”时，主 Agent 无法执行）。接管（`human_control`）仍然只有 owner 能做。
- 工具执行期间 tend 崩溃或连接中断时，调用的结果如何处理还没验证，B12 做专门的故障测试。在得到结论之前，工具的副作用按“结果未知”对待，靠 request_id 和业务去重保证重放安全。

## 4. Runner 协议（细化 v0.1 §10、§12.2）

传输：runner 通过 HTTPS（tailnet）长轮询连接控制服务，凭据是 per-host token。所有请求使用 v0.1 §8.3 的信封。

| 接口 | 语义 |
| --- | --- |
| `runner.hello` | 上报 host_id、capabilities，以及**本地 journal 中仍持有的 run 列表**，用于重连对账。 |
| `runner.claim` | 长轮询领取任务。服务端在一个事务里创建或分配 run，以 `(action_id, generation)` 唯一，并返回 run_id、lease_id、lease 到期时间和不可变 spec 引用。同一 request_id 重试时返回同一个分配。 |
| `runner.heartbeat` | 续租并上报观测状态。响应中可能带期望状态（继续、停止、暂停）和是否已撤销租约。 |
| `runner.events` | 上报事实事件。event_id 由 runner 基于原生 item 身份生成，重传时不变；以 `(run_id, runner_seq)` 去重。 |
| `runner.complete` | 提交结果和 artifact 引用（实现角色）或验收结论（验收角色）。凭据是该 run 专属的、限定作用域的 token。 |

自我 fencing：runner 在本地按租约到期时间计时。超过到期时间仍没有续租成功，就先停止对工作区的写入（中断 harness turn），再停下来等待对账，不再继续自主推进。这让 v0.1 §9.3 的 fencing 由真正执行写入的一方来执行。

Runner 本地有一个 journal（SQLite），记录持有的 run、lease 和已上报的最大序号。runner 重启后先执行 `hello` 对账，然后才 `claim` 新任务。

Worker 在节点本地驱动 `traex app-server`（或测试适配器），与主 harness 共用 harness 适配包。一个任务对应一个 worktree；验收 run 使用独立 worktree，基于 artifact 引用检出。

Artifact 由 runner 生成，不由模型生成：worker 只修改工作区（sandbox 下 worktree 的 git 目录不可写），turn 完成后 runner 执行 commit，并把 `(分支, commit sha)` 作为不可变的 artifact 引用上报。runner 启动 worker 时显式配置可写根目录、网络策略和环境变量（依据 spike A7）。

## 5. 范围（细化 v0.1 §17 阶段 C）

MVP 闭环（D45，外挂形态）：用户在接入了 tend 的 agent 会话里提出需求 → 主 Agent 通过 MCP 工具创建任务与 PlanAction → 调度器派给 Linux 本机 runner → worker 产出 artifact（分支加 commit sha）→ 程序自动派独立验收 run → passed 则 completed，否则进入待决。

MVP 不做：任务范围的后台推理 thread（失败统一进入待决事项）、人工接管、导入、多机器、Orca、Electron、Web（D50）。数据模型为这些保留字段，但不实现相关流程。

## 6. 网络与身份（tsnet）

### 6.1 拓扑

- **服务端**（`tend-server`）以 tsnet 节点身份加入 tailnet，主机名默认为 `tend`。它只在 tsnet listener 上提供 Web、API 和 runner 协议，不监听宿主机的公网或局域网接口。
- **runner**（`tend-runner`）也以 tsnet 节点身份加入 tailnet，主动连接 `tend`。Windows、macOS 和容器里都不需要安装 tailscaled。
- tsnet 的 state 目录需要持久化（服务端放在数据目录下，runner 放在 journal 旁边），保证重启后节点身份不变。

### 6.2 接入凭据

- 首次加入 tailnet 使用 auth key（环境变量或一次性文件传入，用完不落盘到配置）。runner 使用带 tag 的 key（例如 `tag:tend-runner`），服务端使用 `tag:tend-server`。
- tailnet ACL 只允许：用户设备访问 `tag:tend-server` 的 443 端口；`tag:tend-runner` 访问 `tag:tend-server` 的 runner 端口。runner 之间不互通。ACL 示例放在部署文档里，不由程序修改。

### 6.3 应用层身份

- 服务端对每个请求调用 tsnet 的 WhoIs，取得调用方的节点和用户身份，用作第一层身份。
- 角色映射写在服务端配置里：哪些 tailnet 用户是 owner；哪些节点（按 node key 或 tag 加主机名）对应哪个 host_id。
- 第二层仍然使用应用凭据：runner 的 per-host token 和 run 级 token、浏览器会话。主 Agent 的工具调用走本机 stdio，不经过网络。只在 tailnet 内也不能免除角色校验（D21）。
- 浏览器会话可以基于 WhoIs 自动登录（tailnet 用户等于 owner 时签发会话 cookie），不再单独设置密码。

### 6.4 TLS

服务端通过 tsnet 获取 tailscale 签发的证书，对外提供 `https://tend.<tailnet>.ts.net`。当前 tailnet 已开启 HTTPS 证书（spike A9）。证书在首次 TLS 握手时签发，服务端启动后应主动预取一次。没开启时退回 tailnet 内的明文 HTTP（WireGuard 已经加密），但浏览器的部分能力（例如 Service Worker、通知）会受限。

## 7. Web 客户端（细化 v0.1 §14.1）

> 已由 D50 延后。以下内容保留，作为以后做 Web 时的出发点。

- 按移动优先设计。窄屏时，聊天、任务、待决三个区域用底部 tab 切换；宽屏展开成多栏（聊天为主栏，看板和待决为侧栏）。
- 待决事项在移动端是最高频的入口：按决策和审批分别展示，选项可以直接点按，提交时携带 decision_id 和版本。
- 任务详情按需展开：目标与约束、状态、下一动作、产物、验收结论。日志用流式查看，默认折叠。
- 弱网是常态：本地发件箱（IndexedDB）保存待确认的请求及其 request_id；SSE 断开后按事件游标续传；回到前台时先拉快照再接增量。
- 后续可以做成 PWA（添加到主屏、推送通知）。推送依赖 §6.4 的 HTTPS，放在 MVP 之后。
- 技术选型建议：Vite + React + TypeScript；样式方案在阶段 C 开始时决定。

## 8. 开发环境

- 仓库内提供 `.devcontainer/`：基础镜像包含 Go 工具链、Node 24 与 pnpm、sqlite3 和常用调试工具。
- traex 是静态链接的二进制，宿主机的 traex 安装目录以只读方式挂载到 `/opt/traex`。tend 使用专用的 traex home（`~/.local/share/tend/traex-home`），和工作目录一样挂载到与宿主机相同的绝对路径，并设置 `TRAE_HOME`。traex 通过 SSH 鉴权：`~/.ssh` 只读挂载，启动时复制一份可写副本。
- 基础镜像从 Docker Hub 拉取；开发机无法直连时，可以在一台能访问外网的主机上 pull，再用 `docker save | ssh | docker load` 传回。具体主机等机器相关信息记在被 ignore 的 `.local/` 中。
- 容器里的服务端用 tsnet 的 ephemeral auth key 加入 tailnet，主机名加 `-dev` 后缀（例如 `tend-dev`），这样手机可以直接访问开发实例。tsnet 是用户态网络，容器不需要 TUN 设备或特权模式。
- 测试默认不联网：tsnet 在测试中替换为本地 listener，traex 替换为 fake harness。真实 traex 和 tailnet 的集成测试单独标记，需要时显式开启。

## 9. 多客户端（D27）

- **客户端标识**：每个客户端首次启动时生成 client_id 并持久保存（浏览器存在 IndexedDB 里）。它不是凭据：会话 cookie 和 client_id 绑定，鉴权仍然依据 tailnet 身份和会话。owner 的 Principal 带有 client_id，用于审计、事件游标、本地发件箱的归属，以及以后的推送订阅。
- **去重**：owner 的所有客户端共用一个去重域。request_id 是随机生成的，不同客户端之间不会冲突。任何客户端都可以通过 `requests.read` 查看其他客户端发起的请求的状态。
- **并发写入**：不同客户端对同一对象的写入，靠 `expected_control_revision` 或 decision 的版本号解决，先提交的生效。后到的请求收到 `stale_revision` 和当前状态，客户端刷新后由用户重新决定，不会静默覆盖。
- **会话**：所有客户端的消息进入同一个前台串行队列，按服务端分配的会话序号排序。每条消息带上发送方客户端的 `last_seen_conversation_seq` 和 `reply_to`，消息与任务的关联依据该客户端当时实际看到的内容（v0.1 §6.2）。
- **事件同步**：服务端把事件广播给所有在线的客户端，每个客户端用自己的游标续传。一个客户端的操作结果通过事件流同步到其他客户端，而不是只返回给发起方。
- **待决事项**：在一个客户端回答之后，其他客户端立即看到它已解决。用同一版本、同一答案重复回答，返回已有的结果；版本已经变化的回答，返回 `stale_revision`。
