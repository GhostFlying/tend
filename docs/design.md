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
| D39 | 部署形态与 sandbox（修订 D26）：服务端、主 Agent、runner 和 worker 都直接运行在各自的机器上，不在 Docker 里；dev container 只用于开发和测试。第一期主 Agent 和 worker 一律使用 `danger-full-access`，sandbox（大概率是 auto mode）后续跟进。“主 Agent 只通过 tend 工具写账本”“worker 不碰 git 历史”在第一期是约定加事后检查（runner 的完整性检查），不是强制边界；agent 视为可信。（“worker 不碰 git 历史”与完整性检查已由 D57 取消。） | 已确认 |
| D41 | 验收在任务自己的工作区里进行（修订 D33 与阶段 B 计划 §6.2 的一次性验收工作区）。验收的独立性来自独立的 run 和 agent，不来自独立的工作区。同一任务任何角色的 run 还可能在使用工作区时（包括 unknown），不派发新的 run。验收 agent 只判断任务是否完成：可以构建、运行测试，但不修改代码、不清理、不提交，这只通过给验收 agent 的说明约束；tend 不在验收之后做任何清理，也不对验收后的工作区状态做承诺。验收开始前确认各 mount 停在 artifact 的分支和 commit 上，否则验收失败（D57 起分支可以为空，即 detached HEAD）。 | 已确认 |
| D42 | （会话凭据已由 D49 修订：cookie 改为 bearer token）owner 身份：服务端配置 `owners` 列出 owner 的 tailnet 登录名，每个请求都用 WhoIs 取得登录名并检查。浏览器先领取会话 cookie：内容是登录名、client_id 和签发时间，用服务端密钥派生的会话密钥签名，HttpOnly、SameSite=Strict，有效期 30 天。cookie 只有和同一 owner 的 tailnet 身份一起才有效。领取会话时同样要在 `X-Tend-Client` 头里重复 client_id；写请求必须在该头里带上 cookie 中的 client_id，防止跨站请求。没有有效会话返回 `unauthenticated`（401），不是 owner 返回 `forbidden`。不加入 tailnet 的开发模式下，所有调用方都视为配置的 `local_owner`。 | 已确认 |
| D44 | 交付由 worker agent 完成：push、MR、CQ 跟进、提交信息规范等仓库特定的流程，tend 不内置，也不提供交付 profile。`deliver` 动作类型保留，具体机制（例如验收通过后的交付 run）在需要时设计。 | 已确认 |
| D45 | （D68：阶段 F 恢复托管主 Agent，与外挂并存。）主 Agent 先做外挂形态（调整 v0.1 的 D01、D09，以及 D16、D17、D20 的实施顺序）：由 tend 托管主 Agent 的工作（自己的主会话、对话入口、聊天界面）没有放弃，只是降低优先级、往后放。当前先做外挂：用户在自己选择的 agent 里工作，tend 以 MCP 工具和 hook 接入，接入了 tend 的 agent 会话就是主 Agent；这时 tend 不持有对话。已有的托管实现保留但默认不启用。支持的 agent 显式列举：Claude Code、Codex、traex；接入要求是 stdio MCP，以及 SessionStart（含压缩后的来源）和 UserPromptSubmit 两个 hook 能向模型追加上下文。推荐长期使用一个主会话，但不强制；多个会话并发写入账本，由 `expected_control_revision` 解决冲突。外挂形态下，主 Agent 的配置与 sandbox 由用户的 agent 决定，D39 中关于主 Agent 的部分不适用。主 Agent 仍然拿不到验收提交和 completed 的写入路径，由服务端按调用方身份检查。worker 仍按 D38 运行，但不能当作主 Agent：worker 使用用户的默认配置，可能继承用户级安装的 tend MCP 和 hook，因此 runner 为 worker 进程设置 `TEND_ADDON=off`，tend 的 hook 和 MCP 桥看到它时不做任何事。 | 已确认（2026-10-16）；三个 agent 的 hook 细节待阶段 C 实测 |
| D50 | （D70：阶段 F 做 Web，通知仍延后。）Web 延后：阶段 C 不做 Web（阶段 C 计划里提议的 D40 撤回），以后作为移动端访问的便利入口，后端是 owner API；SSE 事件流随 Web 一起做。通知 channel 同样延后：只有一个主会话时，找到它的成本很低。 | 已确认（2026-10-16） |
| D51 | 与 cove 的关系：tend 是 cove 之外独立的编排服务，对应 cove 设计中预留的独立看板系统；两者通过显式接口和外部 ID 映射集成，不互写数据库，cove 不依赖 tend 运行。执行层的方向：工作区由 cove 创建，tend-runner 在 cove 管理的工作区里驱动 worker，人工接管就是在 cove 里对同一个工作区开终端。具体接口在 cove M1 合入之后设计，在此之前 runner 按 D33–D37 自己管理工作区。 | 方向已确认（2026-10-16），接口待设计；工作区与 worker 的分层见 D63 |
| D49 | owner 与 agent 的凭据（修订 D42）：`POST /api/v1/session` 为 owner 的某个客户端签发 bearer token，内容是登录名、client_id、scope 和签发时间，用服务端密钥派生的密钥签名（派生标签与 run 凭据不同），有效期 30 天。scope 是 `owner`（owner 自己的命令，例如 tend CLI）或 `agent`（owner 使用的 agent 的 hook 和工具）。每个请求带 `Authorization: Bearer`，并且 WhoIs 的登录名必须在 `owners` 里、等于 token 里的登录名；token 拿到别的设备上无效。没有 cookie 这类自动携带的凭据，因此不再需要 `X-Tend-Client` 防跨站。只需要 owner 的操作拒绝 agent token。单个 token 暂不能吊销，轮换服务端密钥会让全部 token 失效。两种 token 在同一台机器上，scope 用来防误操作，不是安全边界（D39）。 | 已确认（2026-10-16） |
| D47 | 外挂形态的上下文由 hook 注入（修订 D17、§2.2）：服务端按 `(agent, session_id)` 记录会话的基线和水位。SessionStart（任何来源，包括压缩之后）返回完整快照，开头声明取代之前所有的 tend 上下文，并附带外挂用的角色说明；账本为空时也发。UserPromptSubmit 只在自水位以来有需要提醒的事件时返回增量：需要处理的事件（新待决、run 失败或失联、未通过的验收、投递失败）总是发，超出预算也不省略；结果和账本意图的变化在预算内发；进度事件不发；本会话自己的请求直接产生的事件不发（按事件的 request_id 对应请求记录里的调用方判断，没带会话 ID 的调用不过滤）。其他 hook 不发。服务端答复即推进水位：tend 无法确认 agent 把上下文交给了模型，基于漏看状态的写入由版本校验拦下，会话恢复或压缩时重建基线。hook 不参与写入，没有 hook 的 agent 也能完整使用工具；同一 request_id 重试返回同一答复。预算：完整快照约 8000 字符，增量约 4000 字符（C5）。不保存用户输入。（只对用过 tend 工具的会话，见 D54） | 已确认（2026-10-16） |
| D48 | 外挂形态下规划写入的去重（D28、§3 的第二层去重仍用于托管形态）：外挂时 tend 看不到用户消息，不使用 `(source_message_id, plan_commit_seq)`，也不引入替代它的对话身份。MCP 桥为每次 `tools/call` 生成 request_id，传输失败时沿用。模型重新调用工具是新的请求，由各操作已有的业务约束拦住：`tasks_create` 的工作区名唯一（agent 调用时必填，重名时返回已有任务供模型判断），`tasks_update_intent`、`actions_create` 带 `expected_control_revision`，`actions_create` 的同一代次只建一次，`decisions_resolve` 带待决事项的版本。写入可以带一句理由（`reason`），和调用方一起记在请求记录里，作为来源。 | 已确认（2026-10-16） |
| D46 | `tend` CLI：一个二进制（`cmd/tend`，逻辑在 `internal/cli`，只依赖 `protocol` 和标准库）。子命令：`login`（领取 owner 和 agent 两个 token）、`mcp --agent <agent>`（stdio MCP 桥）、`hook <agent> <event>`（hook 命令）、`setup <agent>`（显示该 agent 的 hook 和 MCP 配置的 diff，`--write` 时写入），以及 owner 命令 `status`、`task`、`logs`、`decide`、`retry`、`pause`、`resume`、`cancel`。MCP 桥和 hook 命令只做转发：工具定义、上下文渲染和全部规则都在服务端，CLI 不缓存账本（工具定义不属于账本：MCP 桥缓存最近一次取到的工具列表，tend 不可达时用它回答 `tools/list`）；MCP 只实现 stdio 上 `initialize`、`ping`、`tools/list`、`tools/call`，用标准库实现，不引入 SDK。hook 命令有硬超时（默认 3 秒，`TEND_HOOK_TIMEOUT` 可调），任何情况都以 0 退出，不可达时注入一行提示。owner 写命令带上读到的版本，遇到 `stale_revision` 打印当前状态后退出，不自动重试。配置和凭据在 `~/.config/tend/`（`TEND_CONFIG_DIR` 可改），凭据文件只有 owner 可读。`TEND_ADDON=off` 时 hook 不发请求、MCP 不暴露工具；Codex 和 traex 的 MCP 配置用 `env_vars` 转发它和 `TEND_CONFIG_DIR`（D45）。hook 读 stdin 也受同一个超时限制；MCP 桥在进程收到中断时不再等 stdin 关闭。 | 已确认（2026-10-16） |
| D52 | project 维度（阶段 D 实施，细节见 D58–D60 与 `docs/plans/phase-d.md`）：project 是 tend 自己的分类，不等于仓库，一个 project 可以涉及多个仓库，一个仓库也可以属于多个 project。任务属于一个 project；project 带默认的仓库、模板和权重。外挂会话按 hook 输入里的 cwd（或显式选择）绑定到 project，快照和增量只含该 project 的内容，需要处理的事件仍然跨 project 提醒，以减少模型在多条工作线之间的混淆。owner 可以持续往 project 里投入 backlog（issue、feature），通常在派发时给出评级。排序按规则：project 权重、评级和等待时间；调度器在有空闲的机器或 agent 时按顺序领取，不依赖后台推理（D31）。没有评级的 backlog 由 hook 提醒主 Agent 补上：外挂的主 Agent 只在用户输入时运行，派发时用户就在场。恢复托管主 Agent（D45）后可以由它接手自动排序。 | 已确认（2026-10-21）：排序按分数（D60），不按机器份额 |
| D53 | 待决事项回答的效果（修订 D31）：tend 自己开出的待决事项，选项里 tend 能直接执行的在开出时带上效果——“retry the implementation”和“raise the budget”请求下一代实现，“retry acceptance”请求下一代验收，“cancel the task”取消任务；“revise the spec”等需要判断的选项不带效果，planner 开出的待决事项也不带。回答选中带效果的选项时，tend 在记录回答的同一个事务里执行效果，走和显式命令相同的领域规则。效果只作用于开出时的任务：开出时记下任务的控制版本，回答时版本变了或规则拒绝，回答照常记录，效果不执行并说明原因。回答连同效果的结果进入增量：已执行的告诉 agent 不必再做，未执行的归为 attention。这样 owner 用 `tend decide` 回答后任务立即继续，不必等 agent 会话。 | 已确认（2026-10-16） |
| D54 | 只给用过 tend 的会话注入上下文（修订 D47）：会话第一次通过 MCP 调用 tend 的工具（带有它的 session ID）时绑定，绑定是幂等的记录，不产生事件。未绑定会话的 hook 什么都不返回，tend 也不记录它（不建基线、不写 packet、不留请求记录），这样 agent 的后台会话（如 Codex 的记忆整理）、子会话和与 tend 无关的会话都不会收到账本内容。已绑定的会话按 D47 处理，绑定后第一次 hook 返回完整快照。绑定时记下当时的账本位置；完整快照只列未结束的任务，所以对有水位的会话（绑定之后、压缩之后），完整快照同时附上水位之后结束的任务和被回答的待决事项，不会漏掉会话还没听说、快照又显示不出的变化。新会话从 MCP 的工具描述和 `initialize` 的 instructions 得知 tend，需要时先调用 `tend_context`。迁移把已有会话记为已绑定。 | 已确认（2026-10-16） |
| D55 | 增量写明变化的来源（细化 D47）：事件写入时记下发起请求的 principal（`events.actor_json`），没有请求的事件（runner 上报、租约过期等）不记。增量每一行带上来源：owner、某个 agent 会话（种类和 session ID 前缀）、托管主 Agent；runner、run、规则和没有记录的统一写作 tend。让 agent 分清 owner 有意的操作、其他会话的操作和 tend 自己的推进。 | 已确认（2026-10-16） |
| D56 | 指定的 tagged 设备也算 owner 设备（细化 D49）：tagged 节点在 tailnet 里没有登录名，owner 若在 tagged 的开发机上运行 agent 和 tend CLI，就无法按登录名认出。服务端配置 `owner_nodes` 把机器名或 `tag:` 映射到 `owners` 里的一个登录名，来自匹配节点的请求视为这个 owner；仍要 `tend login` 领取 token，token 的登录名必须一致。映射到不在 `owners` 里的登录名时拒绝启动。优先写机器名，一个 tag 可能覆盖别人的机器。 | 已确认（2026-10-16） |
| D57 | 提交归 worker（修订 D39、D41 与阶段 B 的完整性检查）：要不要提交、怎么写提交信息、分几个提交，由 worker 按任务包和各仓库自己的规范决定；主 Agent 通过任务包表达要求，不直接干预；tend 不提交、不评判提交，也不限制 worker 怎么用 git（提交、改写历史、切分支、改配置、建 worktree 都不禁止，实现 run 不再有 `git_tampered`）。将来如有需要，可以作为主 Agent 给单个任务设置的限制。run 结束时 runner 如实记下每个 mount：HEAD 所在的分支、HEAD，以及工作树有未提交改动时的快照（以 HEAD 为父、存在 `refs/tend/runs/` 下的 commit 对象，不移动分支、不改 index 和文件）。HEAD 或工作树和上一次记录不同才算有变化。验收开始前核对分支和 HEAD，这是核对 tend 自己的记录，不符时验收以 `workspace_moved` 失败；未提交的文件不核对，因为 prepare profile 会重新生成文件，D41 也允许验收留下构建产物。runner 不推送；任务要求推送或开 MR 时由 worker 完成。 | 已确认（2026-10-17） |
| D58 | project 在服务端配置里定义（`projects`：名称、标题、权重、涉及的仓库、默认工作区模板），和仓库、host 一样在启动时同步进账本；从配置删掉的 project 变为 retired，已有任务照常进行，不能再建新任务。没有配置 project 时，tend 使用隐含的 `default` project，包含全部仓库，权重 1；升级前的任务归入它。任务属于一个 project：创建时给出，或者在只有一个活动 project 时默认用它；`tasks_update_intent` 可以修改（校验版本，产生事件）。没给工作区的任务使用 project 的默认模板。放在配置里而不是由工具创建，因为 project 很少变，权重是 owner 对资源的分配，不应由 agent 修改。 | 已确认（2026-10-21） |
| D59 | 会话绑定 project（细化 D52、D54）：`tend hook` 和 `tend mcp` 读出会话所在 git 仓库的远程地址（hook 用输入里的 `cwd`，没有时用进程的工作目录；MCP 桥用启动时的工作目录），随请求发给服务端，不发送本地路径。服务端把地址规范化（去掉 scheme、用户、端口和 `.git`，转小写）后与注册仓库比对，包含这个仓库的活动 project 恰好一个时自动绑定，没有或有多个时不绑定；请求里没有地址（旧版 CLI、git 失败）时保持原绑定。会话可以用 `session_project` 显式选择一个 project 或全部，选择持续到会话结束，优先于自动绑定。绑定变化后，下一次 hook 返回完整快照；有多个 project 时，完整快照写明当前绑定和切换方式。会话未指定 project 时，`tasks_create` 默认用会话的 project。绑定是记录，不产生领域事件；显式选择经请求去重。上下文按绑定的 project 划分：完整快照只列它的未结束任务和待决事项，另附其他 project 的数量汇总；增量和完整快照里“上次之后”的部分只含它的事件，其他 project 只告诉需要处理的事件（D47 的 attention 类），单独成节，每行标明 project；不涉及任务的事件对所有会话可见。未绑定的会话看到全部，有多个 project 时每行标明 project。`tasks_list` 默认列会话的 project，`"*"` 列全部。写入不受绑定限制。 | 已确认（2026-10-21） |
| D60 | 评级取代整数优先级（细化 D52）：任务带评级 `rating`，`p0` 紧急、排在一切之前，`p1` 高，`p2` 普通，`p3` 低，也可以为空（未评级，排序时按 `p2`，不阻塞执行）。迁移把原来 `priority > 0` 记为 `p1`、`= 0` 记为 `p2`、`< 0` 记为 `p3`，之后不再读写 `priority` 列。`tasks_create` 和 `tasks_update_intent` 接受 `rating`；评过的任务不能改回未评级，改评级产生 `task.rating_changed` 事件。未评级的提醒：`tasks_create` 没带评级时结果里附一条提示，让主 Agent 向 owner 确认（派发时 owner 在场）；完整快照把会话所在 project 里未评级的未结束任务单列一节；增量里别人新建且仍未评级的任务单列一节。owner 用 `tend rate <task_id> <p0..p3>` 直接评级（owner API 的 `tasks.rate`，只改评级，不放开规划类写入），带读到的版本。排序：`p0` 先行，按等待时间；其余按分数，分数 = project 权重 × 评级分（`p1` 3、`p2` 2、`p3` 1）+ 等待小时数 × `queue.wait_per_hour`（默认 0.1）；同分时验收先于实现，再按等待时间和动作 ID；正在运行的工作不被抢占。 | 已确认（2026-10-21）；评级与分数排序已落地（W4、W5） |
| D61 | run 续接 worker thread（细化 D33、D41）：runner 在 worker 的 thread 建立后立即把 thread ID 记进 journal。同一个 run 的下一次尝试（runner 重启后 tend 让它继续、租约恢复，或暂停后恢复）先用 `thread/resume` 接上原 thread，再发一个短 turn：执行被中断，工作区保持中断时的状态，检查后继续原来的任务；验收 run 同样续接，并被提醒中断前的 `submit_verdict` 调用已经丢失、需要重新调用。resume 失败时（rollout 不存在、traex 版本不兼容等）退回新 thread 加完整 brief（带“之前的尝试被中断”的说明），记一条 info，并把新 thread 记进 journal。只在同一个 run 内续接：新代次和验收是新的 run，总用新 thread，brief 已经包含需要的信息。 | 已确认（2026-10-21） |
| D62 | 工作区所在 host 不可用（细化 D33、D53）：任务的工作区只在一台 host 上，工作只能在那里继续。服务端每分钟检查一次：工作区所在 host 已退役，或超过 `host_lost_after`（默认 24 小时，`0` 关闭失联判定）没有消息，且任务受托管、没有其他阻塞、有待执行的实现或验收动作、或者在那台 host 上有状态未知的 run（执行中途 host 不可用）时，任务以 `workspace_unavailable` 阻塞并开出待决事项：等 host 回来（无效果）、在别的 host 上重来（效果 `start_over`）、取消。在线以 hello、心跳和领取为准，空闲 runner 靠长轮询领取保持在线。host 回来后同一个检查解除阻塞，并以 tend 的名义回答还开着的这类待决事项；host 仍不可用而这类待决事项因任务变过而失效、又没有在当前版本问过时，再问一次。重来时实现代次加一，任务回到排队，验收中的产物和未完成的动作作废；旧工作区记入 `abandoned_workspaces`，新工作区不能建在放弃了工作区的 host 上（那里的目录和任务分支还在，会被接着用），旧 host 上的目录不动。旧工作区里的 run 不能再报告工作区。工作区占用只看当前工作区 host 上未结束的 run。 | 已确认（2026-10-21） |
| D63 | worker 形态与工作区分层（细化 D51，限定 D38、D41 的适用范围）：分两层考虑。**工作区层**：由谁建工作区、管多仓库、分支和基点。现在由 tend runner 自己建（D33–D37）；接入 cove 后由 cove 建，一个 cove task 带多个挂载（托管 worktree 或已有 checkout），与 tend 的 mount 对应，tend 只记录建成后的事实，准备命令（profile）仍由 runner 执行。**worker 层**：worker 在哪里运行、怎么被接管。有 cove 后，worker 是 cove tab 里的 agent TUI，人在 cove 里取得这个终端的控制权就是接管，tend 看到控制者变化后停止自动干预；只有这样 cove 才能真正接手。在 cove 支持之前，用 orca 临时承载：由 orca 建 worktree（只支持单仓库），在其中启动 agent TUI，人在 orca（桌面或手机）里操作同一个终端即接管。tend 内部按“工作区提供方”和“终端 worker”两个接口实现，orca 和 cove 是两种实现，traex app-server 加 tend 自建工作区（D38、D41）是第三种。**harness 显式选择，能力向上影响 tend**：服务端配置为每台 host 指定 harness，tend 不替换、不回退。每种 harness 声明自己的能力（如是否支持多仓库工作区）。能力决定 tend 本身的行为：没有任何活动 host 支持某项能力时，需要它的写入（如多个 mount 的 `tasks_create`、多仓库模板）被拒绝并说明原因，快照和工具说明写明这项限制；只有部分 host 支持时，任务只由这些 host 领取，和 OS、标签的匹配一样，`tend status` 可见原因。多仓库能力因此随 harness 打开或关闭：用 orca 时没有，换成 cove 后恢复。TUI worker 通过 tend 的 MCP 工具（worker 模式，按 run 鉴权）交付产物和验收结论，不再依赖 app-server 的动态工具。接入 cove 依赖它的计划接口：事件与等待（含控制者变化）、`tab get` 显示控制者、`tab send`、配方环境变量与每次 run 的临时值、有版本的 CLI 契约。 | 方向已确认（2026-10-21）；orca 的接入见 D64–D67 |
| D64 | harness 能力的落地（细化 D63）：服务端配置为每台 host 写明 `worker`（`appserver` 默认，或 `orca`）。能力表由 tend 定义在 `protocol` 里：`appserver` 支持多仓库工作区；`orca` 只支持单仓库，可接管。runner 配置决定实际启动的 harness，hello 和每次领取时报告，与服务端配置不符时被拒绝并说明原因（重发的领取同样检查），不按任何一方运行。有活动 host 而都不支持多仓库时，多个 mount 的 `tasks_create`、`tasks_update_intent` 和多仓库模板以 `invalid_request` 拒绝，快照与工具说明写明限制；部分 host 支持时，调度跳过不支持的 host 并给出原因。已建的多仓库任务保留已有的 mount，只在没有合适 host 时等待，但不能再加 mount。这项限制出现或消失时，会话的基线失效，下一次拿到完整快照。 | 已确认（2026-10-21） |
| D65 | 工作区提供方记录自己建出的分支（修订 D33 的分支命名）：tend 自建的工作区仍用 `tend/<工作区名>`；orca 的 worktree 用 orca 的命名。服务端在第一次工作区报告时记录分支，之后同一工作区的报告必须与之一致。这只是工作区的准备事实：产物照旧记录 worker 实际的分支和 HEAD，worker 换分支或 detach 仍按 D57 处理。orca 的仓库由 owner 事先加入 orca，runner 配置把 tend 的仓库映射到 orca 的仓库；tend 只建和删自己的 worktree，不登记、不移除仓库。 | 已确认（2026-10-21） |
| D66 | orca 的 TUI worker（细化 D41、D61）：runner 用 `orca agent launch` 在 worktree 里启动 agent TUI（Codex、traex），启动前先在 journal 记下这次启动和写进 brief 的标记；回执丢失时按 orca 给出的操作 ID 重放；runner 在启动途中退出时先按标记在 worktree 里找回 agent，找不到才在确认后重新启动，不因没收到回执就重发；终端 handle 记进 journal。启动结果必须是终端（orca 的 Chat UI 对该 agent 关闭），否则 run 以明确的错误失败。worker 经 runner 本机的 MCP 桥交付：runner 为每个 run 开一个本机 socket，`tend-runner worker-mcp` 作为 agent 的项目级 MCP server 把 `submit_verdict` 等工具转给 runner，服务端协议不变；这份项目级配置同时写入 D38 要求的无人值守设置（approval never、`danger-full-access`、tend 的工具免批准，以及不在启动时检查更新），加入 `git rev-parse --git-path info/exclude` 给出的排除文件；token 放在 worktree 自己的 git 目录下，并绑定到 run，上一个 run 的 agent 不能调用后来的 run 的工具。一轮结束以 orca 的空闲判断和 agent 状态为准。暂停时发 Esc 中断当前一轮、保留终端，恢复时发一轮继续的说明；fencing 和 runner 退出同样只发 Esc，下一次尝试找回同一个终端续接（agent 仍在这一轮里就只等它结束）；取消时只关闭 tend 启动的终端；runner 放弃一个没有结果的 run 时（包括暂停后取消、worker 失败），即使没有 worker 在跑，也先按 journal 关闭它的终端再 release。找回 agent 所需的状态（标记、操作 ID、终端、最近一轮的开始时间）作为 run 的 thread 记进 journal。上传的 run 日志只有 agent 状态的变化、每一轮的提示和最后一条回复，完整过程在 orca 里看。 创建终端与 brief 投递分别确认：仅明确 handed-to-terminal 启动回执记录 turn.started；not-delivered 的否定事实连同终端身份持久保存并立即失败，恢复不再启动或补发。仅该否定事实允许在原 handle/pane/worktree 一致且没有当前 agent 活动时关闭本次空终端，清理后读回确认；否定回执同时保存本机接收时间，允许不晚于该时间且无输入、工具及输出证据的 SessionStart 状态，后续活动或缺时间依据的状态仍保留，重启不重置此边界。未知或缺字段回执不获得此证明，也不重发；但控制取消时若原终端身份、brief 与 turn 状态已证明仍属本 run，则执行既有 fencing 中断或取消关闭，不能因未知回执跳过停止。 | 已确认（2026-10-21） |
| D67 | 经 orca 的人工接管（细化 D51、D63）：runner 记下自己发出的每一轮（提示的开头、开始时间、自己是否发过 Esc）；出现一轮不是 tend 发起的工作（提示不同，或 tend 没发 Esc 而这一轮被中断），即视为 owner 接管，runner 用 `runs.report_takeover` 报告（事实，不带控制版本），当前的 run 使任务转为 `human_control` 并产生需要处理的 `task.taken_over` 事件；owner 也可以用 `tend takeover` 显式接管。`human_control` 下 run 的 desired 是 `held`（不同于暂停的 `paused`）：接管期间 runner 不判断完成、不收集、不发输入，终端保持原样，租约照常续期。owner 用 `tend resume` 交还后，worker 收到一轮“owner 处理过，检查工作区后完成任务”，之后照常结束、收集、验收。接入 cove 后，察觉方式换成 cove 的控制者变化。 | 已确认（2026-10-21） |
| D68 | 恢复托管主 Agent（修订 D45 的优先级、D20 的只读运行与 D39 中主 Agent 的部分）：服务端运行托管主会话，与外挂主 Agent 并存。traex app-server 使用服务端所在机器上 owner 的 traex home（可以另行指定），进程设置 `TEND_ADDON=off`，避免托管会话把自己当作外挂会话；sandbox `workspace-write`，`approvalPolicy = on-request`，需要提权的操作由 `approvalsReviewer = auto_review` 自动审查，不等人确认。托管会话看所有 project，只拿托管形态的工具。不设单一控制权：初版托管主 Agent 只在 owner 发消息时工作；运行权限与事件驱动轮次由 D88 修订，写入仍有版本校验（阶段 F 计划 §2）。 | 已确认（2026-10-22） |
| D69 | 会话的读取与事件流（细化 D27，落实 v0.1 §14.2）：会话记录包括 owner 的消息、主 Agent 的回复和它的工具调用，共用一个顺序，owner API 按顺序分页读取，消息带上状态（已收下、正在处理、已投递、执行状态未知、被停止）。事件流是 owner API 上的 HTTP 流式响应，推送账本事件，按 `event_seq` 续传；客户端用 `fetch` 读取，以便带 bearer token。事件只通知变化，客户端重新读视图。主 Agent 的正文按 D74 累计发布并保留部分回复；原先不做逐字流式的限定由 D74 修订。停止回复是 owner 控制命令 `conversation.interrupt_reply`；客户端持久化完整请求，HTTP 回执丢失时先查询原请求，无记录才沿用原 request_id 恢复投递（D11）。已记录的 accepted、succeeded 或业务 unknown 只对账、观察消息终态，不创建新中断或重放外部副作用（阶段 F 计划 §3、§4）。 | 已确认（2026-10-22） |
| D70 | Web 客户端（修订 D50）：手机优先，同时适配桌面，可以装成 PWA；一个对话加任务概览和任务详情，没有会话列表。代码在仓库根目录的 `web/`，由 tend-server 嵌入并在 `/` 提供，与 owner API 同源；登录沿用 D49：页面按 tailnet 身份领取 owner token。发出的消息先进浏览器的持久队列，带持久的 request_id，重发沿用原 ID。界面是中文。通知继续延后（阶段 F 计划 §5、§6）。 | 已确认（2026-10-22） |
| D71 | 在 Web 里回答待决事项（细化 D53）：选中带效果的选项时直接 `decisions.respond`，tend 在同一事务里执行效果；选中不带效果的选项（包括主 Agent 开出的所有选项）时，Web 代 owner 发一条关联这条待决事项和所选选项的消息，由主 Agent 记录回答并接着处理，避免回答之后没有人动手（阶段 F 计划 §4）。 | 已确认（2026-10-22） |
| D72 | 回复里的任务引用：主 Agent 提到任务时带上任务 ID；Web 把识别出的 ID 显示为带当前标题和状态的标签，账本里查不到的原样显示。不要求模型使用特殊语法，标签的状态总是账本当前的状态（阶段 F 计划 §6）。 | 已确认（2026-10-22） |

| D73 | Web 可修改托管主会话的模型与 thinking effort，不做 Max mode。模型目录和能力来自当前 harness；设置保存在服务端并在设备间共享，写入沿用 request_id 去重和设置 revision 校验。用于下一次开始处理的回复，运行中的轮次保持启动配置；离线排队消息也在开始执行时取设置。模型目录 ID 与实际模型名/provider 分开，验证支持的 effort；不修改 worker 配置。 | 已实现（2026-10-22） |
| D74 | 正文流式输出（修订 D69）：从 harness 的 agentMessage delta 累计正文，以稳定 item/message 身份有限频率持久化并发布累计视图；同一回复不会因片段或最终完整消息产生重复条目。会话读取能恢复当前正文，沿已有账本 event_seq 续传；结束、停止、失败及重启保留已生成内容并明确终态。每次正文或状态变化递增 assistant_revision，前端按版本合并同一条消息，沿用历史分页锚点与跟随规则。约每 100ms 批量保存累计正文，完整 item 立即覆盖；缺少完整 item 的已结束轮次标 unknown，停止或会话丢失标 interrupted，失败标 failed，均保留正文。 | 已实现（2026-10-22） |

| D75 | Web 任务工作流：最近完成入口与分阶段大看板，按生命周期列展示全部状态并分页读取；阻塞/控制状态保持独立标识。任务详情优先展示当前问题、真实待决选项与控制动作、当前产物和验收，再展示进展及可展开的 spec/工作区/原始证据。owner 只读任务列表增加独立全状态分页和项目/生命周期过滤，兼容原活动概览与已结束列表；卡片不通过拖拽改写状态。详见 `docs/plans/web-task-workflow.md`。 | 已实现（2026-10-23） |

| D76 | 唯一 owner 主会话中的完成通知：每个新的 task.completed 事件同事务写入 author=system 的持久化会话消息，绑定任务、被验收产物和源事件，并经原 SSE 同步；复用请求去重及领域完成转换，不回灌历史；初版不自动调用模型的限定由 D88 修订。通知有独立呈现，主 Agent 下一轮沿账本 delta 获得事实，完整重建可从近期消息回忆。详见 `docs/plans/web-task-workflow.md`。 | 已实现（2026-10-23） |
| D77 | 任务定义支持 follow_up 和 related 关系：新任务显式跟进原任务，双向读取和导航；关系为独立元数据，带 revision 的声明关系替换不重开终态或改变 spec，不自动继承 workspace/base 或调度依赖。创建、变更、事件和请求去重同事务，后续关系拒绝成环。详见 `docs/plans/task-relations.md`。 | 已实现（2026-10-23） |
| D78 | Workspace 作为独立资源由任务绑定顺序使用（细化 D33、D41）：任务选择 new、reuse 或 temporary；reuse 通过来源任务解析后固定资源身份与主机，不改变目录、分支或继承改动。复用任务失去原主机时只提供等待或取消，不支持原地 start-over；需要独立新环境时取消后创建 new 或 temporary 任务，禁止悄悄替换绑定资源。临时目录位于 runner 数据根下的 temporary-workspaces/<workspace_id>，不要求 Git。资源占用覆盖实现、验收、返工、暂停、待决及人工接管；任务终止且 runner 确认进程停止后才释放，未知运行继续占用。单个资源清理受阻不阻塞其他资源释放；删除前必须核对原提供方及目录的全部注册身份。排队复用引用阻止清理；最后一次使用释放后七天可清理临时目录，清理预留与新引用互斥。各次任务基线与历史产物独立保存。runner 在 hello/claim 显式报告 workspace_protocol，旧 runner 不领取新任务形式。详见 `docs/plans/workspace-reuse-and-deliverables.md`。 | 已实现（2026-10-23） |
| D79 | 交付与 workspace 模式正交：deliverables 可组合 commit、report、files，保留旧 delivery=commit；所有任务可附带报告，只有要求 commit 时检查 Git 变更。实现者显式提交结果，服务端持久化后独立验收；runner 在关闭已完成 worker 前持久化完成结果，重启后先对账终端控制权，再恢复停止、收集及提交，不重新执行该 worker；人工接管和停止暂时失败保留完成结果，等待归还或确认停止；文件通过按 run 鉴权和请求去重的二进制端点上传，runner 原子保存上传重放身份，提交工具在确认完成前拒绝重复文件名及身份；文件内容存储与尺寸规则由 D87 接替，SQLite 仅保存不可变清单与关联。报告及文件不随 workspace 清理删除；操作验收核对证据，不重复副作用，不确定结果进入待决。工具有界读取已交付文本文件，Web 经 owner 鉴权下载。 | 已实现（2026-10-23） |
| D80 | 人工接手所需的 provider 身份与显示分离（细化 D51、D63、D66）：Orca 原生 folder workspace 承载临时目录，Git worktree 保持原仓库上下文；显示名只有当前任务标题与短任务 ID，实际取得占用后更新，不改目录和分支。Orca 备注首行统一为 `tend workspace <resource-id>`，供侧栏按 tend 来源及 repo 聚合；旧本地状态下次准备时补齐，阶段变化更新备注，相同备注不重复写入。标记仅用于显示，不作为资源操作授权，详见 `docs/plans/orca-workspace-comments.md`。恢复与回收核对持久化资源 ID、路径及实例，不按显示名认领；凭据与运行状态置于 runner 私有目录，不依赖 .git。未来一个 Cove task/workspace 对应一个 tend 资源，各 tend 任务用新 tab，入口路径由 provider 返回；完整 Cove 后端及安全回收等待集成接口，不在本阶段实施。 | 已实现（2026-10-23） |
| D81 | 托管主会话按工具定义指纹轮换 native thread，保留 Web 历史、原 rollout 和可回溯祖先链；交接包由最新账本事实、覆盖版本明确的模型摘要及前序 ID/path 组成。默认空闲 50 分钟在只读临时 fork 中生成预备摘要，不写原会话或 Web；0 关闭，配置文件提供默认值，owner Web 设置独立修订并持久化覆盖。摘要覆盖消息内容及原地更新、工具结果和完成 turn，生成时新活动使摘要过期；同版本成功去重、失败持久化退避。总结分支禁用 Tend 工具、只读 sandbox、无审批升级。schema 变化的启动及 CLI 手动交接共用持久化 create/inject/switch 阶段，目标准备成功后原子切换，不确定外部请求保留状态且不盲重发；缺失摘要允许明确标记的事实包回退。CLI 可仅准备摘要；Web 设置统一从侧栏底部入口（手机待处理页底部）打开共享面板，不挤占模型/think effort 入口。 | 已实现（2026-10-23） |
| D82 | Runner 通信协议与程序版本独立，支持当前及上一代；无版本协议为 0，正式版本从 1 开始。每个 run 固定协议。Breaking change 同时递增版本、实现上一代适配、删除退役一代适配和更新兼容/拒绝测试。适配登记明确退役版本，make check 检查到期及代码标记；持久化历史读取与通信退役分别管理。进程启动使用不随业务 journal 回滚的本机持久 Generation 排序，不同 instance 仅更高代次可替换，迟到首次 hello 不退休当前进程。 | 已确认（2026-10-23） |
| D83 | Runner 在空闲及维护期持续心跳，默认 15 秒（租约要求更快时缩短），接收后 60 秒无有效心跳判离线。实例及序号防旧状态覆盖。维护与在线状态独立；server 事务及 runner 本地阻止新领取，现有 run 完成并交付后才 ready，未知或人工接管不能被排空过程清除。 | 已确认（2026-10-23） |
| D84 | Go 独立更新器监督 server/runner，server 从固定发布产物逐台编排。单个未结束发布批次，发布前全部启用主机在线且兼容并无退役协议依赖；每次保留另一台正常主机。主会话等当前轮结束，server 重启前确认固定截止的维护授权；默认授权 300 秒、重启预算 120 秒、排空 30 分钟。终态任务关联 run 全部本地 done、身份匹配、无 held/fenced/活动 executor 或待确认操作时，旧 app-server UUID 缺成功最终报告这一静态证明不足仅保持资源隔离，不阻止无关 runner 维护；实际 harness 调用错误和未确认原生操作继续阻塞，仍禁止释放/复用/清理，不补停止证明。已明确收到 workspace release 成功回执但 `released:false` 时，只清除本次本地释放意图和请求，不改变资源占用；任务终态或脱离旧绑定后由扫描重新对账并释放，网络未知仍保留原请求身份，避免排队验收与维护互相等待。Runner 健康确认后先由 updater 持久提交，再凭当前进程的提交证明开放领取；回退持久绑定未开放领取的 fence。停止授权丢回执且候选仍暂存时，以 staged 比较交换取消和完整未开始证明恢复旧服务，不续授权或伪称数据回退。升级阶段持久化，未知不盲重做，接收新业务后不恢复旧数据。详见 docs/plans/controlled-upgrades.md。 | 已确认（2026-10-23） |
| D85 | 升级与维护在 Web 主机列表、看板和发布详情展示；在线、维护和 slot 占用分别呈现。开始、每台恢复、完成及异常写持久 system 通知，同事务去重且不唤醒模型；细粒度进度只更新详情。上下文后续获得升级事实，网页断线不推断所有 runner 离线。 横幅只展示非终态发布，完成或取消后自动收起，历史结果保留在聊天通知及发布详情。手机看板的执行主机折叠入口置于最近完成之前，整行显示已登记总数，主机列表与最近完成内容分别允许有界纵向滚动，共同区域保留看板高度；项目筛选不筛掉主机。详见 docs/plans/mobile-header-hosts.md。 | 已确认（2026-10-23） |
| D86 | 聊天支持原生图片输入：图片与文字共用一个有 request_id 的持久发送请求，服务端同事务保存消息和独立图片字节，允许纯图消息。列表、事件与交接事实只携带图片元数据，不嵌入原始图片；owner 鉴权读取原图，主会话将图片作为原生 image 输入交给模型并留在 native 历史。初版图片入口与内联存储、尺寸门槛和 GIF 首帧策略已由 D87 的通用原始附件替代。Web 支持文件选择、粘贴、拖入、移除与放大，离线发送后刷新继续使用原请求身份；桌面与手机设置统一保留侧栏底部文字入口（手机待处理页），聊天与任务看板页头不再显示重复齿轮。详见 docs/plans/chat-images-and-ui-polish.md。 | 已实现（2026-10-24） |
| D87 | 聊天附件与任务交付原文件统一保存在数据库旁的独立内容目录，SQLite 只存元数据、hash、身份和消息/任务关联；上传不设应用层大小、累计大小、数量或图片分辨率门槛，上传下载流式处理，原字节不以 Base64 写入新消息、事件或数据库。不可变内容先暂存并完成落盘，app 再以单事务提交去重、元数据、事件与引用；中断上传不产生有效引用，失败清理本次临时文件。Web 持久发送队列保存 Blob、稳定上传请求 ID 和回执，先对账上传，再冻结消息引用并按原消息请求 ID 发送；普通文件/图片统一附件入口，预览失败不阻止上传或下载，浏览器下载使用仅绑定单个文件、展示方式、owner 身份及短有效期的签名 URL，不将 owner token 放入 URL。模型自动视觉输入只是一种有预算的便利，超预算/不可解码不影响原件访问；GIF 不自动改为首帧，agent 用附件引用和原文件按需分析，托管会话提供本地路径，外挂会话可用 CLI 下载，attachments_read 可在交接后恢复引用和有界文本窗口。旧图片及交付 BLOB 追加迁移并逐文件导出，确认持久文件与 hash 后才清空原文，保留旧 ID/请求/引用；原生 session/rollout 仍由 harness 管理。备份需同时覆盖数据库与内容目录；本轮不自动清理已发布原件。详见 docs/plans/streamed-attachments.md。 | 已实现（2026-10-24） |

| D88 | 托管主 Agent 专职调度：以登记仓库、账本、owner 输入及 worker 报告对齐目标、选择 workspace 与交付、关联附件、推进已授权目标、呈现结果及真正需要 owner 的决策；源码/日志调查、复现、技术 spec、实现与验收由 worker 执行。简单任务允许调查与实现合并，复杂任务可先 report-only 再 follow-up。thread/start、resume 和 handoff 使用 read-only / never，并关闭 shell、代码执行、原生多 agent 与搜索入口；保留 Tend 工具和输入呈现；conversation_read 按游标分页读取持久聊天原文与附件引用，使交接/压缩后的早期 owner 约束仍可恢复，不依赖 shell 读取 rollout；历史工具只返回身份/状态，避免递归展开。不把工具配置当作对抗恶意用户的边界。repos_list 提供登记仓库/基点/主机，worker 解析目录；workspace_state.path 是实际运行目录，mount.directory 为 runner 上报的仓库物理目录；mount.path 是逻辑标识，不能拼接推断实际路径，Orca 单仓库直接以 worktree 根运行。物理目录为可选协议字段，由 assignment.mount_directories 显式协商后才发给服务端，避免旧服务端严格解码拒绝新字段；追加 schema 32 保存事实；历史未知保持缺省，旧报告省略字段不覆盖已有事实。任务 spec.inputs 显式引用原始聊天 file_id，服务端冻结名称、MIME、大小、SHA-256 和来源消息；follow-up 不隐式复制附件，显式复用保留原件。两个 worker 角色都经 run 身份 download_file 获取本 spec 版本输入，runner 流式校验并原子保存到本轮 scratch；不依赖 owner 凭据、不改原字节、不加应用层大小/像素门槛。现有 runtime capabilities 宣告 task-inputs-v1，有输入的实现与验收只派给支持的 runner；旧无输入任务维持原语义，无协议 breaking change。新 task.completed、非托管主 Agent 自己触发的 decision.opened/answered 在原事务写入系统通知和持久 outbox；沿既有 main_turns 去重、串行投递及 unknown 对账，待处理 owner 消息/交接优先，当前 turn 不被后台事件抢断；owner 可沿统一停止入口中止后台回复，系统消息保留真实处理状态；不补发历史、不对心跳/普通进度调用模型。背景轮次明确标注账本来源，重读当前事实，只继续原已授权目标；不自行回答 owner 决策、不因静默重试 unknown。详见 docs/plans/dispatcher-inputs.md。 | 已实现（2026-10-24） |
| D89 | Worker 可在实现或验收的原 run 调用 ask_owner，将问题、证据及选项持久绑定到 task/run/action/spec；question_key 在 run 内稳定去重，相同内容可重放问答，答案不消费。复用待决、聊天与事件唤醒链路，保留问题/答案的来源及 reply 关联；主 Agent 仅转交真实 owner 答案，不代答、不重复开待决，不为送达答案创建 retry。Web 支持直接选项和自由文本回答。等待保留 run、执行 slot 和心跳，不让模型轮询；代码通过可取消有界长轮询等待，连接中断不删除问题。runner 重启持久恢复请求身份及问题参数，原生调用中断时用同 key 续接；取消或 spec/action 失效不能恢复旧工作，暂停保留问答。仅为幂等提问重连，不扩大结果提交的自动重放范围。通过可选 assignment 与 runtime capability 协商，旧服务不暴露工具，带问题的 run 不由缺能力的 runner 静默恢复。本期不引入 parked-run 或话题/tab，等待会占用容量并影响维护排空，界面明确呈现。移动端决定正文按内容增高并允许长词断行；决定 dock 在剩余高度内有界滚动；桌面附件与长草稿挤占高度时保留非零决定区预算并由 seat 提供滚动兜底，transcript 留在该滚动范围之外；composer 弹层通过 portal 贴近触发控件并保持在视口内；“跳到最新”只占用对话区域并在尺寸变化后重算。详见 docs/plans/worker-owner-questions.md 与 docs/plans/mobile-decision-layout-investigation.md。 | 已实现（2026-10-27） |
| D90 | 工作项承载持续用户意图，执行任务只是独立验收的一轮工作。每张卡片由调度 Agent 动态定义阶段、分组、依赖和后续事项，不设项目级业务阶段枚举；修订有版本和历史，不删除阶段身份、任务关联或证据，不通过改计划扩大授权。工作项共享背景仅目标、用户约束/决定、原始输入引用和有来源事实，任务按版本冻结；设计论证放 implementation_context，仅实现角色收到；独立验收保留必需的报告交付对象，但不自动注入可选作者报告。阶段与 task 多对多，默认 workspace 以真实源任务引用提供候选；D92 收紧为同主工作项且确需延续现场才复用，独立任务使用独立资源。当前任务完成、阶段清空和用户沉默不足以结束跟进；未决定下一步保持开放，收尾须明确责任终点/用户决定证据且无未处理承诺、活动或未知执行，保留可重开的历史。白板按通用关注状态投影，卡片显示自己的动态阶段。侧栏以三个工作项及查看全部入口为主，其后展示最近三个已完成执行任务及主工作项、完成时间；完成时间和可选排序由 task.completed 事件派生，不使用最后更新时间。移除按项目重复展示的当前任务列表；工作项详情保留关联执行，全局执行记录通过次级入口查看。见 docs/plans/sidebar-execution-entry.md。新建任务兼容旧客户端时创建独立工作项，不把历史任务自动转换。详见 docs/plans/work-items.md。 工具枚举与领域校验共用协议定义；参数错误定位字段并给出允许值，纠错保留完整计划。见 docs/plans/planning-tool-contract.md。 | 已实现（2026-10-25） |
| D91 | 交付观察独立于 workspace/harness：runner 通过 opt-in GitHub gh 与 Codebase bytedcli 适配器执行轻量只读查询，不使用模型或执行 slot。server 以独立观察表记录来源、状态、核验时间及错误，规划修订不覆盖观察；缺关闭人保持未知，不推断 owner 操作。provider capability、当前实例、查询代次及 nonce 防迟到覆盖；周期读取不进入执行 pending 队列，未知读取可重新观察。开放目标 5 分钟、暂缓 1 小时刷新；外部终态或工作项结束后停止周期读取，手动刷新可重查。事实变化通过事务通知及既有主会话事件路径唤醒，普通进度不调用模型；状态变化不授权合并/部署。观察端点为可选增量能力，无旧请求必填或默认语义变更。详见 docs/plans/delivery-observer.md。 | 已实现（2026-10-25） |
| D92 | 工作区属于唯一主工作项，任务主归属固定于创建时；次要关联不转移归属、不扩展复用权限。独立仓库任务（含只读调查）创建独立 worktree，无仓库任务使用 temporary；同项仅延续实际现场才 reuse，允许多资源并行，不实现跨项只读共享目录。历史归属仅从初始不可变 spec 回填，未知不猜测；已有绑定运行保留。尚无任何 run 的排队 reuse 任务可通过受版本控制的意图更新改为 new/temporary，原子解绑且不触动其他占用者；有执行历史保持原资源。执行投影由真实 run、资源、依赖及主机准入派生，显示排队、启动、实现、验收、等待用户及等待原因，不增加白板列、不自动改写阶段或责任终点。细化 D78、D90；不改变 server/runner 通信协议。详见 docs/plans/workspace-ownership-queue.md。 | 已实现（2026-10-25） |
| D93 | 任务输入统一引用聊天附件或已提交交付文件，规划者只需显式选择 file_id；服务端核对 artifact 成员及真实元数据，将来源 task/artifact 与原件身份冻结在不可变 spec。沿用单 owner 规划权限，不将 follow_up、同工作项或验收通过设为文件引用门槛；失败交付也可作为调查证据，可读取不代表认可内容。关系不自动复制输入，独立验收可见输入须排除仅供实现的设计。来源字段在 runner 边界投影掉，保持 task-inputs-v1 通信与原件下载校验；无新增表、blob 复制或协议版本变更。确定来源和已授权目标下调度 Agent 自动选择必要子集，不要求用户重传或确认技术绑定；绑定失败修正请求而非把 ID 改写到文字中，更新产生新 spec/执行，不热扩权旧 run。详见 docs/plans/artifact-task-inputs.md。 | 已实现（2026-10-25） |
| D94 | 外挂完整快照将固定操作说明与动态账本分开计量，固定说明完整注入但不挤占事实预算；动态部分含包头、项目及条目，按 UTF-8 字节计量，默认 2000，固定说明另计，使总量接近原整体 8000 的目标。外挂增量仍为 4000，托管完整/增量仍为 16000/6000。超额事实沿既有优先级按完整行压缩并给查询提示，受保护待处理事件允许超过预算；完成和待决不再因固定说明增加而消失。不改变账本保存、水位、去重或模型上下文窗口。详见 docs/plans/context-facts-budget.md。 | 已实现（2026-10-25） |
| D95 | 任务创建与收尾形成可读写闭环：requirements.host_id 是服务端硬调度条件，旧 runner spec 投影不携带该字段；固定主机的工作区失联只提供等待或取消，不隐式放弃资源后迁往其他主机。旧待决项的 start_over 回答保留为事实但不应用，并替换为有效选项；已有工作区的主机要求不可改写，只能等待，或取消后以合适的主机要求和独立工作区新建任务；验收视图提供 acceptance_id。工具错误定位字段并提供修正信息，tool_failures_list 只读统一查询原工具失败记录，不返回敏感原输入。完成事件按工作项保存待核对事实，阶段收尾或显式解释后确认，失败事务保留待核对；执行终态不自动关闭工作项。阶段展示区分真实执行与计划责任：逻辑终态不遮蔽尚未收束的 run，当前工作项视图在同一读事务附带执行事实水位，Web 以计划 revision 与事实水位共同拒绝乱序旧快照，不用计划修订表达执行变化。见 docs/plans/task-lifecycle-recovery.md、docs/plans/work-item-execution-consistency.md。 | 实施中（2026-10-27） |
| D96 | worker 明确根轮次失败独立于终端存活；默认每 run 一次有限自动恢复，计数与恢复阶段持久化，不能因 runner 重启重置。已确认连接错误可续原会话，加密历史错误用新会话交接原 thread/rollout、任务及已执行事实；后台工具与未知副作用先对账，不盲目重放。暂停、取消、人工接管停止恢复，恢复耗尽走既有失败与待决，不以无输出超时杀任务。见 docs/plans/task-lifecycle-recovery.md。 | 实施中（2026-10-26） |
| D97 | 工作项进展以去重任务的真实执行事实计数，区分启动与执行；当前步骤、数量和任务筛选优先展示，责任跟进不伪装成执行。完成核对后继续已有授权，未定下一步保存工作项级选择；选择可回答、撤回或替代，保留实际 owner 来源与版本，未回答选择阻止结清但不占执行槽。选择回答通过持久事件唤醒主会话，不直接执行部署。托管回答引用实际 owner 消息；外挂会话可记录明确标注的 agent 转交，审计请求身份与可用的原生会话身份，不伪装成直接 owner 消息。已回答选择可作为同工作项的 owner_decision 证据，保留转交归属，不额外要求用户再次确认；choice 变化纳入增量上下文及工作项范围过滤。 | 已实现（2026-10-26） |
| D98 | Orca 启动提供附加结构化诊断，只有明确未写入提示的就绪超时且原终端确认停止才允许原 run 有限重试；启动预算独立于 D96 的模型恢复，默认一次、允许关闭，先持久化再外部操作。未知投递或关闭结果保持对账，旧 Orca 无证据不自动重试。当前实现扩展已有事件及 journal 字段，不改变 runner 通信默认含义或旧请求校验，协议版本不变。 | 已实现（2026-10-26） |
| D99 | 工作项可引用升级批次作为 upgrade 证据，服务端核验已完成批次、目标范围及安装回执后冻结 release、manifest、revision、主机结果和核验时间；跳过主机须已有目标版本证据。历史快照不可由 agent 改写；升级证据不替代产品验收、不自动关闭工作项。 | 已实现（2026-10-26） |

| D100 | 任务通过不可变 spec.observation 显式声明工作项及部署事实的只读范围；实现和独立验收使用各自有效 run 身份获取事务一致、带水位和采集时点的最小快照，不开放 owner API、聊天、设计背景、凭据或任意路径。部署记录与实际运行身份分开，未知不推断，快照不替代浏览器渲染验证。新能力以 worker-observation-v1 调度与恢复门禁区分旧 runner，不改变旧 spec 行为；生产验收前调度必须准备所需核验渠道。见 docs/plans/worker-observation.md。 | 已实现（2026-10-26） |
| D101 | 工作项白板承载动态计划；独立任务入口统一为“任务执行”，以当前、已结束、全部列表呈现执行与历史。最近完成仍按任务展示并注明所属工作项；状态来自执行投影，分页数量仅表示已加载。资源与任务导航保留，不把任务生命周期列当作工作项阶段。见 docs/plans/task-execution-list.md。 | 已实现（2026-10-26） |

| D102 | 聊天待处理队列独立于正文分页，owner 消息在确认开始处理后按轮次展示。新增消息控制修订，撤回与投递领取同事务互斥，保留原文、附件和审计；已领取或 unknown 不伪撤回，取回编辑重新发送使用新请求身份。原聊天 seq 仍是审计水位，新 transcript 按稳定块分页；旧客户端将撤回投影为 interrupted，新客户端读取精确元数据。未准入与撤回正文不作为主 Agent 当前指令回灌，撤回使旧摘要失效。见 docs/plans/conversation-queue-and-followups.md。 | 已实施（2026-10-26） |
| D103 | Web 将本机待发送、服务端待处理、正在处理和撤回历史分开。发送尝试标记与本机取消在 IndexedDB 事务中互斥，提交后冻结原信封并沿原 request_id 对账；不跨网络等待持有锁。取消墓碑阻止旧标签页复活发送。撤回意图、草稿及已上传附件引用持久化；撤回确认后原草稿单独暂存，按请求身份恢复一次。草稿使用有期限且按页面实例 fencing 的编辑租约，刷新接管不受旧页面释放影响。正文按服务端处理块分页，单轮续页、流式输出和断线读回按来源及修订合并；队列计数和活动项不依赖当前正文页。 | 已实施（2026-10-26） |
| D104 | 工作项跟进规则独立于 task/follow_on，at/every/有限 condition 使用同事务 occurrence 和现有主会话 outbox；托管模式主动触发，外挂只读；revision、冻结目标、期限/次数、未领取取消与 unknown 暂停保留独立事实。定时器仅查询账本，不占 worker slot。手动检查计入累计次数，但不重排下一计划时间；显式更新可以重新安排已结束规则，累计次数保留。 | 已实现（2026-10-26） |
| D105 | 同工作项未领取后台事实合并为冻结批次，保留多工作项来源与独立完成核对；当前托管 turn 分页读取来源后可登记静默检查，普通回复在轮次结束时发布。工作项管理等待规则与执行历史，使用显式时区和持久请求身份。队列、实际轮次结果与业务完成相互独立。 | 已实现（2026-10-26） |
| D106 | Runner 配置维护复用发布批次，单主机冻结 claim 并排空 run/待确认操作；私有 updater 以批次、manifest、原/新配置哈希固定候选和备份，相同程序也执行受控重启。配置不进入发布包或账本，候选配置加载哈希及 provider 能力通过两次健康观察后，再以持久提交证明恢复领取。仅新能力协商后发送配置扩展，普通协议 1 请求不变。回滚恢复原配置且沿既有 admission fence 对账，unknown 不猜测。见 docs/plans/runner-config-maintenance.md。 | 已实现（2026-10-27） |
| D107 | 独立 TraeX app-server 在合并继承环境和显式覆盖后清除全部 `ORCA_` 环境项，避免嵌套执行器继承外层窗格、会话、launch 或 hook 路由身份；包含别名且大小写不敏感。普通进程设置、模型 home 和认证配置保留。Orca TUI 的原有启动与身份不变，不放松接管或未知结果保护，不改变 runner 通信协议。见 docs/plans/appserver-process-identity.md。 | 已实现（2026-10-27） |
| D108 | Orca worker 的首次 provider session 只有在新鲜、非空且匹配 Tend 指令的观测通过接管检查后才能持久绑定；失败观测不污染 journal。旧验收产物自行编译的启动代码不受服务升级覆盖，测试须外部隔离 ORCA_ 环境，后续实现吸收 D107。创建者不证明当前执行者，不据此丢弃可能的人工接管证据；诊断仅含身份和 prompt 摘要哈希。handback 不虚构 owner 曾操作终端，复用原 run 工具桥与已完成检查；历史 journal 的旧 handback 原文沿原请求重放，不因措辞变化重复发起。见 docs/plans/orca-root-observation.md。 | 已实现（2026-10-27） |
| D109 | 工作项与任务统一使用宽浮层，支持全屏阅读、直接链接及浏览器前进后退；从工作项打开任务保留工作项页签、折叠、滚动和返回焦点。已完成调查优先展示评审材料，活动执行优先展示阶段；跟进、承诺、交付和历史仍独立可达。输入原件及交付截图通过 owner 元数据与单文件短期签名展示，报告图片仅解析当前不可变清单的唯一相对名称或文件身份；不加载任意远程图片或 worker 路径。交付 HTML 使用无脚本、无同源权限的 sandbox 和禁止主动资源加载的 CSP 静态预览，原件下载及原有验收身份保持不变。不改变 runner 协议。见 docs/plans/detail-review-experience.md。 | 已实现（2026-10-27） |
| D110 | 仓库注册与 runner 本机绑定在线生效，细化 D30、D65：owner 经带请求去重及控制修订的 repos.register 接管仓库元数据，启动配置不覆盖或退役在线条目。runner 通过私有本机命令校验既有 Orca 仓库 origin、固定 provider ID，原子持久化后发布绑定，不重启或排空已有任务。hello 显式协商 runner-repositories-v1 后才发送可选仓库身份观测；server 只将与账本 remote 身份匹配的已加载绑定合入 host 的有效仓库集合。旧静态配置和协议 1/0 请求默认语义保留，新增绑定不改已有工作区或允许替换仓库身份；退役只阻止新挂载。详见 docs/plans/online-repositories.md。 | 已实现（2026-10-27） |
| D111 | 工作项独立后台会话，主 agent 保持调度权并登记消息关联和有来源上下文；owner 标签纠正优先，关联变更推进版本并使旧结果失效。后台串行会话、有限并发、结构化回传与持久 rollout；主聊天仅展示自身状态。原生 ctx/compact 与 thread 交接分开，unknown 不重放。topic/channel 仅记录后续方向。见 docs/plans/work-item-agent-conversations.md。按 owner 后续指示，暂时允许后台原生工具，创建和恢复显式使用 workspace-write + on-request + auto_review；Tend 工具继续按后台身份与工作项范围校验，业务调度归主 agent。原生 auto mode 不等于工作项数据强隔离；后台默认关闭，尚未达到完整发布验收。 | 实施中 |
| D112 | TraeX 根回合失败观察与有限恢复，细化 D67：能力协商启用 Orca 的可选 rootTurn 原生事实，固定 session、turn、pane、终端启动及 runtime 身份，区分根失败与后台操作收敛；可信暂时性错误在原 run/会话继续最多一次，预算和阶段沿用 journal 扩展，未知操作或发送先对账，恢复发送不明时保留原终端和占用等待处理。旧 Orca 保留 JSONL 证据路径；other 等错误不扩大重试。原生结束时钟缺失但本回合 hook 仍 working 时不提醒，历史不可读仍需核对；引导观察细化为后台待收敛的失败时也生成提醒；未收敛失败经已有 inbox、消息和后台来源事务形成一次持久提醒，已有待决不重复；不改数据库迁移和 runner 协议代次。详见 docs/plans/traex-root-turn-recovery.md。 | 已实现（2026-10-27） |

| D113 | 正式消息显式 linked/unassigned 判定，内容版本与事实检查作为逻辑轮完成条件；D119 将准确版本初始化与正式回复合并审核，审核与协调回合分别表达，允许同回合派发；保留旧原生流式入口，遗漏受限自动修正并持久恢复。托管主 Agent 始终启动后台会话，取代 D111 的默认关闭策略。新项当轮初始化，不实现历史回填。持久 deployment_id 拒绝跨空库实例回放；本次全新部署删除旧业务数据，仅独占转移网络身份。见 docs/plans/mandatory-message-context.md。 | 已实现（2026-10-27） |
| D114 | 退役外挂主协调 Agent，主协调统一由 Tend 托管。删除外挂 MCP/hook/setup、会话项目绑定及专用检查工具，不签发或接受外挂 agent token；保留 owner CLI、worker MCP、runner 和 provider。历史迁移与来源仍可读取，旧入口拒绝执行。取代 D45/D47/D48/D54/D59/D68 中外挂并存的运行规则。见 docs/plans/mandatory-message-context.md。 | 已确认（2026-10-27） |
| D119 | 主协调工具原子返回版本与剩余义务；准确版本的 initial_context 完成首次准备与来源审核，conversation_reply 合并正文、关联和事实审核。内容审核、协调回合与派发分别表达，已提交有效来源可在原生回合完成前派发；未知执行和失效事实仍围栏。格式 3 摘要以原生历史 H、当前有效账本 L、来源更正 D 合并，不重放工具输入输出；按模型预算选择一次或先历史后干净私有会话，分批必须完整覆盖。输入准备前登记持久退避，既有摘要与历史保留。细化 D81、D113，见 docs/plans/agent-ledger-handoff.md。 | 已实现（2026-10-28） |
| D118 | Worker 统一 YOLO，主/后台显式开放网络且保留文件及 Tend 工具边界；scratch 规范路径与独立验收目录、无每次探针；验收模型/强度支持 runner 默认与任务冻结选择。worker 可选结构化问题与主协调说明独立留痕，环境故障、证据缺口、授权及 owner 选择直接展示。新增通信字段通过能力协商，旧请求形状与默认选择保持，存储只追加迁移。见 docs/plans/worker-scratch-decision-context.md。 | 已实现（2026-10-28） |
| D120 | 托管主/后台 Agent 的明确限流终态采用持久指数退避与有界空输入续接；原生内部重试优先，保留工具结果和原责任，每次 native 执行独立记账。未知启动不重发，owner 停止、新指令及维护围栏在实际启动前校验。恢复状态和终止提醒持久投影。见 docs/plans/hosted-agent-rate-limit-recovery.md。 | 已实现（2026-10-28） |

| D115 | Owner 的补充消息由当前主会话投递循环领取，经原生 `turn/steer` 的 thread/expected-turn 前置条件送入同一回合；只有 owner 原文可 steer，系统事件仍排队，handoff 保持屏障。发送意图、接受回执和原生 user-item 消费证据独立保存；unknown 不重发、不作为后续 context 指令。工具首次 source/request 持久固定；规划 effect 使用原 turn 初始消息与 commit_seq 去重，实际 owner 审计来源独立保留，换 native call ID 或消费状态询问不创建新序号空间；回复固定开始时来源并关联该时点已消费的 owner 消息。已消费补充消息加入原逻辑轮，正式消息归类与事实检查不因原生回合完成而跳过；修正轮不接收 steer。接受不承诺即时消费，不支持路径继续等待。见 docs/plans/owner-main-turn-steer.md 与 docs/owner-message-steer.md。 | 已实现，未上线（2026-10-27） |
| D116 | 运行中 run 的补充消息独立于正式 task spec、owner decision 和控制通路。主代理工具显式冻结 task/run/action/spec/代次/控制修订和认证来源，服务端持久队列与投递历史按 run 有序领取；runner 在原生调用或工具响应前记录意图，未知不重发、不迁移到后续 run。TraeX app-server 以原 thread/expectedTurnId 调用 turn/steer；Orca TUI 经原 run 专属 MCP receive_messages/ack_message 在同一执行中的安全点协作接收，禁止 PTY 输入、interrupt、重开 turn 或重建模拟。工具响应产生不证明接收，TUI 显式 ack 与原生接受回执才记录 accepted，均不声明模型已读取或执行要求。暂停/取消/结束使未领取消息失败，已领取结果保留 unknown 待真实回执。新增 run-messages-v1 能力协商、可选 assignment 字段及迁移 0051/journal 0010，旧通信含义不变。专用回执在最终阶段与 done 后独立恢复，沿原请求身份上传；released 租约只允许匹配原领取身份的历史 report_message，不能恢复控制或领取权限。待回执阻止维护就绪。见 docs/plans/run-messages.md 与 docs/run-messages.md。 回执修复计划见 docs/plans/run-message-receipts.md。 | 已实现（2026-10-27） |

| D117 | 消费用量独立于 context 占用：按原生请求身份追加事实、请求 revision 替换贡献，重传、模型校正、父累计不得重复相加；同身份冲突单列并排除。全局/单 run 共享读模型，固定 Asia/Shanghai 自然周期，未知发生时点单列而非冒充接收时间。输入包含缓存明细，输出单列，缺失不填零。worker 历史/live 使用同一持久分页窗口与正式运行身份，缺口/截断可见。卡片显示当前动作和最近步骤，完整模式提供正文阅读流和工具详情；共用身份投影与轮询，切换不改变跟随状态，返回保留阅读位置。见 docs/plans/rollout-token-panel.md 与 docs/plans/rollout-reading-modes.md。 | 实施中 |

| D121 | Issue/sub-issue 为同一递归责任实体，单一具体 assignee 与 assignment epoch；task 唯一 primary issue，stage 可选，执行合同及独立验收保持。支持生命周期、重挂载与围栏交接，子项、在途控制和 unknown 未核清不伪关闭。机械命名迁移先保持历史存储/wire/UI 行为，公共切换后退役活动旧入口。过渡存储以 1:1 issue_controls 扩展现有行，work_items.revision 为唯一权威，当前投影与两份不可变快照同事务保存；不自动采用历史身份，公共入口在安全闭环后启用。P1b2a 已纳入 owner-only 内部 create/update、精确资料引用与 revision/epoch 及 legacy/shared-fact 围栏；信任唯一 owner 和其控制的主机/本地配置，不防恶意本地管理员或多租户；owner 显式引用跨 Issue 真实消息不转责任、不改变原作者，不以原 request/event 证明链限制资料；业务角色/对象控制由服务端校验，CLI 工具限制按适配器能力实现，main/issue/consultation 禁执行不能仅靠 prompt；不承诺同 OS 用户恶意进程隔离或新增 OS 沙箱，worker 沿现有 CLI 权限；agent 作用域与 task/验收边界仍独立校验；P1b2b 已纳入 owner 内部生命周期与终态逐批来源收尾，维持原 endpoint/history，不因收尾重开执行或停止 main；P1b3+b4 r2+P1c 同片接通 primary/no-stage 读取/上下文、owner task 冻结/实现/独立验收/完成核对与取消收尾，并注册 owner Issue API/CLI；新 claim 检查实际来源和继承语义变化，活跃 run 沿原合同。owner 输入投影不接收服务器冻结上下文，显式合同刷新复用原 spec/action 代次规则；旧 facade/history 保留。manager、Agent assignment 在 P4 正式纳入并待组合检查，显式 named task 执行见 D123；主 Agent 保留按实际 owner 原文转录已有 decision/version 既定选项的职责，经原版本/领域规则应用既定效果，不获得自由 canonical task 规划权限。A 批补 canonical context/followup/history/delivery observation/refresh 及 named discovery caller；复用原存储/receipt/sweep/observer，owner 当前认证即授权、manager 写 own Issue，父级只读；P5 deadline-owned 规则不开放 generic 编辑。旧公开入口由后续消费者切换后统一退役，不建永久双模式。见 docs/plans/canonical-issue-callers.md、docs/issue-agent-design.md、docs/plans/issue-agent-refactor.md、docs/plans/issue-controls.md、docs/plans/issue-owner-delivery.md。 P4 receipt-only 发现按包装错误识别空队头，保持原收据退避；named transport 验证使用实际 canonical 分类工具而非恢复旧工具。P4 管理输入复用 worker 已继承的祖先只读定义/有效事实；已知本机启动失败释放后台槽位并按原 pending outbox 持久退避，不改变 native unknown 对账。 | P0–P5 已 CI/rebase 合并；P4 自主负责人本地完整检查及独立 review 完成。后继 canonical callers/owner/UI 及四项实际 caller 修正为最终集成候选，定向检查通过，最终独立复审/完整检查/CI 与生产部署分别待完成 |
| D122 | D128 将 Issue manager 的实际原生进程迁至 runner：发布 category 任务后才领取，主 Agent 与 consultation 继续 host 托管；本项的来源审核、协调作用域及 CLI 限制继续适用。主 Agent 为默认个人助理和全局规划者，issue manager 在继承授权内独立澄清、规划、委派和推进；二者均禁止实际执行，调查也走 task。Issue 直接 owner 指令由本项审核，不经 main 重复审批；协调 execution/effect_seq 与 owner 来源分离，epoch、context 和请求身份共同围栏。P4 隔离候选复用单一本地具名配置、immutable execution 快照、实际 scoped 工具及现有 pool/outbox/batch，main 委派即可收自己的回合，manager 独立审阅本项来源。交接先围栏再核对本项旧 manager 控制/unknown，正常 worker 继续。具名 hosted adapter 复用 CLI 原生登记/回读的进程局部固定 inline PreToolUse 与 readonly/never；缺失不回退 D111，不修改 worker/owner 全局配置。允许 owner 材料呈现；CLI hook 引擎故障仍 fail-open，不宣称 OS 隔离或原子 fail-closed。取代 D111 后台原生执行开放及仅能评估、主协调独占本项派发的规则，保留 D88 的执行隔离。见 docs/issue-agent-design.md、docs/plans/hosted-issue-agents.md。 | P4 r19 规定 review 及后继窄修正、正式完整检查和交叉编译完成，PR #167 已 CI/rebase 合并；最终 owner/UI 收口及真实隔离验收另记，生产未部署 |
| D123 | D128 新增 issue_manager purpose、配置格式 2 与无工作区 pin，manager 由 category 任务领取，首次固定 runner/Agent，后续回合保持归属并逐轮释放槽位；manager 不套用实现/验收配对资格。当前通信协议为 2/1，协议 0 通信适配退役，旧持久快照保留原 hash。以下 P2/P3 的 1/0 描述为当时交付记录。全局 category 与 runner/runtime 本机多 Agent 库存分离，hosted main/issue/consultation 同样登记；只投影非密钥来源、版本和 hash。按 category 加可选 HARD runner 及 workspace host 选择，首次 host 必须同时支持实现及同 workspace host 的正式验收；稳定固定配置快照，恢复不重选，类别不授角色。复用既有 harness，通信按能力协商或完整协议升级。P2b 增加本机不可变私有快照、公共投影及原子 receipt/CAS/unknown 对账；通用 runner journal 11 追加可空 expected_control_revision，重启保留缺省与显式 CAS，旧 NULL 不回填，不改 wire 协议。P2c–P2d 复用 owner CLI/鉴权、runner 本机 socket/client/journal 和条件 hello ack，接通固定来源严格配置接受、全局 Category 及版本化安全库存登记。公共 DTO 唯一定义于 protocol，server 不成为第二配置源；同进程 unknown 沿原 Envelope，新 incarnation 真实围栏后才对明确 not_recorded 的前代尝试接续，登记 accepted 不等于 runtime available。P3 接通 OwnerTaskSpec 每 role 的 category/可选 HARD runtime/model/effort，过滤本机 definition 而不覆盖；空字段不加约束，旧 AcceptanceExecution 仍精确匹配，矛盾输入 invalid_request。当前 task/spec 首次 named 分配及 owner start-over 放弃 workspace 后的新资源分配，均核对同 host/backend 的实现和正式验收候选，只冻结当前 run；恢复用原 pin，后续验收分配才冻结自己的配置。多个 runtime 共用主机全局槽位及唯一 workspace 协调器，unknown 不释放资源。私有 instructions 与 model/effort 经真实 Traex/Orca 适配器注入，验收仍用独立 run/凭据/scratch/会话和受控上下文。RuntimeBinding.ExecutableRef 是实际启动命令，Installation.LocalReference 仅为本机来源元数据；不要求两者相等。wire 保持 1/0，由新 capability 和条件 ack 保持旧无选择请求与默认不变；追加 server migration 57 和 journal 12，旧 11 reader 在读 assignment 前按版本拒绝，新 reader 不给旧 run 补 pin，原无 instructions 快照不改 hash。P2e 增加 owner/named main/Issue manager 同一只读分类与 runner 安全目录；登记 receipt、executor 最后观测与在线时点分开，不授调度/配置权限或增加状态机。见 docs/plans/agent-catalog.md、docs/issue-agent-design.md、docs/plans/agent-definition-config.md、docs/plans/agent-registry.md、docs/plans/agent-inventory-control.md、docs/plans/category-agent-execution.md。 | P2/P3 的本地完整检查、独立 review、CI/rebase 合并已完成；P2e 只读目录/后继 caller 已纳入最终候选并完成旧固定完整检查，当前最终修正组合验证另记，生产未启用 |
| D124 | 开放 main、issue、active run 和独立 named-agent consultation 的直接聊天，保留真实 owner 来源；普通咨询无 issue/task、无实际执行，显式 promotion 去重后才进入业务合同。精确 run 补充不改 spec、不复活终态、不泄露实施 rationale 给验收。Orca 查看不等于接管，owner-only human_control/handback 保留；Cove 仅定义合同。见 docs/issue-agent-design.md。 | P6 对话与原生身份旧固定完整检查及双平台编译通过；咨询零业务写入、显式 promotion 与原 Orca 运行控制已有隔离实测，最终集成检查/CI 与生产部署未完成 |
| D125 | Attention 的 decision/action/information 与 assignment 分离，chat/Inbox/detail 共用身份，读取或归档不回答。手动来源注入先行，无飞书连接器；next action 与 deadline 保留来源，默认到期前 24 小时/到期提醒 main。Task/child 结果、owner 答复与期限明确路由到责任 manager/parent/main，复用 outbox/followup/batch，无变化静默，不增加定时器或巡逻 Agent。P5 接通稳定 Attention/原 decision 回答、当前责任人接续及 owner 人工动作确认；information 不增 ack 义务，不引入 future-dispatch/通用替换链。Deadline 复用单一 followup sweep/occurrence，具名 main 的窄 task rating 独立于用户动作与 Issue 规划顺序。owner 当前命令即有效确认，main 转录保留真实未撤回来源；期限修改同时受原有性质约束，停机不补发过时 near，关联 Attention 的行动只沿原对象收尾。见 docs/issue-agent-design.md、docs/plans/attention-planner.md。 | P5 正式五项 finding 修正独立复核、完整检查和交叉编译通过；PR #168 CI 全通过，rebase 合入 c54ee29b。最终 information UI 与关闭引用接点另片核验，生产未部署 |
| D126 | 旧账本一致性只读离线归档加新空业务账本，取代 D113 及 mandatory-message-context 部署计划中删除历史、不保留归档/恢复材料的安排。历史迁移不改写，archive reader 不走正常 store.Open 或服务循环；manifest 与原件独立校验，unknown 阻止切换。仅显式导入静态设置，新 deployment identity、runner journal/凭据隔离；已有新副作用后回退先对账。UI 先 Image Gen 设计、独立 xhigh review、用户确认再编码；分批 PR 不中途部署，完整后受控发布与真实 E2E。见 docs/issue-agent-design.md、docs/plans/issue-agent-refactor.md、docs/plans/offline-ledger-archive.md、docs/plans/archive-owner-api.md。 | P8 旧固定完整检查和交叉编译通过；正式 CLI 两项实际准入 finding 已修复并通过真实 red/green、定向 race/vet。最终修正组合检查/CI、线上归档切换及生产部署待完成 |
| D128 | Issue manager 改为真实的 issue_manager task，由 main/owner 按 category 发布，runner 首次领取时原子更新具体负责人。每轮共用 claim/租约/槽位/journal，等待释放槽位；责任按 Issue 收尾/交接结束，不独立验收管理任务。Codex/Traex app-server 承载，main/consultation 保留 host。协议 2/1、配置格式 2、保留账本受控迁移，unknown 先对账。修订 D122 的本机 manager 执行位置及 D123 的 manager 库存/领取方式。详见 docs/plans/runner-issue-managers.md。 | 实施中，未发布 |
| D129 | Issue 完整详情页统一负责人对话、当前进展、子项与交付，原执行及验收会话保持独立；摘要按 Issue revision 留存，活动／结果只读投影沿原账本。见 docs/plans/issue-detail-conversation.md。 | 已实现 |
| D130 | App 业务测试每进程通过真实迁移生成一次当前版本空库，只缓存关闭并确认无剩余 WAL 后的只读模板字节；每个夹具复制到独立目录，再走正常 store.Open，连接、原文件、时钟、ID 与配置初始化仍独立。迁移及历史库测试保留真实建库路径，不维护第二份 schema，不削减 race 或分片覆盖。见 docs/plans/app-fixture-template.md。 | 已实现 |
| D131 | 通信模型独立于原生 Agent 会话：Web IM/CLI 使用正式发布、来源端点、投递与阅读回执；legacy conversation/rollout 保留诊断。Agent 仅选回复输入，Tend 固定渠道地址；主动通知默认 Web 与已绑定 Telegram。Telegram 单 owner 私聊，先持久化后确认更新，unknown 不重发；消费证据即时推送，渠道 MarkRead 为可选能力。PR/CI 仅预留领域更新与按需唤醒接点，协议仍为 2/1。见 docs/plans/messaging-web-telegram.md。 | 已实现 |
| D132 | Web 界面改版（修订 D70 的单对话加覆盖层布局与 D121 UI r5 的侧栏摘要）：路由驱动的页面与可分享路径，旧 query 链接改写；侧栏只做分组导航，手机四标签；角色化 token 与共享基础组件，由检查脚本约束；界面使用用户术语，不默认显示内部标识；路由级拆包与全页面巡检。不引入路由或 UI 依赖，History API 与原生 dialog/popover 已满足需求。业务合同、owner API 与 runner wire 不变。见 docs/plans/web-ui-overhaul.md。 | 已实现 |

| D133 | 交互式 Web 沙盒：独立 tend-sandbox 复用服务端组装与真实 owner API、账本、调度、工具及 runner 协议，仅替换托管 Agent 会话与 runner 的执行／工作区边界。只接受带沙盒标记的数据目录，不进入发布包、不连接真实模型或生产数据。前端回归逐步迁移到沙盒。见 docs/plans/web-sandbox.md。 | 已实现 |

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
- 第二层业务去重：规划写入以 `(source_message_id, plan_commit_seq)` 唯一；D115 hosted 当前原生 turn 的去重 source 固定为其初始投递消息，payload 与对象绑定保留实际 owner 审计来源；run 创建以 `(action_id, generation)` 唯一（v0.1 §8.6）。
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

## 10. 主会话交接与预备摘要（D81）

工具定义的生命周期独立于 Web 对话。启动时比较注册的工具名、描述与参数 schema 指纹，发现变化（包括无指纹的历史 thread）后，使用新定义创建 fresh thread。用户接受这次上下文压缩；原 thread 和 rollout 不修改、不删除，交接包包含直接前序及完整祖先索引，提示必要时回查。原生历史的重建由 harness 负责，Tend 不解析或改写其私有历史格式；历史索引保留供人工及运维核验。D88 后托管主 Agent 通过 conversation_read 回查持久聊天，不直接读取 rollout 文件。

预备摘要通过 harness 的 ephemeral fork 生成，模型可见原上下文，总结提示与回复不会进入原 thread 或 Web 历史。fork 独立绑定拒绝 Tend 工具的 handler，采用只读 sandbox 和禁止审批升级的设置；结束、失败和取消均尝试停止并释放 fork。后台生成期间，新用户消息仍可在主 thread 执行，摘要保留其取得快照时的覆盖范围，不能冒称包含后来消息。

摘要正文和覆盖信息保存在 SQLite。D119 的格式 3 在同一读事务冻结当前有效账本对象 L、正式消息更正及未见通知 D 和覆盖水位，不再次输入历史工具快照。旧格式摘要保留但不认证新覆盖。默认输入目标为 256000 token 与有效窗口 80% 的较小值；缺少 tokenizer 时以 UTF-8 字节作明确的保守 token 上界，仅用于选择摘要路径。预算内一次生成；超预算或缺少原生用量时先摘要 H，再在无历史的私有会话合并 L、D；必要时按对象分批，完整成功才保存覆盖。输入准备也纳入持久化 15 分钟退避。内容版本跟踪用户与助手消息、流式原地更新、工具结果和主 turn 状态；仅最大消息序号不足以判断新旧。设置、摘要记录和内部交接事件不改变内容版本。默认空闲 50 分钟触发，同版本成功摘要直接复用，失败按持久化的 15 分钟退避；实际模型 cache 保留策略由 provider 决定，50 分钟不是 cache 命中的保证。文件配置 `main_agent.summary_idle_minutes` 缺省为 50，0 关闭；owner 设置覆盖文件默认值，范围 0..10080 分钟，和模型选择使用独立修订。

启动检测与 `tend conversation handoff` 共用迁移过程，`--prepare-only` 只准备摘要。新用户 turn 在交接切换前排队。优先复用覆盖最新内容的摘要，否则在可取得一致快照时生成；若有未处理消息或模型不可用，则使用标注过期或缺失的摘要、最新代码事实和历史索引。create 和 inject 在发请求前分别持久化阶段，收到确认后才推进，原子切换校验来源 thread 与 generation；不确定结果保持原映射，等待核对，不靠重试猜测成功。

D81 的重启恢复区分历史未知与当前活动：原进程已关闭且关联 outbox 为 unknown 的 starting turn 保留其事实，不阻止新绑定，但仍不重放原请求。连续工具集升级可以重新准备尚未 create 的 prepared 意图；已经确认的旧 target 在隔离下完成后继续迁移到当前工具集，发布 readiness 前必须达到最新 hash。source resume 明确拒绝时，按保留的历史引用、缓存摘要与账本恢复；不确定传输失败仍不擅自替代创建。

unknown 消息投递保留为历史事实，摘要通过持久观察保留其未知状态，但不把它当作当前运行或排队工作。人工交接使用自己的 dispatch request ID；另一个未结束 intent 已存在时，新交接明确失败并提示核对原请求，不复用旧请求身份、不自动重放，也不通过无限重试阻塞后续聊天。

## 11. 已注册仓库的 Web 只读投影（D30、D58、D70）

桌面侧栏在执行主机附近提供“已注册仓库”入口，显示全部账本登记的数量，包含退役仓库且不随 project 筛选。独立原生 dialog 提供可滚动列表、关闭与返回入口焦点；长名称换行，不占用底栏或把长列表塞进看板。手机工作项页按标题、项目筛选、六类关注状态及工作项列表形成连续阅读路径；标题与底栏保留，筛选随列表滚动。标题中的“资源与记录”打开原生 dialog，在执行主机、已注册仓库和最近完成之间切换，并提供完整执行记录入口。资源面板独立滚动，关闭后恢复触发入口焦点与背景列表位置，主机详情及任务详情继续使用已有组件；资源分类不改变全局读取范围。执行记录页保留原有资源展开和任务分页方式。此调整遵循 D70、D90 和 D92，仅改变呈现及入口组织，工作项、阶段计划、执行状态和未完成承诺仍以服务端投影为准。实施计划见 `docs/plans/mobile-work-items-implementation.md`。

仓库的 `retired` 状态在 Web 中显示为“已归档”。列表仅在渲染时稳定地将已归档仓库放到末尾，未归档组和归档组各自保持登记响应中的相对顺序；刷新沿用此规则，不改变账本状态和 API 的退役语义。实施与验证见 `docs/plans/repo-archive-ui.md`。

owner 认证 GET `/api/v1/repos` 与 `repos_list` 共用 views 的全量登记与主机对应投影。返回逻辑名称、退役状态、已登记且未退役的服务主机 ID；主机临时离线不取消服务配置，启用、可服务、在线和 checkout 是不同事实。没有服务主机明确展示，退役表示不接受新任务挂载。Web 不读取远程 URL、模板、运行配置或路径，也不展示默认基线候选；工具原有候选仍仅为候选。

首读、每次打开、手动刷新、事件流重连与恢复前台重新读取。失败保留最近成功记录并标为旧记录，未取得数据时数量未知；断线不能称为实时状态。取消旧请求并校验读取代次，旧响应不能覆盖较新的登记。schema 生成 Web 类型，此只读增量不修改 runner 协议。实施与验证见 `docs/plans/web-repo-registry.md`。

## 12. 工作项跟进规则（D104）

托管登记以工作项、源消息和原始 spec 的稳定业务身份去重；取消 occurrence 的原文只保留 owner 审计，模型历史与新摘要使用取消标记。时钟回拨后已记录的周期时点只推进下次时间，不重复派发。已结束调度的规则仍校验未领取输入的目标有效性；snoozed 扫描按实际索引到期时间退让，避免阻塞其他规则。

手动一次性检查消费当前安排的一次触发，不会随后自动再检查；调度已结束后的明确手动请求仍是单独核对，不恢复自动调度。历史分页在两条独立流（修订与 occurrence）间保留各自游标，不能因为其中一条流已读完而退回第一页。跟进变更的上下文增量遵循所属工作项的项目范围。

## 13. 后台批次与等待安排（D105）

后台队列按工作项合并尚未领取的默认事件和规则 occurrence；同一任务关联多个工作项时，各自保留批次和完成核对责任。无工作项的事件按业务对象归组。每个批次保留一个规范触发消息和全部原始来源，领取事务冻结来源集合；新事实进入下一批，owner 输入继续优先。冻结后的安全投递重试不改变输入，unknown 不自动再发。规则变更只撤销未领取的对应来源，其他来源继续保留，空批次取消；已冻结来源继续作为原始输入历史并要求行动前重读规则。

`background_sources` 使用已认证的当前 thread/turn/投递身份读取每页最多 50 条来源，完整保存答案及事件数据。每页读取留下回执；取消的未领取来源向模型隐藏正文和数据，owner 审计读取仍保留原文。`background_checkin` 只允许当前托管后台 turn，要求全部来源已读，且相关工作项及源完成事件没有未核对的 completion review；证据引用必须来自批次。它不回答待决、不改变选择卡、follow_on 或工作项收尾。主 Agent 的当前工具调用已观察到的条件不额外唤醒自己，原规则记录为已在当前轮次观察。

后台普通回复与工具记录共用持久暂存顺序，在轮次结束时决定发布，并保留原始产生时间；静默只抑制普通回复，工具记录仍保留审计。空的最终文本正常结束轮次而不发布空消息；静默回执绑定实际 thread/turn，安全重试的新轮次不能继承旧回执。成功静默检查留在规则历史，聊天正文收起该轮。失败、中断、unknown 保留真实状态并发布已产生的部分输出，重启也按此规则恢复。触发源和批次通知保留审计身份，专用 transcript 不重复展示跟进源与规范批次通知；任务完成、选择和待决的原始卡片继续可见。正常有结果的后台轮次仍发布回复。完成核对在静默工具调用后有变化时，结束事务重新核验工作项和全部未取消源事件的责任，不能用旧 check-in 遮蔽新责任。

工作项详情提供规则创建、编辑、暂停、恢复、取消、手动检查及修订/发生历史，卡片和当前步骤显示等待摘要，等待不冒充 running。绝对时间使用显式 IANA 时区转换为 UTC 毫秒，拒绝不存在或歧义的夏令时时刻；固定间隔是时长，不是当地每日时间。未知控制请求按 request_id 独立持久保存，刷新与多标签页不会替换它，核对和重试沿用原身份。规则、历史及来源读取使用读取代次和取消保护，并接入 SSE、重连和前台恢复。连续相同静默结果可折叠，展开仍能查看每次来源。

owner 认证的 `/api/v1/coordination/metrics` 从账本导出 pending owner/background 深度及最老创建时间、时点到 occurrence 入队的最大延迟、撤回竞争失败、累计合并来源数、成功静默检查数和 unknown 批次数。历史计数包括留存行，当前排队计数不含已运行轮次；不引入指标依赖。领取日志携带 request/outbox 身份，规则与来源身份保存在同事务事件及 occurrence。迁移 0041 只追加，不改变 runner 通信。旧 outbox 保持原投递方式；旧 Web 仍可使用原聊天 API，新界面按能力禁用未提供的跟进写入。工具定义变化沿 D81 交接，发布与回退预检沿实施计划执行；此实现不包含部署。

D113 补充：程序可确定处理 owner 无自由文本、精确匹配持久执行选项且已成功应用的直接面板回答；只为新生成原文保存本次 decision/version、task/spec/control revision、effect/generation 的授权事实及精确内容检查，不完成其他来源或逻辑轮。此语义不依赖 hosted main 配置。其他回答原文必须进入主协调责任。部署身份变化清理业务缓存，静态构建 shell 缓存由 service worker 版本管理。

D114 当前运行边界：D45、D47、D48、D54、D59、D68、D94 及旧部署章节中的外挂行为仅保留为历史设计，不再提供入口。owner 登录只签发 owner token，旧 agent token 和非托管主 Agent 身份拒绝执行。主 Agent 工具只有一套托管 schema；历史 `addon_relay` 及 actor 来源仍按真实身份读取。runner 的 `worker-mcp` 和 provider 类型独立保留，server/runner 通信协议不变。

D114 状态投影补充：主 Agent 的 `agent_conversations_list` 根据已认证的托管身份明确返回后台能力可用，与 owner API 的托管配置语义一致；`runtime_available` 表示能力已配置，会话排队、上下文就绪和运行失败仍由各自状态表达，不由存储查询的布尔零值推断运行时故障。

## 14. Composer 设置与上下文详情（D73、D89、D111）

模型与思考强度入口持续显示服务端确认的设置值，保存、待核对和错误状态独立提示，不将状态文字替换成强度值。上下文圆环默认只展示用量弧线，百分比保留在无障碍名称和详情中；鼠标悬停及键盘聚焦展示详情，指针可进入面板操作，也可返回圆环继续悬停，离开且焦点不在面板内时关闭。触屏保留点击入口，Escape 在 hover 或面板聚焦时均可关闭；从面板或圆环关闭时返回圆环焦点，hover 查看时保留原输入焦点。详见 `docs/plans/composer-settings-context-hover.md`。

D119 后台发布补充：conversation_reply 在主协调后台批次中也表示明确即时发布，原子保存消息、关联及审核。它撤销本轮此前的静默选择，持久发布身份阻止同轮后续 check-in 将已发布正文变成静默；普通原生后台文本仍保留既有延后结算语义。
## 15. 托管 Agent 限流恢复（D120）

原生 `willRetry=true` 仍由 harness 管理；只有回合最终失败且结构化错误确认为限流，才创建恢复链。原消息 outbox 保持 delivered，含义仍是输入已送达。恢复链、独立执行关联、下一次时间及 outbox 在同一事务提交，重复回执不重复安排。默认等待 5 秒，指数增长到 120 秒并加 0–20% 正向抖动，最多 8 次、首次失败后 15 分钟内安排；等待退出后台进程并释放并发名额。`main_agent.recovery` 可覆盖 `base_delay_seconds`、`max_delay_seconds`、`max_elapsed_seconds`、`max_attempts`，次数为 0 禁用。

续接前核对同一 native thread 的失败回执、已结算工具、owner 控制、工作项状态、设置版本与维护围栏。仅经已验证的 Codex 协议 `turn/start input=[]` 创建新执行，不再次传入原消息、附件或合成用户提示；通过已有 developer 注入更新账本事实。Traex 或缺少可核验回执的实现保留需处理状态。空输入没有持久原生幂等键：先存启动意图，再从响应或 turn/started 保存 turn_id；响应丢失且无法确定身份时保留 unknown，禁止重发。

owner 停止取消等待计划；新的 owner 指令取消旧的等待续接并显式承接未完成前台逻辑责任。执行已开始后的新输入沿 D115 处理。恢复预算、时间及身份跨重启不重置。owner 会话视图增加可选 recovery 信息；停止复用已有幂等控制请求。耗尽预算或受阻只生成一次来源可追溯提醒，并交给现有主协调者；提醒自身不会再次产生自动恢复提醒链。runner 协议未改变，存储使用追加迁移。

P4 正式纳入补记：固定候选 r4 patch SHA-256 `3ac992986f6ba8dc380072e8418c3b5fc4d572fa893b63df7717fdc40db292d4`，59 文件；三项修正已独立复审通过。正式前件为 `c69c66b2be9b49aa1fbe824d9ea6283bec5dc880`；本次保留 P3 完整检查、上游 CI/合并记录及其 tools schema 修正，仅合并最新状态文档，不改变已预审的运行代码。正式组合检查与规定 CLI review 待完成。

P4 重挂与读取补充：父 Issue 负责人可读取受委派后代的任务成果，写入仍由各任务 immutable primary 的负责人控制；正式验收上下文不因此扩展。重挂核对子树实际已领取的普通投递，即使尚未建立 native execution 行，也须等 sending/unknown 收束；未领取 pending 与明确 failed 不新增阻塞。这些规则复用已有 outbox/conversation 事实，不引入新的授权或锁服务。


P4 管理执行恢复细化（D121）：原生 turn/start 前的 hook 准入失败明确未发送；冻结 executor 不免除普通业务重试的实际 receipt 围栏。canonical 终态继续原待交付结果/来源 batch 的发现与核对，历史 legacy closed 不放开。具体修正与验证见 docs/plans/hosted-issue-agents.md。

P4 来源/续接细化（D121）：named 主会话按原消息版本与 owner 纠正锁分类 canonical Issue 来源，用本轮持久effect身份完成自己的路由份额，不能替 Issue 技术审核；职责提示只列实际允许的协调行为。已知未发的普通启动退避不阻断原已注册、到期的冻结续接，后到普通输入仍保持原顺序，sending/unknown按原对账。见[实施计划](plans/hosted-issue-agents.md)。

主会话来源回声细化（D121）：对本协调 logical 自己已持久回复完成分类不构成新外部输入，不额外唤醒负责人；repair 使用同一 logical 归属。当前 owner 显式选择/补充资料仍沿原负责人批量通知，分类与 Issue 技术审阅保持分离。


D121 托管调用补充（r14 候选）：业务角色的资料准入复用原 outbox/native 消费事实；queued owner 材料保留后继责任，不经 Issue 读/审阅/建 task 旁路变成当前已消费资料，也不因其待审阻塞旧 logical 的已准入份额收尾。owner 当前命令仍是授权，不建来源真实性证明链。父 Issue 可用经现状态核验的真实后代验收结果收尾而无后代控制权；named main schema 保留原 owner-source selector，自动 metadata repair 指令依实际冻结角色工具。详见 [托管计划](plans/hosted-issue-agents.md)。

D121 来源消费一致性（r15 候选）：分类 target 与新增消息型完成证据不能抢先消费排队 owner 输入；已撤回原文不能成为新增完成证据。agent 会话预览复用原显示准入，owner 原始历史读取不变。owner 的旧、新分类入口在同一原事务通知 old∪new canonical 负责人，保留原 logical 自己回复分类不额外唤醒、请求重放不重复与来源更正审核。

D121 准入/结果边界细化（r16 候选）：冻结续接仍先核实际native receipt，当前主会话配置缺失时原合法冻结repair可继续，新输入保持排队。已确认的子项业务结果与原生收尾回执分开；父项依确认结果核对，实际task/control unknown仍受原事实约束，不把纯叙述/metadata尾部扩大为全子树静止门槛。


D121 管理者实际来源与收尾接点（r17候选）：围栏旧负责人的已完成/确定未发原delivery若仍sending，恢复原冻结runtime仅供原dispatcher对账，不准入旧epoch新native/工具写。所有当前named源读取含background_sources沿既有准入/撤回投影，普通unknown且starting无实际消费回执保持未准入；已确认started/terminal/原native_item历史保持可用。同logical main自己叙述的分类仅展示记账，不产生新技术来源义务，不清原外部义务；owner后来显式选择仍正常产生审核和唤醒。本人Issue明确branch cancel沿原领域规则收束子树执行是用户授权的窄特例，不等于任意跨Issue tasks_cancel，不改子项负责人/epoch或清unknown；finish_cancel仍等真实收束，不新增子manager确认流程。

D121 管理执行维护摘要：已入账但 native 消费未确认的 unknown 请求仍是必须保全的历史事实，私有交接摘要保留必要原文与未确认标记，不形成新授权或重放。普通来源准入与撤回脱敏不变，详见 hosted-issue-agents 的 r19。

D121 归档组合已选中但未启动的正常暂停也通知原 pool，保持未领取输入与收据对账；复用已审 prepare-exit，不新增队列/定时器或业务权限。

管理会话恢复细化（D121）：咨询与 Issue 管理会话共同遵守实际可服务队头的持久启动退避；后到输入不提前重试旧队头，已在途和原生回执对账仍可发现。

D121 公共计划类型B0：直接改原单一IssuePlan/Stage/FollowOn/Delivery/Evidence/Closure/TaskDisposition声明与其枚举/Values引用，不新增alias或复制model；JSON/SQL/runner wire/历史不变，owner定义/入口退役仍分批。见 docs/plans/issue-plan-type-names.md。


## P7 当前 owner 入口与旧活动 facade 退役

以下是当前切换的追加记录；前文历史设计与已实现行为保留，冲突的旧活动入口由此处及对应实施计划明确取代，raw archive 与 runner wire 不变。

D121/D122 最终活动入口收口（候选）：canonical context/tool 与 owner 当前 Issue 身份来自实际 control 行；旧 opaque 历史/worker wire 不改为新活动身份。named manager 的新入口统一使用真实 issues_*，default/owner 旧活动 facade 与固定 UI 最终切换同批退役。无 Issue Attention 的既有 followup context 按实际 target 显示，不伪造 Issue；沿原 occurrence/main 路由，不新增调度服务。见 [最终入口计划](plans/final-issue-entry-cutover.md)。


D121/D122 公共退役（候选）：当前责任只由 canonical Issue 与单 primary task 路径创建/编辑；旧 WorkItem owner/default-tool 可写 facade 退出，不保留第二活动业务模式。原 Choice 的问题身份、版本与回答效果及历史/raw/runner wire保留；启用托管管理的server必须显式named，缺配置不落回旧native路径，owner-only可省略。与固定UI消费者切换共同发布，见 [入口退役计划](plans/issue-entry-retirement.md)。

D121/D123 owner CLI补齐：Issue负责人交接沿既有epoch/CAS/request提供assign/finish-assignment，hosted安全目录可只读查询；不新增状态/热reload，目录不代表runtime可用。见 docs/plans/owner-agent-cli.md。

### D121：已批准 Issue UI 表面接入

主会话与 Issue/Attention/目录/对话来源/run/只读档案的桌面及手机设计已由用户确认，按 `docs/plans/ui-issue-surfaces.md` 分组实施。保留现有 React/Vite shell、信息侧栏、三入口、共享详情与原消息/原件组件，整体黑白中性；不增加第二缓存、轮询或业务真相。可见UI仅消费对应片段已实现的 canonical 合同，B1 owner字段投影与旧入口退役仍需同步收口；类型改名不等于活动页面已切换。当前页面候选已通过隔离桌面/手机 caller 检查与 Web 定向验证，冻结供独立预审；共享附件恢复片段已独立复审通过。Attention 时间/提醒已接固定后件，owner task canonical 关联合同已接固定后件，最终组合仍待核对，尚不代表全部页面完成、整仓检查通过、合并或部署。

已批准 UI 的预审修正保持真实对象边界：定义草稿固定编辑起点及原 CAS，新版本可明确载入替换；历史定义 reader 按 Issue/选中版本加载及关闭；原生信息仅展示所选 run 的工作区，不继承任务当前工作区；images-only 来源保留既有原图入口。变更区显示实际内容，任务表单固定操作区，手动来源提交前显示原目标，unknown 保留同一请求。Attention 最晚行动时间、时区和提醒设置读取固定服务端字段，计划时间独立显示；这些候选变更尚待独立 delta 复审及最终组合。

恢复定义请求核实为 stale 后，组件仍保留其原 payload/CAS/epoch/任务处置，不回落到重载时的新定义；只有明确载入才采用新 snapshot，不合成历史 IssueView。任务列表、详情及任务引用消费真实 primary_issue_id/title 和 issue_ids；Markdown 的候选 ID 先经当前 Issue GET 确认再给入口，旧 ID 保留文本或原历史流程。正式验收上下文、原档案读取及 TaskInputs 文件scope不变，无第二缓存/轮询。


### UI 活动旧工作项入口退役（D121、D123）

当前 Web 只从真实 canonical Issue/task 入口进入活动责任与执行页面。旧 work_item query 不再恢复可编辑旧 facade，也不把旧 ID 解释为当前 Issue；旧账本通过专用只读 archive 浏览。历史 Choice 保留原问题/版本/答案及原请求业务效果，未知关联只显示原 ID。共用 reader、当前 task 原件与正式验收上下文不变，无新重定向或长期双模式。


UI r5（D121、D123）：主界面“需要你处理”标题常驻连接既有Inbox，空态/全部已解决/摘要失败不隐藏入口；保留原布局和手机导航，无新状态或读取体系。

D125/D121 Attention 完成证据：同 Issue 实际 resolved decision/action 可直接作为 owner_decision 引用；information、未解决、撤回及跨 Issue 不能借此成为 owner 决定。scoped 和通用 resolver 使用同一既有对象判据；不增加 proof/权限状态。任务结果仍用 passed acceptance_id，delivery 只指已有核验的外部观察。见 docs/plans/attention-owner-evidence.md。

D121/D122 最终 caller 修正：owner 创建直接使用原已登记 Issue manager 验证，创建、manager 会话及原责任 wake 在同一请求事务提交；不增加客户端双阶段创建状态。归档来源携带实际内容版本，information 只作通知，任务首 run 自动显示且保留显式历史选择。见 docs/plans/final-ui-contract-review-fixes.md。

D121/D123 可选托管目录：未配置时，owner 只读目录返回明确 unconfigured 空投影（inline、格式版本 1、空 Agent/runtime，无伪造环境/登记身份）；仅 store.ErrNotFound 表示此空态，原鉴权、已配置结果及真实错误不变。目录沿已批准分组显示未配置提示。见 docs/plans/empty-hosted-catalog.md。

D63/D67 原生根 turn 观测修正：Orca pane 的聚合 prompt/done 不是受管根 session 的输入或完成证明。Tend 先核对并保存原 runtime、terminal/pane、launch 与 provider session/根 turn 的归属，再判断人工控制或业务完成；原生 API 或既有 Codex 原记录提供事实，缺失时保持 unknown，不重放、不提前撤桥。真实同 session 新输入及身份改变仍受原接管规则约束，不使用后台文本白名单。运行与历史清理共用该根事实。见 docs/plans/orca-managed-root-observation.md。

D63/D67 的原生投影保留已知 live child 的执行边界；调用返回与根结束不替代 child settled。宿主输入分类、launch/runtime 身份与能力探测分别使用原证据，受管发送回执对账不因新 turn 尚未可见而丢失旧根身份，也不将旧根完成套用于新轮。

旧 Orca 缺少 child inventory 时，D67 从已定位的原生 durable execution receipt 保留原 child/process 的未完成事实；同身份明确终态才允许 settled，根 turn 切换或工具响应返回均不清空该事实。


D63/D66 named TraeX 选择沿既有 Orca Trae TUI 路径传递精确 model/effort。支持性由实际 CLI 与所连执行 host 的独立 capability 正证据决定；旧版本、缺失或错误均保持不可用，不省略参数或切换 provider。配置目录与模型配额/真实调用可用性分别核验。见 docs/plans/orca-traex-launch-preferences.md。

### D127：顶层 Issue 看板与合并状态通知

总览恢复固定六列工作状态看板，工作进度与实际执行事实分离，正式完成/取消仍由 canonical 生命周期决定。无阻塞的 owner 拖拽/菜单直接保存，有阻塞保留原状态并给出处理入口，不增加完成/取消二次确认。阶段只读摘要，内部详情与阶段编辑不属于本批。

状态与审计即时入账，给当前 Issue Agent 的通知按最后一次实际变更防抖，默认三分钟；系统设置可选立即或一至三十分钟。窗口持久化，拖回起始状态不通知，Agent 自身更新不自唤醒；沿原具名管理会话/background/outbox，保留冻结输入与 unknown 回执对账。追加迁移不回写历史、不触发迁移通知、不改变 runner wire。布局依据已批准稿，样式复用当前 token 与 shell。见 [实施计划](plans/issue-kanban.md)。

可撤销的状态通知使用独立合并组，不吞掉首次接手等原有唤醒；负责人变成人时不转送个人规划会话，仍保留取消与当前负责人准入语义。项目筛选保留可输入方式，使已有 Issue 所属的停用项目仍可访问。
## D129：Issue 详情与负责人对话统一

Issue 默认作为应用内完整详情页，保留全局导航与原 query 链接。主栏突出 owner 待处理、本项负责人维护的进展／下一步、直接子项、讨论和交付结果；任务执行、来源、跟进及历史按需查看。负责人会话为固定入口，worker 实施和独立验收会话仍独立；服务端返回实际逻辑会话身份，GET 不创建会话。摘要独立保存不可变 revision，CAS／assignment epoch／request 去重与事件原子提交；历史投影不混入当前事实。活动和交付索引只读取原账本，保留作者及 artifact／spec／验收身份，不推断责任完成。计划见 [Issue 详情统一](plans/issue-detail-conversation.md)。

## Token 用量与资源入口（D70、D117）

资源与记录共用整行导航：全部任务执行、已注册仓库、Token 用量；主机状态另行分隔。Issue 页直接提供资源 dialog，执行记录页保留资源入口，原生 dialog 负责返回、全屏和入口焦点。

全局用量默认全部已采集历史，主摘要直接使用当前响应 totals；时间未知摘要在全部历史下是其中子集，在所选时段下是未计入时段的历史数据，始终受同一模型与 subject 筛选。未知时间不进入趋势，缺失总量不由输入输出合成，缓存不重复加总。摘要、统一筛选、时间分布、分类、明细与覆盖说明依次呈现；分类以已知总量和请求覆盖数为主，各组保留输入、输出及缓存的折叠详情；同筛选更新失败显示带截止时间的上次读数，新筛选失败不借用旧读数。模型名称只为选单累积，不混合数值快照。实施与验证见 `docs/plans/token-usage-integration.md`。

## D132：Web 界面改版

页面由路径决定：主对话、收件箱、Issue 看板与详情、任务执行与详情、资源、设置、归档各有路径，浏览器前进后退与刷新保持所在页面；旧 `?issue=`/`?task=` 链接改写为对应路径。只有短暂表单与确认使用 Sheet/Dialog，不再叠加页面级弹窗。主对话常驻挂载，换页不重建流与草稿。

S2 使用每个 History entry 的本次页面会话身份判断返回：可确认前项属于本次应用会话时浏览器后退，否则替换为父级路径；刷新不采用旧页面会话的返回栈。历史项保存可见页面的滚动、展开状态与触发元素，换页聚焦标题，后退恢复仍可识别的触发元素。Issue 筛选写入 query，原页修改筛选不抢走输入焦点。沿 D129 保持负责人会话唯一展示面：Issue 负责人会话路径读取实际归属后替换到 Issue 详情，独立咨询使用 `/chat/{conversation}`。

侧栏只放分组导航（对话、工作、资源、设置），待处理由收件箱计数与主对话待处理区承担；侧栏宽度在各页面一致。样式只使用 token 定义的字号、间距、圆角、层级与颜色，控件来自共享基础组件。界面文案遵循中英混排加空格与统一术语，内部标识、哈希与工具名默认收起。计划见 [Web UI 整体改版](plans/web-ui-overhaul.md)。

S6 中非聊天路由使用 lazy 与 Suspense 骨架，第三方模块独立共享；构建生成的离线 shell 清单仍包含全部路由资源。手机主操作与标签采用触控尺寸，输入区和浮动跳转避让安全区，详情及短暂弹层隐藏标签栏。全路由深浅主题巡检与导航回归纳入 CI。系统决定的问题与原因说明使用中文；内建选项字符串同时是回答标识和 Effects 键，保留其字节，由 Web 完整映射为中文显示。

## D133：交互式 Web 沙盒

`tend-sandbox` 是开发与前端验收入口，不是服务端运行模式，也不进入发布包。`internal/serverkit` 集中组装真实 SQLite、app/domain、views、owner API、runnerapi、scheduler、dispatch、contextbuilder、tools、mainthread 与嵌入 Web。显式注入 listener／WhoIs、时钟／ID 与托管会话 opener；正式服务端保留原配置和真实边界。

沙盒只替换托管 Agent 会话和 runner 的 Workers／Workspaces：脚本通过真实工具回调写账本，进程内 runner 经回环 HTTP 领取并执行任务。数据目录必须为空或具有内容匹配的沙盒标记；重置只能作用于已验证目录，不读取生产配置、凭据或 tailnet 状态，不启动真实模型、git 或 updater。demo 通过带 request_id 的 app 命令播种，持久请求身份使重复启动不重复创建对象。所有后台循环随 context 取消并等待退出。

Web 开发可使用同源嵌入页面或 `TEND_DEV_SERVER` 的 Vite 代理。既有 JSON fixture 回归保持不变，后续逐步以沙盒承载 UI 巡检与交互验收。实施与脚本规则见 [Web 交互沙盒](plans/web-sandbox.md)。
