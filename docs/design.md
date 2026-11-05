# tend：系统设计 v0.2（相对 v0.1 的调整）

## D202：按 Category / Agent 托管用户级扩展

server 托管 skills、完整 plugins、MCP 定义与用户级 AGENTS.md，草稿编辑与发布分开。包获取和本机视图由独立 extensions/codexenv 模块负责；app/store 只保存可移植资源、组合 revision、发布引用与执行 pin，发布写入遵守去重、CAS、事务和事件边界。包内容先不可变暂存，引用提交后才生效。CLI 经组装注入获取包，不直接依赖 runner/server；serverkit、runner、harness 可依赖 extensions，extensions 依赖 protocol、blobstore、codexenv/procx，不依赖 app/store。app 在事务内仅调用纯组合/校验函数，文件暂存与注入的原生加载探针在事务外执行。

有效组合只使用执行选中的一个 category，再叠加完整 Agent identity；Agent 层可覆盖/禁用资源，AGENTS 按类别在前、自身在后合成。组合 revision 独立于 Agent 身份与模型。项目来源、管理员要求和角色权限保持；Traex 不改变。本轮取代 D197 的 bwrap 启动层：保留原 HOME 与宿主执行语义，启动前使用本机用户共用、六小时节流的个人 skill 路径排除缓存，接受缓存窗口内新增技能尚未排除的边界。各执行使用稳定的私有配置 home，原生维护该执行的 MCP OAuth 并在重启时保留；会话、SQLite、日志、OpenAI auth 和线程锁仍指向原固定状态 home，保留历史 rollout 路径别名。MCP 定义分发，秘密/OAuth 留在执行主机。发布与分发不等于采用：新执行固定目标和组合，恢复保留原引用，长期会话仅在原门禁内更新，不改变 native thread、固定状态路径或重放 unknown 输入。可选协议扩展须经能力协商；未启用的旧语义保持。历史投影环境和快照继续支持恢复，不自动迁移生产或清理内容。历史恢复只读取已验证的本机快照引用，不因新工作的命令、凭据或基础配置缺失而阻止原回执对账。原生启动准备使用调用方 context，进程存续使用单独持有的生命周期。心跳仅携带有界活跃采用观测并显式标识截断，完整历史仍经本机分页诊断保留。新字段在每次 hello 确认能力后才上报，owner 转 worker 按任务类别重新解析组合。尚无输入的空线程通过原生命名元数据持久化，再恢复同一 thread；不创建模型 turn 或合成用户输入。详见 [实施计划](plans/managed-extensions.md)。

## D197：本机 Codex 能力环境

环境映射属于实际执行节点。server 本机 named 会话才使用 server 的 runtime 映射与环境绑定；runner 托管的协调会话沿已领取的 `RunnerRunID` 建立 `RunnerSession`，由执行 runner 校验并采用自己的本机环境。两个节点可使用相同 runtime ID，server 的同名映射不构成远端连接冲突；未领取的选择仍不能建立远端会话。不改变本机绑定隔离、线上协议或角色权限。

server 本机协调会话以已有 named binding identity 保存并恢复环境目录与 ID，旧线程不会因新增映射而改变 home；runner 当前映射不可用只降低相应执行器的就绪状态，保留目录与原 run 仍可恢复。配置请求串行锁与执行引用锁分离，包复制和原生校验不阻止执行收尾；环境退役在检查引用到提交期间继续排斥新执行 pin。

`codexenv` 管理固定运行 home、clean/projection 扩展、AGENTS 整文件同步和不可变能力快照。Linux 使用 bubblewrap 用户 mount namespace 隔离用户技能入口与插件工作缓存；保留 HOME、项目配置和管理员要求。只共享显式 auth 文件链接，不继承主 home 的完整配置，不调用账号级插件安装或卸载。配置与管理请求以本机原子记录去重和版本校验，不访问业务账本。

CLI 经组装注入调用本机模块；serverkit、runner 和 harness 可依赖 codexenv，codexenv 仅依赖 protocol、cfgfile/procx 及文件/进程边界，不依赖业务 app/store。TOML 采用 BurntSushi/toml，插件版本比较使用 x/mod 的语义版本实现。执行采用引用使用独立 SQLite WAL 索引和有界锁等待并分页读取，不把逐次运行历史复制到配置请求回执。可选 runtime 映射不替代 ExecutableRef，不改变线上协议或未配置环境的行为。自动同步只发布快照；运行和回执的生命周期决定采用时点，unknown 不自动续接。首次执行前在 run 的固定本机状态目录保存环境目录和 ID，runtime 映射变更不重定向原执行；仅禁用环境不要求重建来源。已使用 home 通过小型本机目录登记表保留默认 rollout 读取范围，不扫描 run 历史；显式 roots 优先。watch 预检明确失败与已发出操作的未知结果分别记录。丢失原 Session 后，独立进程关闭证明也回写原环境执行引用；未证明关闭不得解除活跃或 unknown。协调模式的权限限制保持优先。详见 [实施计划](plans/codex-environments.md)。

**日期：2026-10-14**
**基线：《中心化 Agent 任务管理服务：系统设计 v0.1》（下称 v0.1，原文应放在 `docs/design-v0.1.md`）。**

本文只记录 v0.2 相对 v0.1 的新增决策和修订。没有提到的部分仍按 v0.1 执行；
D225 修订 D11 的通用请求持久化方式，其他未另行修订的基线决策保持。
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
| D28 | 历史原生调用曾通过 `native_requests` 分配请求 ID；D225 退出通用映射，以实际原生业务身份及业务唯一 key 完成幂等。 | 由 D225 修订 |
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
| D81 | **摘要交接及自动摘要已由 D196 替代。**  托管主会话按工具定义指纹轮换 native thread，保留 Web 历史、原 rollout 和可回溯祖先链；交接包由最新账本事实、覆盖版本明确的模型摘要及前序 ID/path 组成。默认空闲 50 分钟在只读临时 fork 中生成预备摘要，不写原会话或 Web；0 关闭，配置文件提供默认值，owner Web 设置独立修订并持久化覆盖。摘要覆盖消息内容及原地更新、工具结果和完成 turn，生成时新活动使摘要过期；同版本成功去重、失败持久化退避。总结分支禁用 Tend 工具、只读 sandbox、无审批升级。schema 变化的启动及 CLI 手动交接共用持久化 create/inject/switch 阶段，目标准备成功后原子切换，不确定外部请求保留状态且不盲重发；缺失摘要允许明确标记的事实包回退。CLI 可仅准备摘要；Web 设置统一从侧栏底部入口（手机待处理页底部）打开共享面板，不挤占模型/think effort 入口。 | 已实现（2026-10-23） |
| D82 | Runner 通信协议与程序版本独立，支持当前及上一代；无版本协议为 0，正式版本从 1 开始。每个 run 固定协议。Breaking change 同时递增版本、实现上一代适配、删除退役一代适配和更新兼容/拒绝测试。适配登记明确退役版本，make check 检查到期及代码标记；持久化历史读取与通信退役分别管理。进程启动使用不随业务 journal 回滚的本机持久 Generation 排序，不同 instance 仅更高代次可替换，迟到首次 hello 不退休当前进程。 | 已确认（2026-10-23） |
| D83 | Runner 在空闲及维护期持续心跳，默认 15 秒（租约要求更快时缩短），接收后 60 秒无有效心跳判离线。实例及序号防旧状态覆盖。维护与在线状态独立；server 事务及 runner 本地阻止新领取，现有 run 完成并交付后才 ready，未知或人工接管不能被排空过程清除。 | 已确认（2026-10-23） |
| D84 | Go 独立更新器监督 server/runner，server 从固定发布产物逐台编排。单个未结束发布批次，发布前全部启用主机在线且兼容并无退役协议依赖；每次保留另一台正常主机。主会话等当前轮结束，server 重启前确认固定截止的维护授权；默认授权 300 秒、重启预算 120 秒、排空 30 分钟。终态任务关联 run 全部本地 done、身份匹配、无 held/fenced/活动 executor 或待确认操作时，旧 app-server UUID 缺成功最终报告这一静态证明不足仅保持资源隔离，不阻止无关 runner 维护；实际 harness 调用错误和未确认原生操作继续阻塞，仍禁止释放/复用/清理，不补停止证明。已明确收到 workspace release 成功回执但 `released:false` 时，只清除本次本地释放意图和请求，不改变资源占用；任务终态或脱离旧绑定后由扫描重新对账并释放，网络未知仍保留原请求身份，避免排队验收与维护互相等待。Runner 健康确认后先由 updater 持久提交，再凭当前进程的提交证明开放领取；回退持久绑定未开放领取的 fence。停止授权丢回执且候选仍暂存时，以 staged 比较交换取消和完整未开始证明恢复旧服务，不续授权或伪称数据回退。升级阶段持久化，未知不盲重做，接收新业务后不恢复旧数据。详见 docs/plans/controlled-upgrades.md。 | 已确认（2026-10-23） |
| D85 | 升级与维护在 Web 主机列表、看板和发布详情展示；在线、维护和 slot 占用分别呈现。开始、每台恢复、完成及异常写持久 system 通知，同事务去重且不唤醒模型；细粒度进度只更新详情。上下文后续获得升级事实，网页断线不推断所有 runner 离线。 横幅只展示非终态发布，完成或取消后自动收起，历史结果保留在聊天通知及发布详情。手机看板的执行主机折叠入口置于最近完成之前，整行显示已登记总数，主机列表与最近完成内容分别允许有界纵向滚动，共同区域保留看板高度；项目筛选不筛掉主机。详见 docs/plans/mobile-header-hosts.md。 | 已确认（2026-10-23） |
| D86 | 聊天支持原生图片输入：图片与文字共用一个有 request_id 的持久发送请求，服务端同事务保存消息和独立图片字节，允许纯图消息。列表、事件与交接事实只携带图片元数据，不嵌入原始图片；owner 鉴权读取原图，主会话将图片作为原生 image 输入交给模型并留在 native 历史。初版图片入口与内联存储、尺寸门槛和 GIF 首帧策略已由 D87 的通用原始附件替代。Web 支持文件选择、粘贴、拖入、移除与放大；首页正式 IM 与主/Issue 共用编辑器使用相同的剪贴板附件提取，粘贴同步优先读取 file items，无可用项时回退 files，不跨事件按文件名去重，混合文字按当前选区插入，用户显式发送，离线发送后刷新继续使用原请求身份；桌面与手机设置统一保留侧栏底部文字入口（手机待处理页），聊天与任务看板页头不再显示重复齿轮。详见 docs/plans/chat-images-and-ui-polish.md 与 docs/plans/chat-image-paste.md。 | 已实现（2026-10-24） |
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
| D106 | Runner 配置维护复用发布批次，单主机冻结 claim 并排空 run/待确认操作；允许 delivery_observer 配置变化，以及在 Agent 固定来源/执行后端不变时追加安装引用；旧安装引用保留，新引用经定义关系与实际文件 SHA-256 校验，不隐式接受 Agent 定义。见 docs/plans/agent-installation-maintenance.md。私有 updater 以批次、manifest、原/新配置哈希固定候选和备份，相同程序也执行受控重启。配置不进入发布包或账本，候选配置加载哈希及 provider 能力通过两次健康观察后，再以持久提交证明恢复领取。仅新能力协商后发送配置扩展，普通协议 1 请求不变。回滚恢复原配置且沿既有 admission fence 对账，unknown 不猜测。见 docs/plans/runner-config-maintenance.md。 | 已实现（2026-10-27） |
| D107 | 独立 TraeX app-server 在合并继承环境和显式覆盖后清除全部 `ORCA_` 环境项，避免嵌套执行器继承外层窗格、会话、launch 或 hook 路由身份；包含别名且大小写不敏感。普通进程设置、模型 home 和认证配置保留。Orca TUI 的原有启动与身份不变，不放松接管或未知结果保护，不改变 runner 通信协议。见 docs/plans/appserver-process-identity.md。 | 已实现（2026-10-27） |
| D108 | Orca worker 的首次 provider session 只有在新鲜、非空且匹配 Tend 指令的观测通过接管检查后才能持久绑定；失败观测不污染 journal。旧验收产物自行编译的启动代码不受服务升级覆盖，测试须外部隔离 ORCA_ 环境，后续实现吸收 D107。创建者不证明当前执行者，不据此丢弃可能的人工接管证据；诊断仅含身份和 prompt 摘要哈希。handback 不虚构 owner 曾操作终端，复用原 run 工具桥与已完成检查；历史 journal 的旧 handback 原文沿原请求重放，不因措辞变化重复发起。见 docs/plans/orca-root-observation.md。 | 已实现（2026-10-27） |
| D109 | 工作项与任务统一使用宽浮层，支持全屏阅读、直接链接及浏览器前进后退；从工作项打开任务保留工作项页签、折叠、滚动和返回焦点。已完成调查优先展示评审材料，活动执行优先展示阶段；跟进、承诺、交付和历史仍独立可达。输入原件及交付截图通过 owner 元数据与单文件短期签名展示，报告图片仅解析当前不可变清单的唯一相对名称或文件身份；不加载任意远程图片或 worker 路径。交付 HTML 使用无脚本、无同源权限的 sandbox 和禁止主动资源加载的 CSP 静态预览，原件下载及原有验收身份保持不变。不改变 runner 协议。见 docs/plans/detail-review-experience.md。 | 已实现（2026-10-27） |
| D110 | 仓库注册与 runner 本机绑定在线生效，细化 D30、D65：owner 经带请求去重及控制修订的 repos.register 接管仓库元数据，启动配置不覆盖或退役在线条目。runner 通过私有本机命令校验既有 Orca 仓库 origin、固定 provider ID，原子持久化后发布绑定，不重启或排空已有任务。hello 显式协商 runner-repositories-v1 后才发送可选仓库身份观测；server 只将与账本 remote 身份匹配的已加载绑定合入 host 的有效仓库集合。旧静态配置和协议 1/0 请求默认语义保留，新增绑定不改已有工作区或允许替换仓库身份；退役只阻止新挂载。详见 docs/plans/online-repositories.md。 | 已实现（2026-10-27） |
| D111 | **D196 已删除本决策的工作项后台 checker 执行模型；当前 Issue 负责人和咨询保留共用会话设施。** 工作项独立后台会话，主 agent 保持调度权并登记消息关联和有来源上下文；owner 标签纠正优先，关联变更推进版本并使旧结果失效。后台串行会话、有限并发、结构化回传与持久 rollout；主聊天仅展示自身状态。原生 ctx/compact 与 thread 交接分开，unknown 不重放。topic/channel 仅记录后续方向。见 docs/plans/work-item-agent-conversations.md。按 owner 后续指示，暂时允许后台原生工具，创建和恢复显式使用 workspace-write + on-request + auto_review；Tend 工具继续按后台身份与工作项范围校验，业务调度归主 agent。原生 auto mode 不等于工作项数据强隔离；后台默认关闭，尚未达到完整发布验收。 | 实施中 |
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
| D122 | D128 将 Issue manager 的实际原生进程迁至 runner：发布 category 任务后才领取，主 Agent 与 consultation 继续 host 托管；本项的来源审核、协调作用域及 CLI 限制继续适用。主 Agent 为默认个人助理和全局规划者，issue manager 在继承授权内独立澄清、规划、委派和推进；原设计二者均禁止实际执行、调查也走 task；D193 以协商的普通工具模式取代 runner Issue 负责人这项限制，旧模式及 Main 的原配置保持。Issue 直接 owner 指令由本项审核，不经 main 重复审批；协调 execution/effect_seq 与 owner 来源分离，epoch、context 和请求身份共同围栏。P4 隔离候选复用单一本地具名配置、immutable execution 快照、实际 scoped 工具及现有 pool/outbox/batch，main 委派即可收自己的回合，manager 独立审阅本项来源。交接先围栏再核对本项旧 manager 控制/unknown，正常 worker 继续。具名 hosted adapter 复用 CLI 原生登记/回读的进程局部固定 inline PreToolUse 与 readonly/never；缺失不回退 D111，不修改 worker/owner 全局配置。允许 owner 材料呈现；CLI hook 引擎故障仍 fail-open，不宣称 OS 隔离或原子 fail-closed。取代 D111 后台原生执行开放及仅能评估、主协调独占本项派发的规则，保留 D88 的执行隔离。见 docs/issue-agent-design.md、docs/plans/hosted-issue-agents.md。 | P4 r19 规定 review 及后继窄修正、正式完整检查和交叉编译完成，PR #167 已 CI/rebase 合并；最终 owner/UI 收口及真实隔离验收另记，生产未部署 |
| D123 | D128 新增 issue_manager purpose、配置格式 2 与无工作区 pin，manager 由 category 任务领取，首次固定 runner/Agent，后续回合保持归属并逐轮释放槽位；manager 不套用实现/验收配对资格。当前通信协议为 2/1，协议 0 通信适配退役，旧持久快照保留原 hash。以下 P2/P3 的 1/0 描述为当时交付记录。全局 category 与 runner/runtime 本机多 Agent 库存分离，hosted main/issue/consultation 同样登记；具体 Agent 的 `local_id` 是定义者明确保存的稳定 slug，`display_name` 是显示名字，职责及执行配置独立，命名不在运行时生成或随模型变化，见 [定义指南](agent-definitions.md)与[命名计划](plans/agent-naming.md)；只投影非密钥来源、版本和 hash。按 category 加可选 HARD runner 及 workspace host 选择，首次 host 必须同时支持实现及同 workspace host 的正式验收；稳定固定配置快照，恢复不重选，类别不授角色。复用既有 harness，通信按能力协商或完整协议升级。P2b 增加本机不可变私有快照、公共投影及原子 receipt/CAS/unknown 对账；通用 runner journal 11 追加可空 expected_control_revision，重启保留缺省与显式 CAS，旧 NULL 不回填，不改 wire 协议。P2c–P2d 复用 owner CLI/鉴权、runner 本机 socket/client/journal 和条件 hello ack，接通固定来源严格配置接受、全局 Category 及版本化安全库存登记。公共 DTO 唯一定义于 protocol，server 不成为第二配置源；同进程 unknown 沿原 Envelope，新 incarnation 真实围栏后才对明确 not_recorded 的前代尝试接续，登记 accepted 不等于 runtime available。P3 接通 OwnerTaskSpec 每 role 的 category/可选 HARD runtime/model/effort，过滤本机 definition 而不覆盖；空字段不加约束，旧 AcceptanceExecution 仍精确匹配，矛盾输入 invalid_request。当前 task/spec 首次 named 分配及 owner start-over 放弃 workspace 后的新资源分配，均核对同 host/backend 的实现和正式验收候选，只冻结当前 run；恢复用原 pin，后续验收分配才冻结自己的配置。多个 runtime 共用主机全局槽位及唯一 workspace 协调器，unknown 不释放资源。私有 instructions 与 model/effort 经真实 Traex/Orca 适配器注入，验收仍用独立 run/凭据/scratch/会话和受控上下文。RuntimeBinding.ExecutableRef 是实际启动命令，Installation.LocalReference 仅为本机来源元数据；不要求两者相等。wire 保持 1/0，由新 capability 和条件 ack 保持旧无选择请求与默认不变；追加 server migration 57 和 journal 12，旧 11 reader 在读 assignment 前按版本拒绝，新 reader 不给旧 run 补 pin，原无 instructions 快照不改 hash。P2e 增加 owner/named main/Issue manager 同一只读分类与 runner 安全目录；登记 receipt、executor 最后观测与在线时点分开，不授调度/配置权限或增加状态机。见 docs/plans/agent-catalog.md、docs/issue-agent-design.md、docs/plans/agent-definition-config.md、docs/plans/agent-registry.md、docs/plans/agent-inventory-control.md、docs/plans/category-agent-execution.md。 | P2/P3 的本地完整检查、独立 review、CI/rebase 合并已完成；P2e 只读目录/后继 caller 已纳入最终候选并完成旧固定完整检查，当前最终修正组合验证另记，生产未启用 |
| D124 | 开放 main、issue、active run 和独立 named-agent consultation 的直接聊天，保留真实 owner 来源；普通咨询无 issue/task、无实际执行，显式 promotion 去重后才进入业务合同。精确 run 补充不改 spec、不复活终态、不泄露实施 rationale 给验收。Orca 查看不等于接管，owner-only human_control/handback 保留；Cove 仅定义合同。见 docs/issue-agent-design.md。 | P6 对话与原生身份旧固定完整检查及双平台编译通过；咨询零业务写入、显式 promotion 与原 Orca 运行控制已有隔离实测，最终集成检查/CI 与生产部署未完成 |
| D125 | Attention 的 decision/action/information 与 assignment 分离，chat/Inbox/detail 共用身份，读取或归档不回答。手动来源注入先行，无飞书连接器；next action 与 deadline 保留来源，默认到期前 24 小时/到期提醒 main。Task/child 结果、owner 答复与期限明确路由到责任 manager/parent/main，复用 outbox/followup/batch，无变化静默，不增加定时器或巡逻 Agent。P5 接通稳定 Attention/原 decision 回答、当前责任人接续及 owner 人工动作确认；information 不增 ack 义务，不引入 future-dispatch/通用替换链。Deadline 复用单一 followup sweep/occurrence，具名 main 的窄 task rating 独立于用户动作与 Issue 规划顺序。owner 当前命令即有效确认，main 转录保留真实未撤回来源；期限修改同时受原有性质约束，停机不补发过时 near，关联 Attention 的行动只沿原对象收尾。见 docs/issue-agent-design.md、docs/plans/attention-planner.md。 | P5 正式五项 finding 修正独立复核、完整检查和交叉编译通过；PR #168 CI 全通过，rebase 合入 c54ee29b。最终 information UI 与关闭引用接点另片核验，生产未部署 |
| D126 | 旧账本一致性只读离线归档加新空业务账本，取代 D113 及 mandatory-message-context 部署计划中删除历史、不保留归档/恢复材料的安排。历史迁移不改写，archive reader 不走正常 store.Open 或服务循环；manifest 与原件独立校验，unknown 阻止切换。仅显式导入静态设置，新 deployment identity、runner journal/凭据隔离；已有新副作用后回退先对账。UI 先 Image Gen 设计、独立 xhigh review、用户确认再编码；分批 PR 不中途部署，完整后受控发布与真实 E2E。见 docs/issue-agent-design.md、docs/plans/issue-agent-refactor.md、docs/plans/offline-ledger-archive.md、docs/plans/archive-owner-api.md。 | P8 旧固定完整检查和交叉编译通过；正式 CLI 两项实际准入 finding 已修复并通过真实 red/green、定向 race/vet。最终修正组合检查/CI、线上归档切换及生产部署待完成 |
| D128 | Issue manager 改为真实的 issue_manager task，由 main/owner 按 category 发布，runner 首次领取时原子更新具体负责人。每轮共用 claim/租约/槽位/journal，等待释放槽位；责任按 Issue 收尾/交接结束，不独立验收管理任务。Codex/Traex app-server 承载，main/consultation 保留 host。协议 2/1、配置格式 2、保留账本受控迁移，unknown 先对账。修订 D122 的本机 manager 执行位置及 D123 的 manager 库存/领取方式。详见 docs/plans/runner-issue-managers.md。 | 实施中，未发布 |
| D129 | Issue 完整详情页统一负责人对话、当前进展、子项与交付，原执行及验收会话保持独立；摘要按 Issue revision 留存，活动／结果只读投影沿原账本。见 docs/plans/issue-detail-conversation.md。 | 已实现 |
| D130 | App、API 和 mainthread 通用业务测试经独立测试辅助子包，每进程通过真实迁移生成一次当前版本空库，只缓存关闭并确认无剩余 WAL 后的只读模板字节；每个夹具复制到独立目录的新文件，再走正常 store.Open，连接、原文件、时钟、ID 与配置初始化仍独立。迁移、历史及重开库测试保留真实建库路径，不维护第二份 schema，不削减 race 或分片覆盖。见 docs/plans/app-fixture-template.md、docs/plans/ci-parallel-fixtures.md。 | 已实现 |
| D131 | 通信模型独立于原生 Agent 会话：Web IM/CLI 使用正式发布、来源端点、投递与阅读回执；legacy conversation/rollout 保留诊断。Agent 仅选回复输入，Tend 固定渠道地址；主动通知默认 Web 与已绑定 Telegram。Telegram 单 owner 私聊，先持久化后确认更新，unknown 不重发；消费证据即时推送，渠道 MarkRead 为可选能力。PR/CI 仅预留领域更新与按需唤醒接点，协议仍为 2/1。见 docs/plans/messaging-web-telegram.md。 | 已实现 |
| D132 | Web 界面改版（修订 D70 的单对话加覆盖层布局与 D121 UI r5 的侧栏摘要）：路由驱动的页面与可分享路径，旧 query 链接改写；侧栏只做分组导航，手机四标签；角色化 token 与共享基础组件，由检查脚本约束；界面使用用户术语，不默认显示内部标识；路由级拆包与全页面巡检。不引入路由或 UI 依赖，History API 与原生 dialog/popover 已满足需求。业务合同、owner API 与 runner wire 不变。Issue 讨论以作者行头像标记主线、彩色强调标记重点、低权重合并事件与默认折叠过程标记背景，交付物不再占据主栏。见 docs/plans/web-ui-overhaul.md。 | 已实现 |

| D133 | 交互式 Web 沙盒：独立 tend-sandbox 复用服务端组装与真实 owner API、账本、调度、工具及 runner 协议，仅替换托管 Agent 会话与 runner 的执行／工作区边界。只接受带沙盒标记的数据目录，不进入发布包、不连接真实模型或生产数据。前端回归逐步迁移到沙盒。见 docs/plans/web-sandbox.md。 | 已实现 |
| D134 | 正式通信 IM 与版本化问题投影：渠道内回答复用既有业务用例；同事务记录处理渠道与失效原卡片，保留独立原生诊断入口，runner wire 保持 2/1。 | 已实现 |
| D135 | 具名委派工具明确要求原消息及内容版本；planner 使用可编辑输入合同；委派失败与请求回执原子记录，未消解失败使回合进入 needs_review，不自动重放业务。runner wire 保持 2/1。见 docs/plans/coordination-delegation-contract.md。 | 已实现 |
| D136 | Issue owner 是普通 Agent 在具体 Issue 上的持续责任；同一身份可以协调与执行。跨 Agent 委派必须经过子 Issue，独立验收具有自己的子 Issue 与运行关联；保留人工控制及历史合同，不改 Issue 交互。见 docs/plans/issue-owner-responsibility.md。 | 已实现 |
| D137 | Server 统一调度持久观察、有限 cron 和单 Issue webhook；事件来源、分发及消费只读可追踪，完整 Issue/自动化 CLI 共用原业务用例与请求身份；独立接收入口及私有凭据，不改变 runner wire 2/1。见 docs/plans/unified-issue-triggers.md。 | 已实现，未发布 |
| D138 | TraeX 流内限流的 `other` 终态仅通过绑定原 session/turn 的完整原始记录和精确 provider 诊断补足分类；原生根必须已失败且操作收敛。限流等待 30 秒再沿同 run/会话续接，截止时间与既有一次预算持久化；未知发送先对账，耗尽后交由 Issue owner。wire 保持 2/1。见 docs/plans/traex-terminal-rate-limit.md。 | 已实现 |
| D139 | 复用目录的 commit 任务缺少继承改动授权时，由 runner 上报明确未启动的有界结构化诊断；当前 Issue owner 修正同一任务合同，保留历史 run，不自动吸收改动或生成用户重试决定。独立 preparation_required 阻塞不能被主机健康扫描清除；旧协议、未知及旧责任仍走原围栏。见 docs/plans/workspace-preparation-recovery.md。 | 已实现，未发布 |
| D140 | 在 D136 的 owner 责任之上，Issue 对话始终面向负责 Agent：默认请求 issue-manager 类别；同事务建立待领取对话，owner 消息保留原队列，领取后投递；退回 owner 不转发或重发已提交的 owner 消息。看板追加可选只读待领取类别字段，Web 明示待领取及一键委派，沙盒经真实 runner 协议领取负责人。见 docs/plans/web-ui-overhaul-s10.md。 | 已实现 |
| D141 | 可选集中 TraeX worker loop：中心持有模型认证及原生会话，runner 保留 exec-server、工作区和 Orca 原生 TUI。按冻结 runtime 选择，原 run/thread 绑定、人工接管与停止证明沿既有生命周期；本地模式继续可用，wire 保持 2/1。见 docs/plans/central-traex.md。 | 已实现，未发布 |
| D142 | CLI 覆盖全部用户业务、管理与诊断能力，Web/Telegram 为子集；公开接口登记、完整输入输出、请求恢复、帮助及真实入口验证随能力在同一 PR 完成，覆盖门禁进入 make check。见 docs/plans/cli-capability-parity.md。 | 已实现，未发布 |
| D143 | Agents 只读目录按主机及执行方式/CLI 组合展示；显式定义热更新保留旧责任完整 pin，双方协商后核验续接候选，新分配仍仅使用当前定义。协议保持 2/1。见 docs/plans/agent-directory-continuation.md。 | 已实现，待验收 |
| D144 | Runner 原生 rollout 按需预览：原生目录保留原件，runner 持久索引与原生格式 reader，server 仅在内存按需转发；central worker 按冻结 inventory 读取 server 已有原件而不新建正文归档。步骤级 live 与历史共用只读 Web monitor（三轨概览、事件表与记录详情联动）。能力协商后启用独立诊断长轮询，不依赖执行 slot/租约，不启动模型或重复计量。会话/turn 归属、来源缺失与格式缺口显式可见，支持可确认历史，不提供原件副本及打包下载；现有日志与业务事实不变，runner wire 保持 2/1。见 docs/plans/runner-rollout-preview.md。 | 已实现，未发布 |
| D145 | CI 将静态检查、App/API/mainthread/E2E/其他包的完整 race 分组、Web/全部浏览器回归分配到独立 runner 并行执行；交叉编译仍包含真实 Web 构建。最终同名 check 汇总门禁要求全部依赖成功，失败、取消和跳过均不放行。同一 PR 的旧候选自动取消，main 各次检查独立；本地 make check 覆盖同一全集，调度由 D149 细化。见 docs/plans/ci-parallel-fixtures.md。 | 已实现 |
| D146 | Issue 工作状态与执行会话分离：实际执行领取、负责人派发子责任时，在原事务内将 active/有效责任下的 todo 及符合条件的父链提升为进行中，普通会话领取不触发，backlog/待检查/人工暂停/交接/终态形成边界。看板与详情追加可选的直接子 Issue 摘要、实际执行数与最多两项预览，完整列表沿既有父筛选分页；无子项不显示空区块，负责人运行事实归入属性。CLI 同步完整读取，runner wire 保持 2/1。见 docs/plans/issue-progress-visibility.md。 | 已实现，未发布 |
| D147 | SQLite 读池保留最多 8 个空闲连接，复用初始化后的连接；最大读并发、写池单连接、query_only、事务快照及关闭释放保持原合同。见 docs/plans/ci-cache-read-pool.md。 | 已实现 |
| D148 | CI 分离共享 Go 模块缓存与按职责、工具链和依赖分区的滚动构建缓存，PR 只恢复 main 构建快照，main 成功后发布候选快照并有界保留。Web 构建经本轮 artifact 供两组互斥且完整的浏览器回归与真实产物交叉编译使用，各 fixture 保留独立资产副本和缓存。最终 check 拒绝任一失败、跳过或取消。见 docs/plans/ci-cache-read-pool.md。 | 已实现 |
| D149 | 本地 make check 按有效 CPU 选择有界组并发、Go 包并行与 App 分片，复用质量、五个完整 race 分组和 Web 检查；低容量机器保持 E2E 独立阶段，显式顺序模式可回退，多 CPU 下需记录串行原因；八 CPU 自动选择两组并行、App/suite 各两片，阶段日志记录起止与实际预算。见 docs/plans/check-budget-parallel.md。Web 在每次检查私有临时目录构建，Go embed 目录在整个检查期间保持稳定；失败汇总保留全部检查，取消清理本次拥有的进程组。CI 原有分组与门禁保持。见 docs/plans/local-check-scheduler.md。 | 已实现 |
| D150 | 问题沿 Issue 责任链处理：执行问题先交所属 Issue owner，负责 Agent 无法解决时逐级交父 Issue owner、唯一主协调 Agent；只有人工 owner 或主协调无法解决时形成用户待办。沿原 Attention/Decision 记录版本、assignment epoch、逐跳原因和 Agent 回答来源，不伪造用户授权，不改变独立验收及 unknown 对账。新执行协商 supervisor-questions-v1，历史执行保留原答复合同，wire 保持 2/1。CLI 与具名工具支持原问题读取、回答、上报及恢复；新协商执行提问后结束本轮、释放 slot，父 Issue owner 答复后创建保持原尝试代次的接续 Run，无额外协调容量。见 docs/plans/issue-responsibility-routing.md 与 docs/plans/issue-run-handoffs.md。 | 已实现，未发布 |
| D151 | 完整 race 检查继续以 Go 动态发现的所有根测试、Example 和 Fuzz seed 为覆盖来源，固定实测成本仅用于分片放置和重包优先调度，新测试自动纳入。高容量本地机器按总测试进程预算选择最多八个 App 分片、每包最多四个 API/mainthread/E2E 分片及最多六个其他包并行；小机器保留 E2E 独立阶段和顺序回退，CI 保持原分组与容量约束。可选 TEST_REPORT_DIR 输出本轮私有原生 JSON 耗时报告，默认保留 Go 原生缓存。E2E、handoff、contextbuilder、runnerapi、tools、dispatch、views及消息通道的普通空库fixture复用不可变模板；模板字节复制机制不依赖store，供Store包内普通事务/投影测试共用。迁移及历史升级测试保留真实建库，显式路径重开继续通过真实Open；App索引重建对照仅复用初始当前schema空库。批量读取规模测试从一次真实创建的合法投影在独立事务内准备数据，创建/Issue流程仍由专门业务测试覆盖；复用变体不重复完整reload/restart合同。见 docs/plans/race-tail-latency.md 与 docs/plans/test-setup-deduplication.md。 | 已实现 |
| D152 | 验收后持久保留独立交付核对责任，具体 PR/head/base/验证/部署范围通过原 taskless versioned Attention 确认，候选或观测失效持久撤销旧授权；owner 重开原子恢复新协调代次，保留 terminal task 和 unknown。API/CLI 同步，runner 2/1。见 docs/plans/delivery-responsibility.md。 | 已实现，待验收 |
| D153 | runtime 通过 CLI 绑定服务端自适应并发分组，所有托管执行共享预算；明确拥塞减 1，30 秒合并/冷却，30 分钟稳定且累计满载 15 分钟、有成功 turn 和等待需求才加 1。Web 只读，CLI 全能力；七天历史保留 Issue/session 归因和升档依据，unknown 保留占用。见 docs/plans/adaptive-concurrency.md。 | 已实现，待验收 |
| D155 | 已提交 server 通过独立本机窗口引导 updater，再以实际归档/host 维护确认前滚；保留 unknown run、旧收据及父失败，限定失败候选可独立后继，CLI 提供完整引导、取消、对账及释放。 | 已实现，待验收 |
| D156 | 正常维护之外提供显式 receipt-recovery crash stop：真实未知生命周期不冒充排空，完整 Run 库存及原 lease 持久保护先于新服务入口，保留物理停止事实与业务未知，prelink 延迟原生执行。 | 已确认 |
| D157 | Manager delivery 先去重旧 logical responsibility 再查未决投递；Token 重叠查询按 native identity 与 current fact 索引关联。只改善查询规模，不改变 blocker、冲突、未知或计量语义，追加迁移83。 | 已实现，待验证 |
| D158 | Token 归属在同一只读事务内按请求批量解析日志与本地 actor，保留原 SQL 的歧义、NULL、优先级与错误回退，不使用跨请求缓存或新增迁移。 | 已实现，待验证 |
| D159 | 明确协商 retain 能力的 runner 可逐 Run 保留原维护未知，不令整 host Hello 失败；持久 fence 阻止重放，未知责任、原 lease/pin 与容量继续保留。旧 peer 保持原拒绝路径。 | 已实现，待验证 |
| D160 | Agent Issue 关联依据冻结 Run pin 与创建时责任来源，按 canonical identity 展示当前/历史及原版本；API、CLI、Web 同步提供有界分页与筛选，缺少权威关联和执行观测时明确保留缺口。 | 已实现，待验证 |
| D163 | 终态任务的未答规则问题在原事务撤销并收敛 Attention；拒绝迟到新答复，保留已答历史与暂停语义。 | 已实现，待验证 |
| D165 | Tailnet HTTPS 显式协商 HTTP/2 与 HTTP/1.1，以标准库复用持久事件流和页面读取；证书源、准入、认证与明文监听保持原合同，条件复现不宣称生产根因。 | 已实现，待验证 |
| D166 | 显式升级窗口可 park 空闲 manager，经原 close 与物理回执结束旧 Run；同窗口阻止重新领取，完成或取消后以新 Run 承接责任。默认关闭，原协议与请求身份不变。 | 实现中 |
| D164 | 原不可变 artifact 可经 owner 或主 Agent relay 的精确授权共享给目标责任代次；artifact/file/hash 与原来源冻结，撤销阻止新读，不授予源任务控制或伪造新报告。追加迁移86并提供 CLI/工具全入口。 | 实施中 |
| D167 | 创建者或显式用户决定后续 Task 是否强制独立验收；真实来源及政策冻结，免验仍需合法结果、责任核对和交付证明，不伪造通过记录，不追溯撤销在途验收。 | 实施中 |
| D169 | 仅在原 manager 成功 close、原事件与 native/业务回执均对清后，以独立 Task CAS/原 Run+lease 证明收束 waiting；缺 session/证明仍保 unknown，不重跑原动作。 | 实现中 |
| D173 | 原 close 与独立 stopped 及事件已对清后，以原 Task CAS/Run/lease/pin/epoch 结束旧 transport、排唯一 receipt-only 后继；只恢复原 thread 读取真实回执，未知业务不因此完成。 | 实现中 |
| D174 | worker handoff 的答案与续接权限分离：typed continue_now / wait / execution Task dependency；旧入口保留原默认，仅显式新 typed 入口选择等待；双 CAS resume / reconcile 仅沿原合同续接。 | 已实现，待验证 |
| D180 | 独立 server/runner 维护窗口持久限时，显式外部 force 接受未完成与未知；固定原进程与数据身份、先退出后接管，保留原请求、逐 run 隔离及独立恢复准入。 | 实现中 |
| D181 | 验收通过、返修及受阻结论均可显式提交不可变证据文件；首次领取冻结能力与精确反馈来源，文件原件和稳定上传身份先于 verdict 确认，事务关联与逐文件权限使原证据在 scratch 清理后仍可读取。旧无文件结论保持原义，不推断本机路径持久化；runner journal 18 阻止旧 writer 静默丢文件引用。详见 docs/plans/acceptance-evidence-files.md。 | 实现中 |
| D183 | 自动执行使用直接 app-server 与 runner 自管 git workspace；Issue 交接 prompt 独立于 Run session 续接。原身份恢复不隐式替代，纯辅助读取 unknown 不冒充业务未知；SQL 失败与真实无归属分开。对象权限拒绝不撤销全局身份，存储降级只支配失败的键。整体维护截止不随阶段推进续期。见 docs/plans/direct-appserver-handoff.md。 | 实现中 |
| D184 | Issue 修改合并通知当前负责人，由其判断原任务约定是否仍适用。D193 明确普通来源变化不阻止调度或真实结果；可选显式确认独立保存历史，不改写冻结 spec。 | 实现中 |
| D185 | PR 与 CI 观察由显式配置的 server 程序服务统一负责，同一交付共享事实并合并通知；读取、刷新与重试有期限，保留旧 runner 查询及迟到结果身份。 | 实现中 |
| D186 | 涉及 PR 的 Issue 明确自动合入或人工审批策略；同 PR 共享唯一正式 reviewer 和候选绑定结论，默认建议一次初审及一次集中复查，D193 取消两轮硬门；真实审阅轮次和用户预算保留，部署权限另行约定。 | 实现中 |
| D187 | 交付 worker 经 run 受限视图读取自身真实授权绑定、当前有效性和恢复事实，不获取 owner 凭据，也不从 Task spec 中猜测独立授权记录。 | 实现中 |
| D188 | 升级与恢复围栏在同一事务阻止新摘要，工具集交接仍可用原事实；普通候选健康固定截止覆盖所有返回分支，未提交安装可凭精确进程或 owned Wait 证明经独立维护前向接管，旧操作及未知保留。 | 实现中 |
| D189 | 部署准备经同源受限视图读取固定受控服务、完整目标节点、匹配安装回执和相关未结责任；准备报告保留未知，不取得安装权限、不激活围栏或要求全局清零。 | 实现中 |
| D190 | 部署基线失效与正式刷新复用同事务排队：同时唤醒共享 provider job并绑定刷新顺序；旧查询不结算新基线，已合入结果完成刷新后仍停止周期轮询。 | 实现中 |
| D191 | Issue 资料作为持续上下文供负责 Agent 自主读取；spec 与 Run 保留输入快照，不作为普通资料白名单。实现、验收及新 session 共用同源索引和原件读取，追加材料无需逐文件或逐阶段授权。 | 实现中 |
| D192 | 已完成部署以原冻结执行、原批准、原绑定安装及独立验收结清历史责任；当前候选准入不决定历史结清，旧授权不因此恢复执行能力。 | 实现中 |
| D193 | Issue 负责人自主选择方法、资料、协作与连续交付；普通事实变化合并通知，不以摘要、默认阶段或次数自动代替语义判断。具体副作用与真实用户限制保留原身份，补正和有界恢复追加事实。 | 实现中 |
| D194 | Issue 上下文提供可选快照及增量读取，固定事件区间、分页原件索引并保留稳定会话；共享 PR reviewer 可发现对应实现检查证据，自主判断复用或复跑，整批维护统一验证和交付。 | 实现中 |
| D196 | 托管会话交接与正常自动上下文各只注入至多 2 KiB 索引；原文按需读取，索引不确认审阅。移除自动摘要与摘要交接，保留独立 compact、历史审计及原生未知请求对账。替代 D81 的摘要流程，调整 D17/D18 的托管注入语义。 | 实现中 |
| D197 | 本机 Codex 使用固定独立 home、clean/projection 能力快照、严格 Linux 文件视图与可对账的本机管理；运行和恢复引用控制采用边界，不改变角色权限与线上协议。 | 已实现 |
| D202 | server 全托管用户级扩展与指令，按执行 category + Agent 合成，显式发布并自动分发，保留项目规则、角色权限及历史执行引用。 | 实施中 |
| D204 | 最终成果以自包含、可读的报告为主，原始背景与问题先于结论；验收和证据按需核查。已关闭责任的原有效会话仍可完成该项最终沟通和受限异常记录，正式发布以实际消息回执为准。 | 实施中 |
| D205 | worker 将待提交证据放在 scratch，工具、构建和测试可自行使用并清理私有系统临时目录；运行时 TMPDIR 不受 scratch 路径约束。 | 实施中 |
| D206 | Issue 只保留 PR 引用及自身需求、贡献和交付责任；当前 PR 事实按 provider target 唯一保存，查询身份和通知游标与事实分离，审阅及交付读取同源。 | 已实现 |
| D207 | 用量来源的自动采集状态独立于计账覆盖；metadata 登记不启动采集，有限失败预算后按证据退役或转待对账，保留原请求、未知用量与显式恢复。补充 D195。 | 已实现 |
| D208 | Manager 已接受的原生工具模式属于线程 binding 身份，关闭、park 和回执恢复只改变当前许可，不改写该模式或历史绑定；新执行仍按协商能力准入。 | 已实现 |
| D209 | 当前负责协调者可显式解除 Agent 自己设置的技术等待；管理换代后的原已停止 handoff 可在当前 epoch 下显式承接相同命名执行责任，保留原合同、原 Run 及 owner 等待边界。 | 已实现 |
| D210 | 已关闭 manager 在停止后以 runner lifetime 有界恢复原回执，物理停止与业务回执独立结算；不重复原生工作。 | 已实现 |
| D211 | Manager 原生回执投递使用既有 RequestTimeout 的单次总预算，到期保留原事实并退出到真实停止与回执恢复，不限制模型执行。 | 已实现 |
| D212 | 写连接超时保留原事件/请求归属、有界内部调用链及失败时刻的单 Store 活跃写入快照，区分排队与事务阶段，不改变写入或确认语义。 | 已实现 |
| D213 | 自动只读回执 Run 继承已接受的原线程工具模式；包括已有缺字段 action 的恢复，只读取原线程，不以新建、分叉或工具集迁移兜底。 | 已实现 |
| D214 | 具体交付批准与结束在准入和执行授权间使用一致的纯选项语义，延期可保留说明；不可执行的历史答复允许显式准备原候选的新问题，原答案和来源不可改写。 | 已实现 |
| D215 | 已接受的同 PR 历史贡献可引用同 Issue 已结清执行完成责任结算，保留原接受与冻结交付证据，不新增执行或授权。 | 已实现 |
| D216 | SQLite 文件路径经标准 URI 编码，journal/独立进程代次库与 Store 读写池不误解字面特殊字符，保持真实文件及原锁/连接选项。 | 已实现 |
| D217 | 主 Agent 使用与 owner 相同的业务、管理与诊断能力，通过服务端绑定的 owner 权限调用实际 owner 接口；真实 actor、原请求域、原生执行与协调 effect 保留。能力从实际注册表导出，普通 Issue Agent、worker 和 runner 不获得此权限。原 owner 领域校验、状态机、执行收束及验收事实继续适用；主 Agent 可在用户目标内自主作代理决定，无需逐操作新增用户消息。见 docs/plans/main-owner-and-issue-admission.md。 | 已实现 |
| D218 | 新 Issue 默认 backlog，仅保存意图与可选执行类别；显式 backlog → todo 才开放 runner 主动领取，禁止跨过 todo 进入执行或完成。候选由 Issue 当前状态与选择派生，领取事务才创建 Task/action/Run/租约，无独立发布或主 Agent 再分派步骤。退回 backlog 或暂停关闭整条子责任准入，通过协商的 run-pause-handoff-v1 记录确认停止后交还当前 Run 与槽位，保留 Task/spec、预算、工作区、成果与原生引用；再次 todo 生成唯一后继，独立 Task 暂停、人工控制和 unknown 义务继续适用。取消优先收束，不恢复被暂停的业务工作。历史定义以新 revision 归一化；旧协议默认行为保留，未协商交还的原执行仍占槽位。见 docs/plans/issue-admission-runtime.md。 | 已实现 |
| D219 | 所有任务从持久记录重建业务上下文；同目标复用 Task、追加 Run，session 复用仅为优化，新会话是正常路径。 | 实施中 |
| D220 | 来源理解并入正常工作，取消独立核对/提交门槛；负责人可自执行，同 PR 审阅沿原责任持续处理。 | 实施中 |
| D221 | 外部等待保存条件和恢复位置，真实停止后释放资源，由 server 合并观察与唤醒。 | 实施中 |
| D222 | Issue 连续交付沿原明确范围授权，部署入口显式提供；最终成果发布以真实消息和附件为准。 | 实施中 |
| D223 | 不可变正文与 rollout 原件以文件保存一次，DB 保存引用、窗口和业务状态；迁移和备份可恢复。 | 实施中 |
| D224 | 用量覆盖依据已持久化原生响应与水位；候选读取及 writer 工作有界、按相关变化推进。 | 实施中 |
| D225 | 内部幂等回到业务唯一 key；退出通用请求档案、重复正文、永久刷新通知和历史估价，以紧凑 DB 状态、单份文件原件及可核验回收治理存储。 | 实施中 |
| D227 | 物理停止归还执行资源，原请求的未结事实跨载体持续可发现；历史读取与新业务准入分离，停止交接、升级 park 和原件复用沿真实出处推进。 | 实施中 |
| D228 | 独立维护控制按原计划与当前 archive/host 业务状态收敛；未知回执后不重放过时 CAS，取消与 finish 只执行尚未满足的转换，不恢复通用请求账本。 | 已实现，待统一验证 |



| D176 | Token 用量在 app 写事务维护请求贡献与四小时汇总桶，读取只用汇总、精确边界贡献和有界明细；后补归属、冲突/重叠及报价变更原子刷新，迁移后启动补齐，无新增定时器。见 docs/plans/token-usage-projection.md。 | 已实现，待验证 |

| D168 | 新普通任务规则问题在原不可变开问事件冻结真实 spec 与可证 action；替代仅关闭明确过期问题，pause 与旧来源未知不猜失效，原答案及请求回放保真。 | 实现中 |

D146 卡片进度补充：卡片仅展示真实直接子 Issue 的完成进度与子项，移除阶段标题、阶段计数及阶段进度条；无直接子 Issue 时隐藏子项区块。计划阶段保留在详情，明确与直接子 Issue 区分。复用已有 `children` 摘要与父筛选分页，既有阶段 API 字段保留兼容。见 [实施计划](plans/issue-card-child-only-progress.md)。

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

D113 部署身份读取补充：`App.DeploymentID` 先在独立读事务中读取持久 singleton，已有身份不进入写池。只有身份缺失时，结束读事务后进入写事务并再次检查，由单写事务完成唯一初始化；成功提交前不发布身份，读、写或提交失败均返回错误和空身份，后续调用可重试。不增加内存缓存，不改变持久身份、响应 header、mutation guard 或 session 特例；不修改连接数、busy timeout、协议或 CLI 能力。见 docs/plans/deployment-identity-read.md。此机制修复不代表线上持续加载已恢复。

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

总览恢复固定六列工作状态看板，工作进度与实际执行事实分离，正式完成/取消仍由 canonical 生命周期决定。六列横向布局及既有筛选、URL 语义不变；“已取消”在未保存偏好或存储不可读时默认展开，用户主动收起的偏好继续生效。见 [默认展开收敛计划](plans/issue-board-cancelled-default.md)。无阻塞的 owner 拖拽/菜单直接保存，有阻塞保留原状态并给出处理入口，不增加完成/取消二次确认。阶段只读摘要，内部详情与阶段编辑不属于本批。

状态与审计即时入账，给当前 Issue Agent 的通知按最后一次实际变更防抖，默认三分钟；系统设置可选立即或一至三十分钟。窗口持久化，拖回起始状态不通知，Agent 自身更新不自唤醒；沿原具名管理会话/background/outbox，保留冻结输入与 unknown 回执对账。追加迁移不回写历史、不触发迁移通知、不改变 runner wire。布局依据已批准稿，样式复用当前 token 与 shell。见 [实施计划](plans/issue-kanban.md)。

可撤销的状态通知使用独立合并组，不吞掉首次接手等原有唤醒；负责人变成人时不转送个人规划会话，仍保留取消与当前负责人准入语义。项目筛选保留可输入方式，使已有 Issue 所属的停用项目仍可访问。
## D129：Issue 详情与负责人对话统一

Issue 默认作为应用内完整详情页，保留全局导航与原 query 链接。主栏突出 owner 待处理、本项负责人维护的进展／下一步、直接子项、讨论和交付结果；任务执行、来源、跟进及历史按需查看。负责人会话为固定入口，worker 实施和独立验收会话仍独立；服务端返回实际逻辑会话身份，GET 不创建会话。摘要独立保存不可变 revision，CAS／assignment epoch／request 去重与事件原子提交；历史投影不混入当前事实。活动和交付索引只读取原账本，保留作者及 artifact／spec／验收身份，不推断责任完成。计划见 [Issue 详情统一](plans/issue-detail-conversation.md)。

## Token 用量与资源入口（D70、D117）

资源与记录共用整行导航：全部任务执行、已注册仓库、Token 用量；主机状态另行分隔。Issue 页直接提供资源 dialog，执行记录页保留资源入口，原生 dialog 负责返回、全屏和入口焦点。

全局用量默认全部已采集历史，主摘要直接使用当前响应 totals；时间未知摘要在全部历史下是其中子集，在所选时段下是未计入时段的历史数据，始终受同一模型与 subject 筛选。未知时间不进入趋势，缺失总量不由输入输出合成，缓存不重复加总。摘要、统一筛选、时间分布、分类、明细与覆盖说明依次呈现；分类以已知总量和请求覆盖数为主，各组保留输入、输出及缓存的折叠详情；同筛选更新失败显示带截止时间的上次读数，新筛选失败不借用旧读数。模型名称只为选单累积，不混合数值快照。实施与验证见 `docs/plans/token-usage-integration.md`。

### D117 补充：可恢复原生计量

Codex 与 TraeX 以原生持久化 `token_usage_record.usage` 为唯一新增消费贡献，通知只触发读取；持久执行不依赖 `experimentalRawEvents`。分页账本使用 provider/thread/ordinal 身份，旧格式使用 provider/thread/turn/response/原生时间；同一原生 Session 合并 thread，显式 fork 保留独立 Session。缓存包含在输入，推理包含在输出，不累加累计数，不由上下文占用推算消费。执行模型使用请求当时的 turn 上下文或冻结执行配置，原生完成记录时间与采集时间分别保留。

Runner 在 journal 中将批次与游标原子持久化，host 身份通过增量能力 `native-usage-ledger-v1` 上报当前或历史执行；租约过期不阻止合法测量，也不授予业务执行权。服务端继续接受旧用量事件，旧事实以关联映射排除重复贡献并保留审计证据。此变更为可协商增量，通信协议继续支持 2/1；服务端迁移追加至 74，journal 追加至 14，回退构建必须能读取新增表，不能通过删账或降库回退。维护 CLI 默认只预览，应用复用读取器、journal 与 host/owner 上报通路，不创建业务执行。来源以 host 身份经 `runner.usage_register` 登记，和消费批次一样先持久化再确认，不占用业务原生事件序列或依赖执行租约；central-native 历史由服务端读取，Broker 持久保存来源及停止证明，Runner 不读取服务端路径。

内部摘要 fork 与合并使用私有持久会话，关联来源并计量，不增加普通会话入口。无法恢复的临时历史保留覆盖缺口。普通页面隐藏原生 request/thread/turn，仅展示 Session；默认按 Issue，Session 为第二个 Tab，两者及全局、单执行共享有效请求贡献与筛选。列表展示 Issue、执行模型、同格紧凑 Token 细分与 OpenRouter 美元估算价格；逐请求按当前报价计算、缓存不重复计价，并标明报价时间及覆盖。详情展示任务、历史 Agent 名称、执行模型、CLI 名称及版本、职责和 Session；category 通过 Agent 名称浮层提供。页面信息层级按设计图 review 后实施。实施、历史样本与发布顺序见 `docs/plans/token-usage-repair.md`。

OpenRouter 报价中的日历条件按原始 JSON 保存其存在性；暂不支持的星期或时段仅使对应模型未报价，不能因星期名称的表示方式拒绝其他模型的整个目录。来源未取得最终结束证明时显示待对账，避免将覆盖未确认误标为上传待同步。

所有角色共用统一模型计价解析：完整目录 ID 的原样精确匹配优先，其次是不区分大小写的唯一完整 ID、唯一模型 slug；同层多候选保持未报价，batch 等变体不合并。原始模型名不改写。规则升级通过追加迁移登记既有请求，沿 D176 的启动投影刷新一次性重算价格与汇总，不重采历史。见[实施计划](plans/usage-pricing-case-normalization.md)。

## D134：IM 内交互与跨渠道处理来源

通信消息可携带引用既有 decision、attention 或 worker 问题的交互投影，冻结用户看到的业务版本及允许选项。Web 消息内控件、Telegram inline keyboard 与引用文本使用同一回答准入和原业务用例；明确选项可直接处理，包含新条件或含糊的文字交给当前负责人继续在 IM 内澄清。Agent 不提供渠道地址或任意卡片命令。

正式受理答案的同一事务记录实际处理渠道、答案和时间，追加事件并登记已发布端点的展示更新。另一渠道显示“已在 Web / Telegram 处理”并使原操作失效；答案受理与执行结果分别表达。回调 ACK、阅读或普通文字接收不代表已处理。网络延迟下旧操作仍由业务版本围栏拒绝；渠道编辑失败恢复同步，不重复执行业务动作。

Web 默认入口为正式通信 IM，原生会话、工具与 rollout 保留独立诊断入口；历史消息、附件和责任身份复用。Telegram 以正文、按钮、callback 和原消息编辑实现交互，不依赖 Business 或 Mini App。新增字段与 owner 操作为增量，runner 协议保持 2/1。实施及验收见 docs/plans/im-interactions.md 与 docs/plans/web-im-ui.md。

## D132：Web 界面改版

页面由路径决定：主对话、收件箱、Issue 看板与详情、任务执行与详情、资源、设置、归档各有路径，浏览器前进后退与刷新保持所在页面；旧 `?issue=`/`?task=` 链接改写为对应路径。只有短暂表单与确认使用 Sheet/Dialog，不再叠加页面级弹窗。主对话常驻挂载，换页不重建流与草稿。

S2 使用每个 History entry 的本次页面会话身份判断返回：可确认前项属于本次应用会话时浏览器后退，否则替换为父级路径；刷新不采用旧页面会话的返回栈。历史项保存可见页面的滚动、展开状态与触发元素，换页聚焦标题，后退恢复仍可识别的触发元素。Issue 筛选写入 query，原页修改筛选不抢走输入焦点。沿 D129 保持负责人会话唯一展示面：Issue 负责人会话路径读取实际归属后替换到 Issue 详情，独立咨询使用 `/chat/{conversation}`。

侧栏只放分组导航（对话、工作、资源、设置），待处理由收件箱计数与主对话待处理区承担；侧栏宽度在各页面一致。样式只使用 token 定义的字号、间距、圆角、层级与颜色，控件来自共享基础组件。界面文案遵循中英混排加空格与统一术语，内部标识、哈希与工具名默认收起。计划见 [Web UI 整体改版](plans/web-ui-overhaul.md)。

S6 中非聊天路由使用 lazy 与 Suspense 骨架，第三方模块独立共享；构建生成的离线 shell 清单仍包含全部路由资源。手机主操作与标签采用触控尺寸，输入区和浮动跳转避让安全区，详情及短暂弹层隐藏标签栏。全路由深浅主题巡检与导航回归纳入 CI。系统决定的问题与原因说明使用中文；内建选项字符串同时是回答标识和 Effects 键，保留其字节，由 Web 完整映射为中文显示。

S8 将进行中的主对话、Issue 负责人轮次与任务执行统一为单行阶段和用时，步骤在一个默认收起的披露中保留；手动展开不随新步骤增长而重置，结束后收起并显示摘要。阶段仅使用现有事件与工具映射；缺少原生开始／结束时间的历史窗口显示用时未知，不据页面加载时间推造执行时间。阶段播报与计时 tick 分离，减少动态效果偏好生效。自动跟随只响应末尾数据变化，用户披露保持控制点位置，分页继续恢复原锚点。

S9 的活动只读投影使用事件中持久的请求身份显示作者：你、主 Agent、冻结负责人目录名、执行 Agent 目录名、执行主机或 tend；不再把所有事件展示为无主体的系统动作。负责人交接不重写历史归属，缺少目录信息使用角色回退。当前进展展示 updated_by；沿用现有 Author 字符串，不新增 owner API 字段或迁移。

S9 补充：Issue Footer 的结束提示、收件人、队列、输入区和不可发送原因使用与正文相同的居中内容内层。不可发送时不展示收件人；owner 负责的 Issue 明示没有负责人 Agent 对话，并提供次级交接入口，不制造对自己发送的含义。

负责人会话 can_send 仅描述业务授权，不代表读取成功；会话读取为空时显示加载／失败说明，不能因授权可发送而隐藏读取错误（S9 R2）。

S11 体验修正：讨论正文不采用贯穿历史的缩进与竖线；作者头像保留在作者行，系统事件与后台来源文字直接对齐讨论正文，后台来源折叠箭头后置，失败强调不改变左侧位置。Issue 专属诊断入口归入分组弹出菜单，保留 Issue 过滤与事件页目标；进展编辑、负责人交接和任务创建使用各自上下文入口。讨论不再裁成最近三轮，进入历史边界时自动读取更早页并保持阅读锚点，不提供人工展开历史的按钮。

## D133：交互式 Web 沙盒

收件箱上下文修正沿用 D125 与 D132：owner Attention 只读投影返回当前 Issue 与执行任务的可读标题，列表及详情首先呈现责任事项，再呈现原问题和动作。默认查看尚未解决的记录，“全部记录”保留历史及直接链接；已读和收起不能隐藏未解决义务。只增加 owner view 的可选字段，不改变原决定、任务控制、存储或 runner wire。独立 `inbox` 沙盒场景复现两条已处理历史与一条等待答复的真实脚本失败决定。见 [收件箱上下文](plans/inbox-issue-context.md)。

`tend-sandbox` 是开发与前端验收入口，不是服务端运行模式，也不进入发布包。`internal/serverkit` 集中组装真实 SQLite、app/domain、views、owner API、runnerapi、scheduler、dispatch、contextbuilder、tools、mainthread 与嵌入 Web。显式注入 listener／WhoIs、时钟／ID 与托管会话 opener；正式服务端保留原配置和真实边界。

沙盒只替换托管 Agent 会话和 runner 的 Workers／Workspaces：脚本通过真实工具回调写账本，进程内 runner 经回环 HTTP 领取并执行任务。数据目录必须为空或具有内容匹配的沙盒标记；重置只能作用于已验证目录，不读取生产配置、凭据或 tailnet 状态，不启动真实模型、git 或 updater。demo 通过带 request_id 的 app 命令播种，持久请求身份使重复启动不重复创建对象。所有后台循环随 context 取消并等待退出。

沙盒脚本默认分阶段停留：托管轮次约 8 秒，任务执行约 20 秒；显式快速节奏供自动化测试使用，所有延迟继续通过可取消的沙盒 helper。只影响脚本边界，不改变正式调度、协议或业务行为（S8）。

S9 demo 增加真实主 Agent 创建、负责人多轮澄清与进展／状态工具、owner 决定回答、同一任务失败后显式重试、报告文件与独立验收、负责人总结到待评审的完整故事；另保留等待回答的执行。长轮次与较早活动分别验证续页和历史读取。播种期间显式快速节奏，完成回执写入后恢复交互默认节奏；幂等请求与原始账本终态继续作为唯一事实。播种保留真实时钟，不人为修改租约或事件时间。

S9 看板示例只保留一个 owner 负责的 Issue，其余通过真实创建路径委派注册的沙盒负责人；旧播种请求身份保持不可变，因此已有目录需显式 reset 才采用新的看板分配，不在重启时改写旧 Issue。

S9 R2：播种推进必须验证创建业务结果，元数据修复完成不等于业务操作成功；缺少结果时沿用稳定恢复来源请求重新触发真实主 Agent 创建。重型生命周期测试使用内部旧 demo 选项，不重复播种多轮故事；独立快速故事测试覆盖完整交互、元数据修复后的创建恢复与回执重播。CLI 默认仍播种完整故事；内部旧 demo 模式也保留原先 owner 分配，避免新增的负责人工具轮次拖慢通用生命周期测试。

Web 开发可使用同源嵌入页面或 `TEND_DEV_SERVER` 的 Vite 代理。既有 JSON fixture 回归保持不变，后续逐步以沙盒承载 UI 巡检与交互验收。实施与脚本规则见 [Web 交互沙盒](plans/web-sandbox.md)。

## D135：委派合同与未完成责任

具名 issues_create 必须提交准入消息的原 ID 和独立 content revision。工具 schema、字段说明及服务端错误明确同一合同；来源校验和业务版本围栏保持。Owner API 仍保留可选来源的原语义。planner 的具名写入 DTO 只包含可编辑字段，明确 kind/status 枚举，身份、版本和时间由服务端分配，不复用完整存储记录作为模型输入。执行层继续接受原 owner 有效 payload，不重写历史 callback 参数，保留原请求 hash 及回执重放。

具名主会话的 Issue 创建／manager 发布结果与请求回执同事务追加记录。失败 Apply 的部分业务写入回滚，随后提交失败责任；授权失败不产生准入。同逻辑责任（包含澄清继承）、同来源、同操作、同目标及同委派内容的后续成功才消解此前失败；原失败及错误不删除，错误或缺失来源不靠另一条输入成功自动消解。

消息分类及事实审阅完成不代表委派成功。原生回合结束时仍有未消解失败，则持久进入 needs_review 并追加事件，供现有通信查询和事件流诊断。不调度业务重放或放宽 metadata repair 权限；后续业务需正式输入和既有授权路径。追加迁移不回填历史失败，runner wire 继续 2/1。见 [实施计划](plans/coordination-delegation-contract.md)。

## D136：普通 Agent 的 Issue owner 责任

Owner 是具体 Agent 对具体 Issue 的责任，不是一种专用 Agent 类型。保留 `issue_manager` 作为既有持续责任执行角色的 wire/存储名称；它与 worker execution 可以使用同一 Agent identity、模型和配置修订。格式 2 的 Agent 可通过可选 `owner_runtime_id` 指定协调适配器，例如 Orca 执行加 app-server 协调；不创建第二个 manager 身份。app-server worker 在声明 CLI、实际验证协调能力后也可直接承担 owner。类别表达能力，不要求专用 issue-manager 类别。协调就绪失败与执行适配器失败分开记录；不因一方失败补造另一方能力。

新 `issues.request_owner` 和 Issue 创建的 `owner` 输入显式建立 `issue-owner-v1` 合同。可以按能力类别选择，也可约束具体 Agent 身份；首次领取冻结实际执行配置。runner wire 保持 2/1，新增可选能力 `runner-issue-owner-v1`，旧 runner 不领取新责任。旧 `manager` 请求、已有任务和历史会话保持原合同，不静默升级或重写待对账执行。显式变更既有责任需要 replace/既有交接及其围栏。

当前 owner 可以通过冻结任务使用自己的执行适配器工作；实现身份、Agent/runtime revision、模型和 Issue assignment epoch 由服务端固定。跨 Agent 的工作委派必须先创建子 Issue，由接收 Agent 成为该子项 owner；子项 owner 可递归委派。省略子项 owner 时保留当前 Agent 身份，不默认选择另一 manager。父 owner 始终负责协调与结果整合。Task、Run、session 仅承载执行，不成为跨 Agent 派工旁路。

D122/D128/D136 的 canonical 上下文查询按本项责任投影：来源更正保留已准入份额，事实保留 own/global/project 的有效复核，协调状态与 native pending 使用持久本项执行身份（含旧代次及 repair chain）；没有 hosted identity 的历史请求才按原请求的本项 conversation 归属。全局 logical 回合接触过某个上下文不构成本项执行身份，不将其他 Issue 的来源分类责任转移过来。`prepared` 继续如实显示历史 baseline，`dispatch_ready` 仅为上下文及既有冻结合同诊断，不代表 canonical 创建任务的实际准入结果；正式命令仍核对当前来源、Issue/assignment 版本及冻结合同。继承 EffectiveContext、TaskContextBlockers、真实 worker admission 与历史 baseline 调度路径不变，不因投影修正而结清 recovering/unknown 或扩大来源读取权限。见[实施计划](plans/scoped-canonical-context.md)。

既有独立验收流程与材料隔离保留。新自身执行任务同事务建立有界验收子 Issue，并冻结其原始范围。验收领取将实际 reviewer 身份和配置与该子项绑定，运行关联可通过 owner 查询和 CLI 追踪。最终有效验收结论在通常收尾条件满足时可结清此有界责任；人工扩展的目标不能被原验收结论自动关闭。取消子项请求对应执行停止，实际执行及未知结果核清前不得确认取消。父 Issue 的完成仍走原证据与显式责任收尾路径。

D123/D136 冻结产物验收：canonical task 的独立验收使用当前 artifact 及其不可变 spec，不因提交后 Issue 或祖先新增/修正的语义内容要求重跑实现。新的范围、来源复核与完成责任继续留在 Issue，验收不能替它们结清。实现领取仍检查当前继承语义和来源；验收仍检查负责人/reviewer、责任代次、Issue 生命周期与依赖、祖先结构、task 控制、当前 spec/action/artifact 及资源占用，不放行显式取消或已替换合同。无 API、迁移或 runner wire 变更。见[实施计划](plans/canonical-frozen-acceptance.md)。

旧 epoch 的实现、验收及失败结果都作为历史事实保存并暂停后续推进，不因旧执行退出产生新的重试决策。交接使旧合同的未决问题失效；旧执行不能发起新问题或创建后续动作，已记录答案仍保留历史事实。任务合同刷新不能偷换实现或已领取验收的身份和配置；不兼容更新明确拒绝，由当前负责人显式建立新执行合同。

本轮不修改 Issue 页面、人工指派、暂停、取消或交接入口。人工干预仍是迭代期必要能力。模型或 runtime 的补充指令可以影响自己执行、拆分粒度和能力选择，不能改变责任与授权规则。原生子 Agent、mention 或咨询不能绕过正式子 Issue 委派。协调适配器继续保持其实际工具隔离；不把提示词要求宣称为任意原生命令的 OS 隔离。实施与验收见 [计划](plans/issue-owner-responsibility.md)。

## 15. 统一触发与只读诊断（D137）

Server 每 30 秒统一扫描跟进、截止提醒与交付观察。到期交付先进入持久观察队列，runner 仅领取、查询并上报；poll 可以在同一事务补充到期候选，保持原即时领取合同。查询继续使用原 generation、nonce、实例及两分钟租约，不占执行 slot。过期查询记录失败后允许新的只读观察，不重放未知业务副作用。

跟进增加五字段数字 cron（分钟、小时、日期、月份、星期），支持 `*`、列表、范围及步长，日期和星期同时限定时取 OR。必须显式指定 IANA 时区和 expires_at，默认最多 20 次。时区数据由 app 解析并作为输入传给纯领域计算，domain 不读取时区文件。夏令时不存在的当地时刻跳过，重复当地时刻只采用第一次；停机漏过周期合并一次并向未来安排。原 at/every/condition、deadline/Attention 归属与次数语义保留。

Webhook 订阅绑定一个已有 Issue，owner 创建、修改、暂停、恢复、取消和轮换凭据均校验 revision/assignment epoch。独立接收入口不挂载 owner/runner API；配置后默认监听回环，由外部反代负责 HTTPS。私有 key 以不可变文件暂存，账本仅保存引用；暂存本身不创建有效订阅，符合 D87 的事务前暂存边界。入站请求以订阅和稳定 delivery ID 去重，同身份不同内容冲突；当前凭据验签通过且事务提交后才确认接收。外部内容仅为不可信事实，不能选择目标或扩大授权。

暂停/取消撤销尚未领取的订阅来源，不撤销已冻结处理；暂停期间收到的请求留接收记录但不补发。Issue 暂停或交接时保留 pending，恢复后分发；Issue 终态在原状态事务取消订阅，重开不恢复。owner 负责的 Issue 保留待处理事实，之后交由 Agent 负责时再向同一 Issue 消费链路分发，不创建第二协调者。

诊断投影复用规则、触发、观察、来源、批次和原生处理事实，增加每次实际触发/观察的关联记录与阶段，不记录空轮询。分开表达已接收、待分发、已入队、处理中、处理结束、未分发、失败、未知和取消；业务完成仍由 Issue 收尾约束决定。历史没有时间证据的阶段显示未记录，不猜测。owner 认证的只读 API 提供按 Issue、来源、状态、时间、关联身份筛选及稳定游标，返回同一读取快照的水位。事件通知仅用于重新读取。

Web 提供全局调度/事件及 Issue 过滤视图，页面只读。CLI 复用现有完整 Issue 生命周期，提供完整 schedule、webhook、event、observation、request 管理/诊断与 JSON 输出、原信封 dry-run/replay，不创建第二份客户端请求账本。新增内部存储及 owner API 不改变 runner 协议 2/1。详见 `docs/plans/unified-issue-triggers.md`。

## D139：启动前合同错误的负责人修正

工作目录复用与继承改动的提交范围是两个独立约定。`workspace.mode=reuse` 保留目录和未提交状态；只有负责人核对目标与改动归属后，才能显式设置 `include_existing_changes`。`follow_up` 不继承资源或附件；跨责任代次的执行合同仍须新建，当前代次的准备缺项通过 `issues_update_task` 更新同一任务并产生新 spec/run。相同 spec 的重写保持无效果，不触发重复执行。证据不足时可先保存原实现目标及冻结 spec 引用，将同任务显式修订为有界报告调查，避免争用现场；调查经独立验收完成后，以同 Issue 的后续任务恢复原实现/验收要求，并在占用与原生执行结清后复用调查资源。报告任务完成不代表原修复或 Issue 完成，也不重开已完成任务。

Runner 在实际工作目录适配器中构造有类型的准备失败，记录 `inherited_changes_not_included`、准备阶段、未启动事实、来源任务、工作区及所缺字段；文件证据仅含有界的仓库相对路径、暂存/未暂存状态及 HEAD，不携带内容、原生目录或 stderr。状态不证明作者或授权，截断和总数显式保留，归属不清时仍需有界调查或澄清。诊断在既有 release journal 内持久化，以同一请求重试上报。

新 server 在 Assignment 中提供可选 `preparation_diagnostics`，runner 只有收到许可且 journal 证明没有线程或启动尝试时，才附加可选的 release 诊断。旧 server 不收到新字段，旧 runner 不提供诊断，均保留原有失败处理；runner wire 保持 2/1。Server 按原 run 的冻结 spec/workspace 校验诊断；当前合同已修订或租约对账改变了 run 投影时，仍接纳迟到的最终失败事实并结清原执行，不拒绝 journal 中的回执，也不自动进入负责人修正流程。负责人修正准入额外要求 run 仍处于 starting。失败、诊断、事件与必要唤醒在同一事务保存。历史记录不补造启动证据。

只有当前普通 Agent owner、有效责任代次与当前执行合同、活动 Issue/托管任务下的确定准备缺项，进入 `preparation_required`。任务阻塞但不排队重试、不打开面向用户的 retry 决定；持久事件仅路由到该 Issue 的现有负责人。该阻塞与主机不可用独立，健康检查不能自动解除。旧 spec/代次、取消、暂停及 unknown 保持既有控制和对账规则，不自动转交主 Agent。

`tasks_read` 的 run 投影携带诊断，owner API/CLI 共用同一事实。具名负责人的实际工具 schema 与常驻指令均说明复用和提交授权；创建受理不宣称 worker 已启动。Runner 承载的负责人可在当前准入会话和责任代次中读取本 Issue 的 run 消息，不能跨 Issue、伪造其他 run 或沿用旧 assignment；不增加发送和执行权限。前端只补充新阻塞标签及生成类型，不改变页面交互。
## D140：Issue 对话始终面向负责 Agent

在 D136 的 owner 责任之上，Web 新建及类别交接使用显式 owner 合同；历史 manager 请求不静默升级。新建 Issue 默认请求活动的 issue-manager 能力类别，不可用时退到首个活动类别，无类别时由 owner 负责；显式选择及恢复原请求不被默认值覆盖。普通 Agent 的 owner 请求与历史类别 manager 请求共用发布路径，在同一事务建立稳定负责人会话，待领取可提交消息但 admission 继续等待真实领取，不提前选择执行者或启动会话。

退回 owner 时尚未投递的 owner 消息保留原队列及撤回能力，再委派后送达；sent 与 unknown 沿用核对，不重发。owner 负责时不提供私人备注输入，提供持久请求的一键默认类别交接及完整交接入口。沙盒通过现有 ManagerExecutionResolver 承接真实 runner 领取，正式 runner 行为与 wire 不变。看板的 pending_manager_category_id 是可选只读派生字段，避免 Web 按卡片读取完整详情；不改变 runner wire、控制入口或迁移。实施见 [S10 计划](plans/web-ui-overhaul-s10.md)。

## D141：集中 TraeX loop 与 runner 原生 TUI

本地与集中 Agent 长期共存，按冻结的具体 Agent/runtime 配置显式选择。集中模式由中心运行 app-server，复用中心用户的模型认证、全局配置与 session；真实代码、Git/工具认证、exec-server 和 Orca TUI 留在 runner。原生 TUI 是同一 thread 的远程客户端，不启动第二个 loop。Cove 后续复用该会话和控制权边界。

执行连接沿现有 run 与节点鉴权由 runner 主动建立，不开放无鉴权的执行端口。冷恢复固定中心、run、工作区和 Environment，缺失绑定时明确阻断，不回退中心本机执行。执行资源仍由 runner 对租约、fencing、取消和未知结果负责。中心会话持有与 worker attempt 分离，人工 held 不销毁会话、不自动完成；TUI 断线或退出不交还控制权。首次人工写入须先完成既有 takeover，显式 resume 才恢复自动驱动。业务动态工具由 Tend 控制连接唯一回答，TUI 入口限定原 thread。参见 [实施计划](plans/central-traex.md)。

原生前端正常退出保存绑定原 capability 的本机证明，区别于仍可重连的 helper；连接中的 Orca PTY 不单独证明原生前端存活。恢复只重建同一 thread 的视图，保留 owner 已退回的 shell。确认的 provider、终端、pane 和 thread 通过既有 native identity 事件记入账本，供原生会话入口读取；导航事实不授予控制权限。默认及示例服务端配置不启用中央 worker，本地 runtime 不自动迁移。


## D142：CLI 能力全集与持续覆盖

CLI 是全部用户业务、管理与诊断能力的完整入口；Web、Telegram 根据交互场景使用其子集。能力覆盖包括完整有效输入、业务筛选、分页、输出、原文件和原请求恢复。内部执行协议保持原角色、租约与执行上下文，不转成 owner 旁路。新增或修改用户能力必须在同一 PR 同步命令、帮助、能力目录及验证。

Owner HTTP 路由统一登记，实际 owner/hosted operation 与 CLI 命令绑定在 make check 中核对。别名归并到已有能力，未覆盖的公开入口拒绝通过；入口测试执行真实 CLI 分发并核对 HTTP/socket 与 Envelope。CLI 仍只依赖标准库和 protocol，服务端业务规则、去重、CAS/assignment epoch 及 unknown 对账继续由 app 负责。请求重放保留原身份和输入，不产生第二账本。

本轮补齐会话、消息交互、Issue 状态与通知、决策、任务/运行/交付、归档、用量与目录，并增加只读 doctor 及已有维护入口。本机 Agent 管理只连接原私有 socket。Artifact 详情使用 owner 认证的只读 API 与现有视图，修复 Web 已有调用。新增 owner 能力不改变 runner wire 2/1，不新增存储迁移。实施与验收见 [计划](plans/cli-capability-parity.md)。

## D143：Agents 只读目录与版本续接

当前 Agent 平铺展示，运行位置与运行方式分列；运行方式由执行适配器与显式 CLI 声明确定，不从 runtime ID 推断。页面及详情标题 16px、名称 14px，历史定义折叠保留，手机使用独立详情；保留 runtime 绑定和 Agent 稳定身份。目录只读，定义经原本机显式 reload 接受。新分配使用当前定义，已有责任继续冻结快照；续接候选由账本引用和 runner 实际核验共同限定，不能用于新分配。能力双向协商后追加 hello/heartbeat 字段，协议仍为 2/1；未协商时原行为不变。原指令、路径和凭据不进入公共目录，历史保留不预占进程或槽位。见 [实施计划](plans/agent-directory-continuation.md)。

D143 排版与元数据修正：format 2 的 hosted runtime 可显式声明 codex/traex，旧配置省略字段仍有效；缺声明只显示已知适配器，缺运行观测显示“未上报”。既有 `tend agents hosted/show --json` 同步输出原安全投影，无新增 API 或 runner wire 变更。见[实施计划](plans/agent-directory-presentation.md)。

## D150：问题沿 Issue 责任链处理

D136 的责任主体仍为 Issue owner；`worker` 只指 Task/Run 的执行适配器，不另设管理层。另一 Agent 承担独立工作必须拥有子 Issue，包括调查与正式复核。执行中产生的问题归属于实际执行的 Issue（验收为复核子 Issue），负责 Agent 自己提出的问题则提交上级。

原 Attention/Decision 保留业务身份和效果，新增持久责任记录及不可变逐跳历史。当前接收者由来源 Issue、当前祖先关系、owner、assignment epoch 及责任版本共同约束；交接和重挂载使旧责任失效。Agent 可以处理已明确交给自己的原问题，包括有限次数调整和技术恢复，但不会取得任意子任务编辑权或新的用户授权。回答记录真实 Agent 来源；原 task spec、run、租约、独立验收、未知结果对账与请求去重不被绕开。处理不了时只能上报一个父级；根 Issue 上报唯一主协调 Agent，主协调仍不能处理才转用户。人类 owner 出现在链中时直接停在该层。

内部待处理问题通过现有持久 background source 唤醒责任 Agent，不形成用户 Inbox、徽标、渠道交互或个人待办。解决后回到来源 Issue；旧问题在升级时保留原文、ID、历史及回答通道并接续责任。用户仍可直接控制。CLI `attention list --recipient agent|main|owner|all --responsible-issue <id>` 支持责任筛选及分页；`attention resolve|escalate` 使用责任版本、epoch、原问题版本和理由，支持完整 Envelope 的 dry-run/replay。

负责人会话持续启动失败时，在同一读取快照中冻结实际 runtime selection 及其原输入或对账 attachment 身份，独立技术事件仅结算该身份的启动失败窗口，复用 Agent 恢复政策的次数及总时长预算，暂态仍按原30秒退避。尚无 native intent 的 pending 或已由 dispatcher claim 的 sending 均可计本地启动失败，不伪造 outbox 投递次数或 native turn，也不重派或释放原 sending。耗尽后建立唯一 action Attention 并交父 Issue，根交主协调；主协调自身启动失败直接持久通知 owner，不再依赖同一不可用运行时处理自己的故障。原输入及实际 dispatch 状态保留，问题结清前暂停该未准入输入的自动启动；明确结清或实际成功后以原输入身份继续。已有 native intent 或未知回执的只读 attachment 独立发现，不受较新输入的启动预算阻断，原未知事实仍走原对账。见[实施计划](plans/bounded-agent-startup-escalation.md)。

明确未发送的普通 `rejected` 输入在没有 pending native receipt 时仍受原启动预算约束；具名 sending/rejected 的冻结恢复合同及显式 pending receipt 的只读发现保持有效。原输入的 hold 同时约束 outbox 领取和首次 native starting 的事务准入，防止较新输入打开会话后绕过旧输入的技术待办；已有 native 执行事实的读取和结算继续保留。见[审查修正计划](plans/startup-hold-admission-review.md)。

原生 compact 的 starting/running/unknown 也独立恢复其原会话完成流，以原 compact outbox request 对账，不借用较新输入的身份或清除其 hold；缺原 outbox 的旧事实仅保留只读发现，不捏造请求。实际 starting 在同一事务用私有 snapshot 冻结原 runtime 与 thread（含显式 legacy）；历史无 pin 时只凭同 conversation、同原 thread 的执行事实恢复，缺证或歧义保持 unknown，不能改用当前定义。恢复直接 resume 原 thread，拒绝恢复不能创建替代。见[原身份修正计划](plans/compact-runtime-identity-review.md)。缺证 compact 仅隔离其 conversation，不中止后台 pool 的其它扫描。原 compact attachment 在 ResumeThread 暴露回调前即进入隔离，禁止新 native 工作与工具回调；idle summary 同时受该隔离与未确认 compact 的账本门约束；完成事实持久化并 ack 后退出恢复 session，重新执行正常 binding/toolset 核验及必要 handoff。见[pool 与工具集修正计划](plans/compact-recovery-pool-and-toolset-review.md)。queued 或已结束的 compact 不授予未知原生操作豁免，完成事件只结算原 operation/thread，不重发 compact。见[补充审查计划](plans/startup-native-compaction-review.md)。

新 runner 以可选能力 `supervisor-questions-v1` 协商，并在创建 run 时冻结答复合同；新运行的 `ask_owner` 表示提交给 Issue owner，其答复不能扩大原授权。旧运行仍保留真实用户答复语义，但问题先按责任链上报。字段可选，默认关闭；runner wire 继续为 2/1，不改变旧请求的有效性。实施计划见 docs/plans/issue-responsibility-routing.md。


交接与容量合同：新执行按 `issue-question-handoffs-v1` 冻结分轮交接能力。提问持久保存原问题并由 runner 确认真正停止后结束本轮、释放 slot；代表 Issue owner 的执行自动提交父 Issue owner，根 Issue 提交唯一主协调 Agent。答复与退出确认均持久后才创建接续 Action/Run，沿用原 spec、artifact 和 generation，不消耗实现尝试额度，不复活旧 Run。固定原主机、执行配置和 workspace 互斥；可恢复的实现会话保留原生 thread，验收与不支持原生接续的适配器通过原问题及完整合同重建上下文。协调与执行均严格使用配置容量，不提供额外名额。新合同仅向明确协商的执行启用，历史 Run 保留原等待合同并受控排空；wire 仍为 2/1。详见 docs/plans/issue-run-handoffs.md。Agent 决策和行动完成不能用作真实人工授权证据。

manager（含 receipt-only 对账运行）真实失租转为 unknown 时，同事务按原 run/lease 建立唯一技术 action Attention，绑定原执行事实并交实际父 Issue 负责人，根到 main，复用上述责任通知与逐级升级；责任已交接时先由当前 owner 承接。发现者记录为 system，不冒充 manager 业务请求。原 unknown、lease、冻结 pin、native pending 与输入保持不变，不自动重派或认定业务失败；没有真实失租的 pause 或条件等待不产生故障。此触发不回填历史已失租运行；历史技术责任须按原身份单独补齐，不以部署替代恢复。无需迁移、API/CLI 或 runner wire 变更。见 [实施计划](plans/manager-lease-reconciliation-attention.md)。

## D152：验收后交付责任与 owner 重开恢复

Task 的 completed 仅表示冻结产物通过独立验收。canonical Issue 在同一完成事务中按原完成事件建立独立交付核对义务，保存 task、acceptance、候选版本、问题、后续执行和结清证据；计划阶段为空或完成通知被确认均不能消除该责任。关闭与取消终点同时校验这条账本。历史已完成任务仅补待核对事实，不补 owner 批准、不批量提问、不重开历史事项或重放执行。

负责人先准备实际 PR、head/base SHA、验证引用、已知 provider 状态及明确部署范围，再通过 `issues.update_delivery` 创建 taskless versioned Attention。在当前 IM 提供其原问题，复用已有答复入口；旧问题、条件性文本、变更/撤回的 owner 来源不能授权新候选。仅原问题精确批准后，`issues.create_task.delivery_authorization` 才能创建冻结范围的后续交付执行；提交前和领取前均核对候选。原实现任务保持 completed。真正合入与已授权受控自更新由 Tend 的后续执行任务负责，不交给外部操作者代做；使用已有本机 upgrade CLI、固定请求及批次身份，unknown 先对账。部署候选同时冻结 server 实例与完整 host 集合，合入前 rebase 改变 head 须重新确认。结清要求匹配 head 的 provider merged 事实及后续执行的 passed acceptance；deploy=true 还须提供本次执行开始后创建的 completed upgrade_id，校验其 server 实例/健康、完整 host 集合及已核验安装回执，冻结升级证据。执行报告的 upgrade:<id> 证据引用在 artifact 同事务绑定到该执行，running/attention 批次不领取其验收，避免验收抢占待更新主机；原 context admission 检查不变，终态失败仍可独立审查。已合入后已知终态失败的部署恢复须重新确认 provider_state=merged/实际merge_sha/部署范围，创建新交付执行，不再次merge；原批次running/attention或原执行未结清时不允许另起批次。独立验收核对 merged-source 发布来源、实际 server 版本/hash、主机安装及原 Issue 行为回读。批准或 task completed 不自动声明部署成功。延期答复保留责任并阻止关闭，不反复询问同一问题；后续真实 owner 继续请求通过 action=resume 新建候选确认，保留原延期答复，不将其解释为批准；只有明确结束剩余交付的 owner 决定才可结清未交付代码；负责人可引用当前已准入、未撤回且内容版本一致的 owner 结束来源，不要求先做已被取消的 PR 准备，也不能把原 defer/approve 答复改写成结束。报告/调查以明确的 accepted report 终点结清；结束尚未交付的代码责任需要实际 owner disposition。交付事实不从可编辑 stage 名称或自然语言任务名推断。

部署目标按本服务的固定 updater 串行。已开始的 deployment 执行保留持久逻辑所有权；另一个 Issue 只有 pending action，没有 run/slot/执行环境，因而不会持有排空资源等待第一批。取消不能猜测 unknown run 已退出；确认终态及真实退出后才释放，原任务/批次/问题不替换。确认问题冻结最近已结清升级的 ID/revision，provider 观测记录查询发起时的 baseline；升级完成或取消使旧查询失效并被动刷新。领取和本机 start 再核验 owner epoch、答复、当前 baseline 和活动执行租约。`tend upgrade start --delivery-task` 从已校验的实际二进制读取 Go VCS 身份，拒绝 modified、未知或不符批准目标的产物；已 merged 的候选冻结当前 target_sha、核验原 merge 包含于该目标，不能安装旧 main 覆盖前一项交付。Agent 的技术 resolve 不能代答交付批准；已有真实 standing owner 授权仍可经原版本化来源通道准确记录，不能伪称用户逐项审阅。

首发兼容仅核验既有事实：候选的 reconciliation 绑定同一 Issue 下原已独立验收的报告任务、原 completed upgrade_id、实际 merge 及已观测的 installed source。新任务只核验、不合入/安装；保留旧任务和批次身份，不回填 typed 执行。新独立验收及完整安装证据通过后结清。首发等待复用 D150 已部署且实际协商的 question handoff：原交付任务启动唯一批次后以原升级 ID 提问并停止本轮，协调者待批次实际完成后答复原问题，原任务接续只读核验再提交，避免在旧版本上提前生成验收占住排空资源。旧运行无 handoff 能力或上一批次未结清时明确等待，不强制改旧合同。该兼容入口不把外部 bootstrap 包装成 Tend 自行交付。

新增 owner 输入、只读字段和服务端迁移不收紧原 runner 请求。runner 先声明 `delivery-candidate-identity-v1`，server 才在交付查询中请求可选 `candidate_identity`；只有收到请求的新 runner 才发送可选 base SHA；另以 `delivery-target-lineage-v1` / `target_lineage` 握手发送目标分支 tip `target_sha` 和 `target_contains_merge`；旧 server 不接收新字段、旧 runner 仍可报告原事实。缺 base/target/lineage 的历史观测保留为未知，不能给新候选合入授权。通信仍支持 runner 2/1。

owner API/CLI/Web 的 reopen 与 named main 的合法 owner 来源共用事务：检查原修订与责任代次、来源版本及祖先，先核对旧协调运行和回执，再建立新协调任务/代次，沿用稳定会话和消息队列。旧协调未结清或 unknown 返回明确对账阻塞，不启动第二个协调者，不修改 finished/failed/unknown implementation。已关闭修订和 closure 历史保留。原请求重放返回原结果，旧 revision 的并发请求不能再次提升代次。重开仅恢复责任，不授予 merge/deploy 权限。详见[实施计划](plans/delivery-responsibility.md)。

D152 验收修正：GitHub 的 `baseRefOid` 仅为 PR base，不能当作当前目标 tip。观测独立读取 `baseRef.target.oid`；merged PR 按 merge 与 target 的不可变 SHA 查询 compare，只有 identical/ahead 且 merge-base 等于该 merge 才记录包含关系。确认候选和问题同时冻结 base 与 target；open 候选合入后仅允许当前目标恰为其 merge 的部署，merged 候选可确认包含该 merge 的当前目标。所有候选/领取/start 校验使用该独立身份，旧事实不得猜测。owner defer 始终保留 deferred 状态，prepare、validation 更新及 provider/baseline 失效不能清除；只有 owner 显式 resume 或当前合法 owner 来源可重新创建确认，原 defer 及更早的 owner 来源不得作为 resume。

D152 执行合同修正：实际 `issues.create_task` 冻结的交付约束与 source guard 使用相同身份。provider_state=merged 时核验 merge_sha 是冻结 target_sha 的祖先并构建 target_sha；base_sha 保留历史 PR base 身份，不能构建为部署来源。open 候选实际合入后核对历史 base 不变、当前目标恰为本次 merge，构建该 merge_sha。最终 spec 由真实版本化答复与创建任务回归读取验证。owner 选择 defer 时可附说明文字，文字不取消延期语义；带说明的答复同样持久阻止 prepare 或观测刷新重新提问，只有合法当前 resume 来源可继续。

D152 结清修正：merge-only 与部署结清共用当前 provider 的 base/target/merge 整合校验。结清还必须保有 executing 状态、原执行绑定的 duty 修订及仍有效的原版本化批准；独立 passed 仅证明冻结产物，不能覆盖后续 provider 回退、未整合、base 漂移或持久候选失效。失效后即使相同快照恢复也不复活旧批准，原 duty、执行与验收历史及 close blocker 继续保留，通过当前责任协调或合法 owner disposition 处理。


## D153：自适应并发分组

并发预算与 host slot 独立，由服务端账本统一准入。分组及 runtime 绑定通过完整
CLI 管理，不增加配置文件；已有执行在绑定变更时转移占用。预留与确认运行分别
计数：前者防止超额，后者累计满载证据。空闲会话不占额度，额外摘要单独申请，
手工 compact 同样申请并按其原生完成证据释放；解除订阅不代表执行已停止。
负责人代理不重复计数；未知执行沿原 fencing 和对账，不按时间假设释放。

每组 min/initial/max，拥塞每次减一，三十秒内合并，最后拥塞后冷却三十秒。
升档按同组全部受管 session 的已知反馈判断：完整三十分钟无临时 429/overload、
累计十五分钟达到当前上限、有成功 turn 且仍有容量等待才加一。升降档、配置或
成员变化、重启、反馈失联后重建观察，不把停机或无反馈当成成功。原生重试不
重复触发，同 turn 的故障与最终回执合并；不接管原 session 重试。

最近七天记录保留不可变决策快照，拥塞关联确切 Issue、Tend/native session、turn、
运行位置和结构化错误；无 Issue、未提供 native ID 或发生时间时如实显示。稳定
升档保存窗口、满载毫秒、成功 turn、零拥塞与等待需求。历史清理不影响去重与
当前预算。Agents 第三分类只读展示这些事实，CLI 同步全输入输出、筛选分页、
原请求恢复与可执行能力覆盖。增量能力协商启用反馈，runner wire 保持 2/1。

D153 串行准备补充：上限为一时，runner 负责人尚无活动 turn 的准备摘要可以借用
自身已预留的同组额度。借用期间原负责人不能启动新 turn，摘要结束才归还；若
原负责人正在执行或有未知操作则不能借用。正常可并行的摘要仍独立申请。关闭
原生 session 只核销该 session 实际创建的摘要额度，不能核销其他进程的未知执行。

## D154：固定 updater 的原窗口前进恢复

针对 terminal 升级账本与本机 irreversible checking 不一致、旧健康条件不可由 updater 改变的恢复，新增独立 forward recovery。旧 batch/start、operation 全快照、旧 proof、失败与 unconfirmed 历史保留；新 binding 的健康和 receipt 不解释为旧 batch committed。恢复不调用旧 terminal retry、不新建升级 batch、不回滚已接单数据库。

第一跳是新产物的本机 installer，不依赖旧 updater 新 RPC。仅支持经逐项核验的 systemd 用户 simple service：固定 regular ExecStart、原参数和配置、KillMode=process、SIGTERM、Restart=on-failure。installer 在 unit 外运行，独立排他锁；停止 updater 后证明旧 MainPID 退出、child 完整身份保持、同 state/同 flock inode，原子替换固定 updater 产物。新进程从持久 journal 进入 adopt，普通 Step 在恢复期间被阻挡。未知策略/身份/持久化结果拒绝；不提供 shell kill、unit 修改或旧 binary 回装。

恢复启用时在原 flock 下迁移至 format 2，独立 generation/CAS 和 updater epoch；format 1 原语义保留，旧 updater 不能读取 format 2。原窗口、operator UID、私有过期资源交接证据和物理 writer 退出证明为新授权条件；历史 writer 用 PID/start、launch 来源与实际退出事实绑定，无法取得的 executable hash 留空表示 unknown，不能用它替代退出证明。live server/updater/candidate 仍要求完整固定 hash 身份；旧 grant 不续期。停止和启动先持久意图，恢复 stopping 只观察退出；启动不确定只收养唯一精确 launch，零或多个均 unknown，不跨超时重复启动。

successor 在 App 构造时 fence 新 owner/main 业务写入与新 runner claim，已提交请求先去重并重新授权，可信原生历史适配器仅可为既有 thread/turn/item 补记终态事实；在公开服务前向固定 updater 核对 durable 授权和自身内核身份，并通过 app 事务登记独立行。recovery health 验实际进程、binding、协议/schema、DB 读取、登记/fence 与已配置组件 readiness，区别于首次安装的 MainDrained；同 boot 至少两个递增样本，错误清计数。Current、process、独立 committed proof 和 recovery receipt 在同一次 fsync/rename/dir-fsync 中持久；旧 proof 留历史，旧 operation 不伪造 terminal。

receipt 文件先持久，再由 app 单事务追加 ledger link、事件、请求和本机 audit outbox，并释放 admission fence。DB 失败仅按稳定 link request 重放；updater 只在精确 receipt 和已完成 app 关联后归档原 operation、解除 unfinished 占位。status 分列原 unconfirmed 与 successor committed/link 状态。执行器回调保持私有 socket 权限，CLI 提供完整 plan/apply/status、筛选/分页及固定请求恢复。新增本机协议不改变 runner wire 2/1；追加存储迁移，不修改历史迁移。Refs: D84、D142；见[计划](plans/forward-recovery.md)与[启用说明](forward-recovery.md)。

D154 补充：恢复资源交接区分本机 kernel writer 证明与固定远端证据文件的操作者见证，远端 PID 不进入本机 `/proc` 校验。owner operation 先去重和重新授权，恢复 fence 仅拒绝新动作，原归档 body admission 不变；CLI 校验并执行同一私有安装器副本。

## D155：已提交 server 的独立本机维护前滚

正常协调升级仍要求原 run 可对账；D154 只恢复原不可逆 checking 窗口，不扩展为任意部署授权。
已提交服务需要新版本时，操作者可发起独立、本机、限时且绑定当前 committed proof 的维护。
新窗口保留全部业务 run、pin、thread、决策及旧 upgrade/recovery 事实，追加独立收据与账本行。

第一跳由新版固定字节 installer 执行，绑定当前 state/generation、旧 updater/child 内核身份、
配置、unit 策略、原 flock inode、前次 journal hash 与候选发布兼容范围；它先保留原 child，
只替换 updater。新的 format 3 状态及独立 journal/head 链不修改旧 D154 journal；已 linked
窗口可成为下一独立窗口的来源，旧窗口和 proof 进入历史。同 ID 重试只对账；中断续进另带
固定 continuation request 和 journal generation，不通过重复 start 推测未知结果。

换 server 前使用旧服务已有 archive API，以固定 request/CAS 建立准备及显式主机维护。
只有准备修订匹配、owner 在途为零、HostedDrained 为真、全部主机新领取已停止并确认对应
维护控制时才能停旧 server；OnlineReady 的 run-zero 条件不用于此路径。持续未排空时可在
旧 server 停止之前取消，保留 Current/child，仅恢复自身窗口实际创建的准备和 host CAS。
第一次替换 updater 不要求 HostedDrained 为真，因此旧服务不认识新 API 也能完成引导。

旧进程退出后先备份，再持久不可逆的新进程启动意图。候选在公开业务入口前以真实本机
supervisor 状态和内核身份注册独立维护账本并 fence 新业务，使用两次同 boot 递增健康样本
提交 Current/proof/receipt。app 以稳定 link request 单事务链接独立收据、事件及审计 outbox，
updater 再确认同一 receipt；显式 finish 按原窗口 CAS 释放归档及主机维护。失败、过期或未知
不授权回退已接单数据、伪造 run active、延长租约或改写旧失败。runner wire 保持 2/1。

Refs: D84、D142、D154。见[实施计划](plans/committed-server-maintenance.md)与[操作说明](committed-maintenance.md)。

D155 审查补充：确定 `ErrNotStarted` 不等同于含糊启动，保存未启动阶段；在旧进程已退出
且未有候选运行/迁移的阶段，允许新的显式请求、generation/CAS 与新鲜限时资源证据
继续同一候选，不调用不存在的旧服务 API。真正 unknown 启动只核原 launch。原始 state
单次保于不可变 journal，state/history 用 hash 引用，避免递归增长。父 DB 已 link 而本机
确认丢失时以精确父 receipt 核实成功事实，新后继不将父 Linked 置回 false。

D155 锁观测补充：维护计划从精确 updater 的 fd/fdinfo 验证原 flock，文件描述符的
完整 stat device/inode 必须匹配计划。Btrfs 子卷 stat device 与 flock superblock device
不同的情况，通过同 fd 的 mount ID 和 Btrfs mount device 精确核对；进程及 fd 前后
身份不变且确有该 PID 的独占 flock 才通过，不以相同 inode 省略设备或所有者校验。

D155 重启来源补充：同版本受监督重启保留历史 committed Binding，当前 Process
随核实后的新 kernel 身份更新。普通维护分别匹配原 Binding 与当前持久 Process，
不把历史提交 launch 当成当前 launch；实际 Inspect、唯一 Find 和 artifact 校验保留。
failed-parent 前滚仍绑定其原 successor launch；新 successor 不得复用历史 Binding
或当前 Process 的 launch。原 ledger、receipt 与请求不改写。见[实施计划](plans/restarted-maintenance-source.md)。


## D156：原收据未知时的显式崩溃恢复维护

旧版本可能在 archive prepare 下持续恢复只读收据，无法报告 HostedDrained。普通 D155
仍要求真实排空；新的 `receipt_recovery_stop` 计划与独立 `recover-stop` 请求明确承担
未知托管生命周期，不把 false 观测改成 true。只接受准备修订匹配、owner 在途为零、全部
host 新鲜确认维护、领取为零，且旧 API 已知 hosted-effect blocker 仅有 native receipt
reads 与托管生命周期未知。未知 blocker 拒绝。准备下两次完整 task/run 库存须以 TaskID/RunID/role/generation/spec/host 稳定身份全集
匹配固定计划和逐 host open count；Observed/Preparation 是动态观测，允许变化但原快照
与各自 SHA 分别保留，同请求仍严格匹配完整 payload。task 已终态也不能漏掉其尚未终态
Run。HostedDrained 真实变为 true 时仍可选择显式恢复，只要 native receipt 未知仍为正、
其他 effect 与原安全门均满足；这扩大原恢复谓词的接受集合，不转为普通停止。
真实 HostedDrained=true 也独立证明前台摘要调用和后台 manager 已退出；此时允许保留
合法正数的 summary operations 作为未结账历史，原 started 记录不改、不伪成功或失败。
HostedDrained=false 时仍拒绝摘要正数，其他 effect 不放宽。prepared/prelink 本次维护
不重试摘要；finish 后的普通摘要 backoff 与正常调度仍按原规则。
见[摘要历史计划](plans/drained-summary-attempt-history.md)。

固定 updater 以精确 pidfd 仅 crash-stop 原 server 父进程，持久意图并确认实际退出，不
发送 graceful native close、不信号 runner 或共享执行组。外部结果不因连接断开变成成功
或失败；未知执行保持原 pin，不重发旧业务动作。SQLite WAL 与后续备份保留提交事实。
候选在任何业务监听、runner RPC、dispatcher 或 hosted 启动前，以同事务核对完整原 Run
集合、原 lease 和原 hosted request 身份；遗漏或换绑拒绝，已终态不复活。

原 manager 的 unknown 收据资格在注册时冻结。同 lease 的物理 close/Stopped 保存原请求、
sequence 和停止事实，不自动终结原业务 Run；晚到收据、重启或 link 都不清除此保护。
旧协议不能区分维护断连与普通物理 give-up；captured 普通 Run 的同 lease release 因而只
保留未分类原事实，正式业务结果仍走原规则。明确用户 stop/cancel/pause、角色或 lease
变化优先正常控制，不把新独立业务完成伪装成旧 physical close。原请求永久幂等。

prelink 真实延后 front/background/native 收据与模型启动，并禁止新的 native business
claim；健康仍核验实际组件静止、DB 可读和已登记 scope。两次递增真实 health 与独立
DBlink 后恢复原身份的收据对账，archive/host CAS 仍由显式 finish 释放。保护不因 link
消失；新业务动作须独立接管，不沿物理已停止的原 runner journal 自动重启。追加迁移
0082，不改历史迁移或 runner wire 2/1。受保护原 lease 的旧 Hello 不返回 Continue，明确让整台 host 的对账保持 pending，
不自造旧 runner 不支持的 hold 或 retain。独立 owner stop/cancel 可用已持久物理事实结算
该原 Run，原物理回执不改；普通 release 的待对账状态通过既有 UnknownOutcome 让旧客户端
保留原请求。新控制实际释放原 lease 后，原客户端按认证拒绝收尾。
本范围不宣称历史 unknown 或 runner drain 清零。

维护候选 Start 成功但 Inspect 前退出时，仅本次 owned Wait 返回的真实 Exited/Signaled
状态形成独立 launch_exited 事实，不伪造 ProcessIdentity。该事实与固定打开执行的 ELF
SHA/launch/PID 绑定；没有 Wait 证据仍 unknown。新独立窗口在精确父计划和原准备授权、
资源/CAS、fresh 原 launch 空扫描下可以前滚新 artifact，覆盖父已登记或尚未登记的情况。
已登记父的真实 Process 与请求原文保留，不能把空扫描解释为退出或补造 start ticks。
仅该已证退出且单元 inactive/dead、MainPID=0 的安装阶段，允许 ControlGroup 自然为空；
保留真实快照，以原钉定 cgroup 对比原 policy，其余属性和旧 Format2 规则不改变。

Refs: D142、D154、D155。见[实施计划](plans/receipt-recovery-stop.md)。


## D157：原责任与用量重叠查询的有界关联

Manager handoff 的 delivery blocker 只要求存在同 issue/旧 assignment 的原 logical turn。
先对这些 logical identity 去重，再查其 sending/unknown outbox，避免同 turn 多次续接
先形成执行数乘积再 DISTINCT；conversation 分支、epoch 边界、操作白名单与排序保持原义。
Token overlap 保持原 SQL 和原始计量事实，只追加 native candidate 与 current fact 覆盖
索引，防止相关子查询逐行重扫历史。未知计数、冲突后 legacy 回退和匹配去重不变。
Manager control blocker 的原 request_records EXISTS 按 request_id/status 查询，原主键的
domain 前缀不能覆盖此访问。追加0084的反向覆盖索引，保持跨 domain 的原 SQL 与完整
状态事实，不改变请求去重。精确产物反汇编的 CALL 返回点与 SQL 常量绑定定位这一查询，
不能仅从邻接行号把热点归到 delivery。
本地规模/EXPLAIN 与完整结果等价只认证查询实现；生产页面延迟仍需独立有界验证。
见[Manager 计划](plans/manager-delivery-query-latency.md)和[Token 计划](plans/usage-overlap-query-indexes.md)。
见[控制回执查询计划](plans/manager-control-query-latency.md)。

## D158：Token 归属的请求内批量解析

同一用量视图的独立 turn 曾逐项扫描 run_logs 与本地来源。resolver 在当前只读事务内
批量读取 SQLite 原投影，以当前 Token thread/turn 限定日志索引内存；重复同 Run 不形成
歧义，不同 Run 保持未归属。来源解析保留 direct thread 优先，以及原 ORDER BY 的
false 高于 NULL；最高 rank 的 actor 投影冲突时按完整参数执行原 SQL，保留其原有 tie
选择，不新增排序规则。actor categories 按每条记录独立解码，未知计数仍为未知。

请求内缓存不跨视图或事务，下一请求可以看到新事实。批量读取失败丢弃部分结果并回退
原逐项 SQL；失败不缓存为确定归属。无需协议、存储 schema 或归属事实变更。完整输出
等价与同 thread/独立 thread 两种布局的规模收益分别验证，离线收益不替代生产读回。
见[实施计划](plans/token-attribution-resolver.md)。

## D159：保留未知运行而不阻塞整个主机对账

旧 runner 只理解 Continue/Stop，不能把未知运行冒充已停止或授权继续。因此保留原错误
路径，同时增加显式 opt-in `runner-retained-reconciliation-v1`：支持 N/N−1 且声明能力的
runner 才接收逐运行 retain。无该能力或不受支持的代际不得收到新 verdict。这是可选
协商扩展，旧请求/响应默认语义与协议代际不变；不是放宽未知身份检查。

新 Hello 可登记实际 incarnation 和控制，并按原 host/run/lease/业务控制核每个运行。
维护保护中的原运行不续租、不把 unknown 改 running、不重新分配；它仍计入原容量，
原 pin/request 事实不变。无关运行独立对账。原 journal 缺失不能创建新 worker；旧 lease、
跨 host 和明确 Stop 不被 retain 吞掉。runner 把 retain 保存为本地 fence，不释放 journal
或重放 worker。启动与 fence 序列化，多个 Hello 完整处理也串行，防止旧 Continue 迟到
后覆盖 retain。合法的新 Continue 同时解除 journal 与 active 的保留标记；已经取消的
执行器退出后再请求对账，报告沿原请求结算。明确 Stop 经相同结算门授权真实收尾。
快照后已完成的 journal 仅凭原身份和 Done 事实局部忽略迟到 retain，不能中断其它
运行的 verdict；迟到的本地 fenced 写入不能覆盖较新的权威控制。

这一批只扩展执行器内部协议，不新增用户控制能力，CLI 已有 runtime 读取可见该能力。
原运行的独立业务对账/退役入口另批实现，并须同步 CLI/主工具面；主机成功登记不证明
原运行恢复，也不证明升级排空或未知外部结果已确定。见[实施计划](plans/retained-run-reconciliation.md)。

D159 普通 Run 继续补充：独立 typed `runs.continue_retained` 只授权命名普通 Orca
执行的原 journal 严格附着，不删除原维护保护、物理停止或未知业务请求。动作核验
Task CAS、Run/task/action/generation/spec/host/同一未 released lease、完整执行 pin、
全部保护身份与原文摘要及真实已结算业务；新 capture、控制变化或新 pending 使原决议失效。
owner、准入主会话来源和当前负责该 Task 的 Issue owner 共享同一 app 校验；CLI 提供
只读对账、完整冻结输入、原请求查询与精确 replay。决议本身不续租，只有携带原 journal
的合法 Hello 可恢复原 lapsed lease；缺 journal 不分配新运行，heartbeat 不能先行消费授权。

专用 continue_retained verdict 仅返回给显式协商 strict-retained-resume 的 runner；
N/N−1 旧 peer 仍 retain，旧请求默认语义不变。新 runner 将严格模式独立写入 journal，
保存原 Assignment/Thread，重启和后续普通 Continue 不丢失约束。Orca 要求原
worktree/terminal/pane/session/runtime/launch/turn 完整一致，且当前原 turn 可观察；
坏 Thread、失踪 terminal、unknown/pending 或身份变化均保留待对账，禁止新 launch、
隐式 continuation、原 prompt 重放和 shutdown 注入。首批 app-server 不支持。
附着不能恢复已取消的 turn，也不能将原生 unknown 判为成功或失败；同 session 的新
业务继续需要独立持久化发送合同，不由此授权推断。实际恢复必须另核原业务推进，不能
只以 lease active 或健康主机验收。首次附着只接回 started journal；最终阶段仅恢复同一
严格许可下已持久记录的真实 worker 结果，或原问答 handoff 已确认物理停止后的
`runs.yield_question` 回执。后者须为已协商问答交接的普通 Run、releasing 阶段，保留原
request ID、Run/target、decision 和原 thread payload；仍由服务端核验原 handoff，
不产生 WorkerDone、新问题或新 native 执行，不能结算旧 give-up/release。见
[回执恢复计划](plans/strict-question-yield-reconciliation.md)。严格附着的 setup、
观察和本地事件保存失败保持 pending；最终请求只有业务成功回执才清理，accepted 或未知
结果保留原请求身份。strict 最终请求和前置事件仅作一次现有请求超时内的上传尝试；
未知结果保留原请求/事件，显式唤醒 Hello 对账以观察后续 Stop；strict pending 报告在
退出前沿既有 RetryDelay 等待，Stop/runner 取消可提前打断，避免成功 Hello 绕过退避。
不依赖凭据撤销、进程重启或其他库存变化。普通 legacy 的 transport retry 语义保持不变。
另一个 authenticated Hello 的独立 Stop 可结束同一原 journal
责任；它以原 Assignment/lease 摘要持久化，不清 strict 约束，不将 Forbidden 当业务成功。
物理停止后，在 Done 同一事务保存原请求/事件历史再退出 pending；原 server 业务回执
不改写，后续 Retain/Continue 或新附着使旧 Stop 许可失效。Stop 复核、清理和新许可撤销
按同一 Run settlement gate 排序，最终 journal 事务再次核对 Stop 并归档；Done 后不接受
新的附着覆盖历史。单个 strict journal 不准入只保留该 Run 的 fence 与诊断，不阻止同 host
应用其他 Run verdict 或完成启动。见[计划](plans/retained-run-continuation.md)与
[操作合同](retained-run-continuation.md)。

严格附着的显式 Pause 仍中断已经验证的原 terminal，区别于 fence/shutdown 的零自动输入；
未知或未附着不获得输入权限。原 pin 可由已协商能力/原 backend 下的 AgentContinuations
证明继续可用，不要求它仍是当前新任务库存，也不替换其任何冻结字段。

D159 验收回执恢复补充：Orca bridge 将原 verdict 原子保存到 Run 私有状态，保存成功后才确认。回执绑定原 Run、worktree、native launch marker 和已持久的逻辑 turn（新发送另存唯一 turn ID，避免同一毫秒混用）；worker attempt 与本次附着授权 ID 不改变原执行身份。新 launch 或发送新 turn 时切换绑定并清除内存 verdict，旧回执不能成为新 turn 的结果。重建先恢复同一绑定的 verdict，再由严格附着核验保存的 terminal/pane/session/runtime/native launch/root turn。绑定未匹配或没有原回执时保持 pending，不发送提醒；破损或保存失败同样不生成业务完成。既有无绑定的其它 harness 回执格式保留，未修改 runner wire N/N−1。见[实施计划](plans/retained-acceptance-verdict.md)。

## D160：Agent 运行关联与观测来源

Agent 详情的 Issue 关联只读冻结的 run_agent_executions 身份与历史 pin，关联依次采用
原 Run responsibility、manager task 合同或任务创建时的主 Issue；可变 task links 与
显示名称不构成历史归属证据。同 Issue 多 Run 去重但保留各自角色、状态、定义版本、
runtime、快照与来源。当前表示仍有 starting/running/waiting/interrupted/unknown Run
或原 Run/action/spec 仍有效的结构化执行问题，不声明正在调用模型；Issue 生命周期独立显示。
普通任务级 open 问题缺少原 Run 归属时，只作为任务未结记录数输出，适用性未确认，不驱动
当前执行分类。控制 revision 的变化本身不证明问题失效，暂停仍保留原有效执行问题。

独立 owner API、命名 CLI 与 Web 同步支持范围、Issue 状态、标题/ID、原版本和稳定
Issue 游标分页。没有冻结 Run 关联时返回明确覆盖范围，主会话或咨询缺少记录不推断
从未工作。服务端 Agent 没有 runner executor 观测入口时显示未接入运行观测，区别于
runner 的未上报与离线；用途与类别分列说明，不把类别当作执行权限或健康证明。

无迁移或 runner wire 变更，不修改配置 revision、原 pin 或未结束执行。见
[实施计划](plans/agent-issue-history.md)。

## D162：飞书文本 channel 的来源身份与不确定结果

飞书作为显式配置、默认关闭的通信 adapter，复用通信消息、ReplyTo/Issue 路由、outbox 与 owner 请求去重。默认出站 WebSocket 接收复用固定 SDK 的连接鉴权与协议，基于官方 v3.12.1 的最小 fork 提供 terminal Dispose，显式等待连接与 cache cleaner 退出，原 Start/CloseAndWait 语义不变。HTTP 回调为可选模式，普通事件仍按原始 body 校验签名与时间；URL challenge 单独解密并校验 token，允许其没有事件签名而不创建业务消息。两模式均核 app/tenant；只有与 owner 管理绑定完全一致的 sender/private-chat 可以形成 owner 输入。连接配置不等已连接或消息已交付，传输错误不停止主服务。绑定以 request_id/CAS 修改，运行配置撤销 owner 权限后不再接收或发送。

provider event_id/message_id 冻结同一请求身份，持久收件恢复只处理尚无 request record 的情况；业务消息/事件/outbox/request record 仍同一事务。发送冻结目的地、revision 和稳定 UUID；确定限流可沿原 UUID 重试，未知结果不重送。状态、输入分页与绑定的完整 CLI 同步实现。附件/群聊/交互卡片和真实应用激活不在本次实现证明范围，详见 `feishu.md` 与 `plans/feishu-channel.md`。

## D163：终态任务的过期待决收敛

普通任务规则产生的问题冻结了 task control revision，但原过期问题清理仅覆盖 worker
运行问题及 Issue 所有权变更。任务完成或取消后，同事务撤销尚未回答的规则问题，并由
原 decision.cancelled 事件撤下对应 Attention。迟到答复在写消息和应用 effect 前拒绝；
原已接纳答复和原请求重放仍保留成功事实。没有冻结任务版本的范围/产品问题保持原义。
单纯暂停或 revision 变化不足以证明责任已被替代；非终态复查的 action/generation
来源规则另行处理，不按同一父 Issue 批量清理新预算问题。
见[实施计划](plans/obsolete-task-decisions.md)。
## D165：HTTPS 事件流复用

Tailnet HTTPS 使用原本机证书选择器，并保留 MagicDNS 和 HTTPS 证书开启检查，
在原 tailnet TCP listener 上显式广告 h2/http/1.1。服务端仍由标准库
http.Server.Serve处理，HTTP/1客户端继续可用，认证、WhoIs及明文监听合同不变。
准备监听的等待使用服务端传入context，不另引入provider或module。

真实本地TLS与显式受限HTTP连接池证明持久事件流可占用HTTP/1槽，使普通读取排队，
同条件HTTP/2可在同一TCP连接复用。这仅认证该传输机制与修复，不证明某次生产
页面持续加载的现场因果；fresh部署后的真实浏览器恢复仍需单独验证。见
[实施计划](plans/https-stream-multiplexing.md)。

## D166：升级窗口的 manager park

可选 park_idle_managers 绑定同一升级 request/CAS。升级记录 state_json 保存 exact
Task/Run/lease/close 请求；默认省略字段，旧请求规范 payload_hash 与回放不变。
仅空闲且无未知原生业务回执时，队列既有 close；最后事件序号与真实关闭回执通过
原 FinishManagerTurn 结束旧 Run，Task waiting 保留 Issue/conversation 责任。窗口期间
refresh、manager claim 与新 native business 被 admission fence 阻止，不能抢回资源。

普通 server/runner 安装的 open-run、journal、进程证明门槛不变。未知 close 沿原 ID
对账；cancel/fail/完成依原 CAS 与窗口状态释放 fence 后，只有新 Run/lease 可以续责。
发起工具异步返回，部署执行 worker 应提交现有 started-installation artifact 退出，
不能持有自身 Run 等待安装；原 delivery duty 留给后续独立验收和正式回读。CLI 与
admitted main 工具都提供显式选项，仍通过固定本机 controller 和精确 owner-approved
交付范围校验，不接受远端任意产物绕过授权。交付批准同样冻结 park 选项，旧批准不能隐式扩权。
仅 opt-in 在切换前探测固定候选的 `--capabilities` 并校验实际 executable SHA 与 feature；
入口在配置、数据库和 listener 之前退出，旧程序不支持时拒绝，不回退普通启动。
既有 manifest 不增字段，默认 false 不探测；CLI 提供对应 typed 能力诊断。
见[实施计划](plans/manager-upgrade-park.md)。

## D170：摘要与交接的有界事实审计

原 attempt 行按 snapshot 复用，不作为完整历史计数。只读审计按稳定事件水位分页，未来事件在原事务冻结 instance/source/target 的当时已知身份，旧空 payload 保留不可观察；不借当前 binding 回填、不输出摘要正文或本地路径。CLI 与主 Agent 只读工具同批提供，无执行策略或 runner wire 改动。见[计划](plans/handoff-attempt-audit.md)。

D170 的恢复路径 RefreshHandoff（包括 injected→created）和已确定未创建的 RetargetPreparedHandoff 也在实际变更同事务记录新审计身份；相同参数重试不重复，旧无来源历史不回填。
## D168：普通任务规则问题的冻结来源

控制 revision 同时承载暂停等独立控制，不能用其差值判断问题属于旧代次。新的规则问题
在 decision.opened 原事件冻结版本化 Task/spec 及可证原 action；按明确替代事实收敛
对应问题与 attention。旧来源缺失不回填，规划者自由问题保持原语义，已经接纳的回答
与原请求重试不改写。详见[计划](plans/task-question-provenance.md)。

D168 的 Attention 当前性投影同样读取冻结来源的适用性，不能因单次 pause 推进
control revision 就把仍有效的问题标为过期。明确退休的旧问题退出 open 工作列表，
但按原 ID 与 includeArchived 历史仍能读取原问题、withdrawn Attention 和不可变来源。
### D172 显式复用原 Issue 责任

主协调器不能按标题猜测 Issue 相同。新的 reuse 动作识别 issues.create 或 consultation promote 新建分支的精确成功创建回执，指定确切 Issue 及当前 revision/assignment epoch，把当前准入的 owner 新来源按消息双 revision CAS 关联到原责任。保留全部原关联、owner 修正与新来源 review fence，不创建 Issue、不隐式 reopen/转责/复活 Task，不改 plan/完成/交付事实。旧无可证创建来源明确 unknown/refuse；默认 create 与合法相似但不同的 Issue 不变。共享 Main 控制准入依据实际 native delivery/consumed steer 及原 logical execution 的恢复链，不把分类或 adoption 可扩充的复查集合当新授权。旧 active 显示关联原样保留，不因此转成 canonical 控制权限。真实澄清继承仅可更正原 native 已准入、相同 source/operation/target/intent 的确定失败委派；不把 adoption 当一般新授权，不重发未知结果。Refs: docs/plans/explicit-issue-reuse.md。


## D164：跨 Issue 的精确不可变证据授权

授权消息的 source/revision 仅作 share 与 revision reason 的审批 provenance，不自动写入可授予一般材料访问的 source_message_id 关联。

共享以 artifact ID、原 ref SHA256 和文件 manifest 为最小对象；目标 Issue CAS/assignment
epoch 与原请求身份在同事务绑定，share 自身 revision 支持撤销和历史读取。owner 可显式
授权；主 Agent 必须 relay 当前 turn 准入、版本相符且未撤回的 owner 来源，普通 manager
不能自授权。仅 artifacts_read 和 exact file download 增加 grant 检查，源 Task、兄弟产物、
Issue 上下文和业务控制继续使用原权限。换代后的新 manager 不能继承原 grant；撤销
不追溯删除已经合法冻结的 worker 输入。CLI 读回完整身份、状态和适用性；原报告直接
复用，不复制内容或重新调查。见[实施计划](plans/scoped-artifact-sharing.md)。

D164 输入冻结补充：首次绑定新 Task input 必须在创建/更新 spec 的同事务校验实际 manager Issue/epoch 的当前精确读权限；不能经 spec.inputs 绕过撤销。旧 spec 已冻结的同一输入保留独立权限；owner 原显式配置输入不降权。角色有规划权限但所选新输入不合法，记录确定 invalid_request 回执，不把失败请求在日后 grant 后赋予新含义。

## D167：由创建者决定独立业务验收

创建 Issue 的真实 Agent 身份来自服务端已认证工具执行，不接受输入自报创建者。顶层主 Agent 可以判断顶层事项策略；用户在 CLI/Web 的显式选择保留 owner 权限和真实决定者。执行 worker 与重新分配后的 Agent 不获得免验权。缺省或历史来源缺失保留既有强制规则，不反推历史创建者。

政策变更使用原 request_id、Issue revision CAS 和 assignment epoch，保留不可变创建者、真实决定者、理由及来源。新 Task 在创建事务中冻结政策；后续调整不影响已有 Task、独立验收或人工决策。免验只取消自动生成独立 review 责任的需要，合法报告、文件或代码结果仍按当前 spec、代次、context 和原输入检查。

免验完成的依据是当前真实 artifact 与创建者政策，不是 passed acceptance。完成通知、待核对事项、Issue 结果证据及持续交付责任明确保留这一差异。合入、部署和外部观测仍须通过各自真实证明及控制门，不由报告、完成或政策推断。仓库 PR 代码审查及 CI 完全保持。见[实施计划](plans/creator-acceptance-policy.md)。
### D171 原问题答复附件的精确读取权限

原 worker question 的 owner 回复或明确 ReplyTo 问题的补充消息，可以通过独立 request_id 和问题输入 revision CAS 授权指定不可变文件。原 AnswerJSON/spec/执行 pin/lease 不改，事件逐问题追加，撤销挡未来读取。权限只覆盖冻结的原 Run、lease、角色和仍适用的责任，不向同 Task 新代次或未证明的 continuation 继承。主 Agent 必须在已准入执行中引用真实 owner 来源；授权 provenance 不给予整消息材料权限。现有 question 读取提供文件 metadata；不承诺旧执行器收到主动注入。Refs: docs/plans/worker-answer-inputs.md。
## D169：原 manager 的有证据独立对账

retained 不代表原运行已经恢复。新的 typed 诊断和独立收束动作仅在原成功 close、
事件尾序号一致且全部应用、没有未对账 native 或业务回执时释放该次旧 lease，保留
Task 的 waiting 责任，后续仍走正常 admission 以新 Run 承接。owner 或实际准入的
主 Agent owner 来源通过 Task CAS、Run/lease/close request 和原结果 SHA 指定事实，
不接收手写停止断言，不改原 pin、回执或请求结果。未知响应继续同一完整请求身份；
未对清业务回执时先保 unknown，原回执归账后仍以该请求续进。
成功 close 后的新原生活动会使旧停止证明失效；确定未执行的拒绝和真只读不会伪造成继续执行。
参见[计划](plans/retained-manager-reconciliation.md)与[操作合同](manager-reconciliation.md)。

manager 关闭时如果只剩回复 delta，没有原生 item/turn 终结事件，协调 attachment
在真正保存 interrupted/unknown 累计回复后，显式确认该回复最后已消费的原 run/seq
及同 thread/turn/item 的 delta 前缀。普通 streaming 保存不确认，保存失败不确认，
确认事务还须匹配原 delta 身份与已保存终态正文，不能将旧终态的 no-op 视为新前缀已保存。
draft 只记录成功消费的协调游标，确认时映射回各原 Run；前缀内任何未消费的原 delta
仍保持未应用，不能因后续片段已保存而覆盖一次事件处理失败留下的缺口。
确认失败仍可用原身份重试。此确认不是原生 turn 成功或业务回执对账，不删除未知效果；
成功 close 的完整 applied 守卫和 runner 冻结的信封、请求 ID、末游标均不变。
见[实施计划](plans/manager-native-rpc-reliability.md)。

已 lapsed 的原 manager 若仍有 closing 与未决业务收据，普通 active-only admission
不再承担其回复事件消费。BackgroundPool 在正常 claim 前可识别原 task/run/lease、
冻结 pin/epoch、原 thread 和已确认 resume 全部匹配的数据库回放范围；它不创建
harness Session，不查询模型，不 enqueue 原生 RPC，也不恢复 lease 或生成新 Run。
同一读取快照固定原 item 的回复 delta，包括重建全文所需的已应用前缀；完整消费后
复用上述持久化确认。回放的记录事务要求已存可见或后台正文仍是本次正文的前缀，
不能用剩余 suffix 覆写旧输出。普通原生终结处理保留既有默认语义。错误身份、非回复
事件或不兼容正文继续 unknown，晚到 close/stopped 仍由原凭据和信封正常归账；
该本地读取边界不代表原生 turn 结束或业务结果已确定。
见[实施计划](plans/retained-manager-db-replay.md)。

数据库专用回放计入维护 drain，但不能作为 live interrupt 的执行证明；准入检查与 pool 注册共用 drain 锁。原 interrupted 正文可按同 item 的真实累计前缀延长，其他终态不因此可改写。未发布 background buffer 的精确终态正文也可证明持久化，确认 delta 不发布或结清其 batch。

原 execution 为 logical repair 时，内部回复 delta 不得发布成用户正文。数据库回放实际消费完整固定前缀后，在同一事务重核原 repair/pending/run/thread/turn/item 身份与原始 delta 内容，保存仅含来源、消费序列及累计摘要的内部审计，再更新 applied；原始事件保留。此确认不代表 native turn 终结或业务成功，也不清理业务收据；普通回复的终态正文持久化守卫不变。

## D173：有停止证明的原业务回执读取

manager transport 的 failed 不直接代表业务失败。会话清理前的结构化退出日志只冻结当场确定的 Issue/Run/epoch 和 conversation，并分开记录原 serve 返回错误、context cause、关闭触发分支和后续 cleanup error；pool 在 cancel 前记录退出阶段、原错误和直接处理的 delivery request。日志不缓存跨回合或重连的 thread/turn/request 身份，未知字段省略；显式关闭仅记录调用处已知的 initiating request。日志不包含正文、prompt、工具参数、环境或凭据，不新增业务效果，也不代替原生回执和物理停止证明。详见[观测计划](plans/manager-session-diagnostics.md)。

原 manager 成功 close、原事件全部应用、独立 stopped 请求已成功且末游标相同，仍不代表原业务完成。若唯一剩余阻塞是原业务收据，`task reconcile-manager --mode read_receipts` 可在同一事务中校验 Task CAS、原 Run/lease/host、assignment epoch、冻结执行 pin、close 原结果和 stopped 原 payload 摘要，结束旧 transport 并创建唯一 receipt-only 后继。原业务请求、未知结果、native pending 与原始事件均保留；相同请求重试不增加代次。默认 settle 仍须业务收据全部对清。

reader 使用新 Run 的正常 lease，不能续活旧 lapsed lease，也不能绕维护、park 或其他执行占位。它只按原 binding resume 原 thread，沿冻结 AppliedModel/provider/effort；缺身份或配置继续阻塞，不以当前模型目录、业务 readiness probe、新 thread、摘要、迁移或业务工具替代读取。只有原身份的真实 native 终态与完整 consumed sources 能经既有对账事务结清原 pending；物理停止、内部 repair 审计或日志里的终态线索不能代替该回执。

实现复用既有 receipt-only 动作与普通请求去重，并新增可选 Assignment `require_receipt_reader` 标志。默认 false 保留历史 receipt-only 的 resolver、模型恢复与 native 操作规则；仅新 proof-bound 来源且已协商能力的 reader 为 true，启用冻结配置/noModels/严格 resolver。该临时约束不进入原 thread 的 binding identity，重连与后继代次从原 action 来源重建。字段可选且默认语义不变，不升级协议代际。新模式仅对显式报告 `runner-manager-receipt-reader-v1` 的原 host 准入；该模式新动作的 claim 与重启恢复同样要求此能力，避免旧执行器在读取前做普通业务探活。原待对账请求集合尚未清空时，非终态或失败读取后的后继代次继续携带原请求来源与能力要求；该原集合全部对清后，不把限制传播到无关责任。原 closing Run 的事件消费优先恢复同一 Run、精确 thread 的已确认 resume 内实际模型配置；仅历史 resume 缺该配置时再读该 Run 已有缓存模型目录。不能为了缺少当前 hosted turn 快照而切断原事件泵，也不因此访问当前模型目录或创建 native 会话。已持久化 compaction 的账本对账仍可完成，创建 compaction 与其他 native 维护继续禁止。历史 receipt-only 与普通业务协议不因此收紧。新 runner 的内部受限解析跳过业务探活，普通执行保持既有探活和模型选择。部署需同时更新实际 runner 才具备这条受限读取实现。见[实施计划](plans/proved-close-receipt-reader.md)。

## D174：回答事实与执行续接权限分离

新增 `decisions.respond_continuation` / `decisions.resolve_continuation_from_message`，
要求显式完整 typed policy、原 decision version 及父 Task control revision；worker handoff
的条件答复通过新入口把机器续接意图与自由文本分开：
`continue_now` 在原执行确已停止且原合同仍有效后续接；`wait` 保存非空原因并等待独立的显式
resume；`dependency` 保存另一个普通 execution Task 的精确 spec 版本，仅该版本的真实 completed 账本事实
满足条件。Task 前提、canonical Issue 与 legacy stage 的等待边在同一纯图中检查；
新增 dependency 答案及后续图修改均在原事务中拒绝环，不能由另一条入口绕过。
旧 `decisions.respond` / `resolve_from_message` 的有效无策略输入保留原有
answered 与 stopped 齐备后的续接默认；没有 policy row 的历史 handoff 仍沿原合同，不伪造
一个旧 wait 决定。新入口缺 policy 拒绝，只有显式 typed wait 才等待；不从自由文本推断
typed 条件。既有 Attention 通过非空 typed 字段显式 opt-in，省略时保持旧行为。
新 typed IssueManager 答案必须引用当前 Issue 已准入且未撤回的原 owner 消息；
复用既有 owner relay 校验，不将 cancelled outbox 当成仍然有效的 owner 授权。

答案及其原 request_id 保持不可变。独立 continuation 记录保存自己的 revision、等待原因、冻结
依赖和 resume 请求；`decisions.continuation.resume` / `reconcile` 同时校验 continuation revision
与父 Task control_revision，并继续校验原 spec / action generation / artifact / Issue assignment
及执行 pin。reconcile 只能读取已冻结依赖的真实就绪事实，不能用客户端布尔值解除等待；wait
只能由独立 resume 解除。托管主会话及当前 Issue owner 通过已有 execution/effect_seq 和 source
准入执行这些动作；显式 resume 绑定实际 owner source，技术依赖对账不要求新的人工答复。

真实 Task 完成事件通过 dependency 索引定点唤醒相关等待项；创建后尚未领取的 continuation
仍在 scheduler 事实收集时重查冻结依赖，依赖改版或未完成不能因旧 ready 投影被领取。全过程
仍保留原 handoff 的进程停止证明、原协议与执行器 pin、原 generation 和工作区合同，不创建
新的业务责任、不占用等待中的 worker 槽位、不重放旧业务请求。CLI、owner API 与托管工具
提供一致的 typed 输入、只读阻塞事实及双 CAS 控制；输出明确区分 waiting / ready / continued。

迁移 89 只追加独立 continuation 账本，不改写历史答案或伪造旧请求的策略。已有已提交
successor 动作保留身份，legacy 查询不物化 policy；新的独立 resume 仅记录本次权限，
不补造旧 answer request。

兼容评估：初版把旧无策略默认改成 wait，确实改变旧有效请求，不能仅以字段可选或测试绿
视为兼容。现通过独立新 operation 与显式 opt-in 保持旧输入、旧请求回执、无 policy row 的
续接以及旧 yield 合同。N/N−1 逐项验证旧默认不变，新 typed wait 独立验证无后继。runner
wire 字段和既有 capability 的默认语义均不变，因此不提升 wire 版本，也无旧通信适配器
需要登记退役；未来若要移除旧默认，必须独立实施版本化迁移。详见[兼容修正计划](plans/decision-continuation-compatibility.md)。


## D179：仍持租约的原关闭流程消费内部输出

原生进程的成功 close 回执可已持久化，而 server 因尚未消费的内部 repair delta 保持未知；不能以停止等待、重复物理关闭或伪造业务完成解除该等待。原数据库 prefix 消费器扩展到 active 且已进入关闭流程的原 manager，但新增分支仅接受唯一原 pending repair receipt、原 close、已确认 thread 元数据、冻结 runtime/pin、Issue 与 assignment epoch，且固定 slice 只能包含该 repair 的完整 agent delta。active 普通用户答复不进入此分支，原 lapsed 普通回复和 repair 行为保留。

BackgroundPool 在同一 conversation 的既有互斥与升级准入内先消费固定数据库 slice，不打开 harness、不申请新租约、不启动原生线程。内部 repair 正文仍仅保存在原始事件；按完整已消费 cursor 和正文摘要原子记录审计及 applied，包括空正文。晚到 delta、不完整消费、身份变化和事务失败不能被确认。消费 prefix 不产生 native terminal，不发布 repair 为用户答复，也不结清原业务 unknown。原 close 的 all-applied 接纳门保持，原 runner 的相同 Envelope 自然重传后才释放原物理责任；旧排队命令沿既有停止结算明确记录从未执行，已领取的未知操作保留原对账。

同一 run 的 pending、started、unknown close 复用原控制命令，原 payload、结果和 request ID 不变；已有升级 park 继续拥有新 close 的准入与专用错误围栏。普通 completed-error close 的旧有效通信行为保持，不能仅以无字段变更为由收紧其后续请求；改变该状态另需版本化设计。find 只合并同 run、相同完整 payload、尚无结果的未决只读查询，完成后可取得新鲜读取；不合并 turn、inject 或 tool_result。历史积压保留原记录，不直接删除或改成成功。本次不改 runner wire、journal、旧有效 payload、物理停止证明或新的业务权限。详见[实施计划](plans/manager-native-transport-liveness.md)。

原生 reply 回执的持续未确认与传输失败由同一个本机循环处理，每轮只发送一次原 Envelope，包含 HTTP 500 包装的 unknown。首次等待保留 RetryDelay（默认两秒），随后加倍至默认 30 秒上限；显式配置更长间隔时不缩短。明确拒绝立即返回，context 取消保留原 journal 请求，恢复仍先对账原 close，不重新打开 native 或生成新的关闭边界。非 reply RPC 的通用重试规则不变。原事件消费完成后在下一次有界重传取得确认；该退避不改变 all-applied 接纳条件、原业务 unknown、通信协议或持久格式。见[回执退避计划](plans/manager-native-receipt-retry.md)。

## D178：验收未满足与执行受阻的事实边界

约定的质量门已经执行且未满足时，`changes_required` 可以要求实现方诊断并解决该失败，不要求验收者先证明某一产品缺陷；执行结果与根因归属必须分开。`blocked` 仅表达有证据的外部前提阻止验收，`inconclusive` 表达证据尚不足以判断要求是否满足，二者都保留原证据、后续调查及负责人处理责任。验收者仍不修改实现。

历史 `acceptance_env_unavailable` 同时用于上述两类 verdict 及验收 Run 自身失败，仅是兼容性的待处理码，不是环境故障证明。保留 wire、历史 verdict、调度和既有显式重验收入口，Web 将该通用码展示为“验收待处理”，CLI 文本同时保留原 verdict、Issues 与 Evidence。负责人沿当前原 Attention、Task 合同、CAS、assignment epoch 和预算安排有界后续；没有 effect 的 Task 规则答复不自动产生 worker handoff 后继。metadata repair 只能完成其原上下文义务，不因此获得业务续办权限。

本次不新增 verdict 必填字段、结构化硬约束、返修 effect 或自动重试，也不改变旧有效请求。worker 提示随 runner 版本生效，只有 server 部署不能证明旧 runner 已采用新合同。运行中的未知事实与原生收口另行验证，不能以文案改动认定现场已恢复。详见[实施计划](plans/acceptance-failure-contract.md)。

## D175：TraeX 原 rollout 的终态补证

独立原生历史连接可能把仍活动的其他连接回合投影为 interrupted，但缺少真实结束时间；不能删掉 ending_unconfirmed 守卫或以观察时间制造结束事实。已冻结的 TraeX runtime/session/launch/turn 与工作区身份完全匹配时，可只读原 providerSession 指定的 rollout，核对 managed launch/input、原 session/turn 和显式原生结束事件及其时间。该补证只在原 observation record 位置的局部副本上发布真实事实，不覆盖冲突身份或已有确定原生结果；生命周期 Agent 始终沿用原 native-api 观测，补证不参与完成、恢复、控制或历史清理。每轮只发布一次 projection，不反复切换原弱观测与补证 revision，不创建会话或重发输入。TraeX 观测以持久语义指纹区分相同原生身份的 running 与后续终态/结束时钟/operations；排除采样时间，真实变化只推进一次 revision，相同事实及重启重放沿用原事件身份。指纹仅用于观测去重，不改变原生绑定或生命周期权限；旧 thread JSON 缺失字段仍可读取，首次补齐语义元数据可追加一次事实。原生取消仅证明 turn cancelled，不证明物理进程停止；工具调用、原生进程和子工作收敛独立校验，缺失或未知保持 pending/unknown。补证本身不授权新的模型 continuation，已有 Codex 与直接原生回执路径不变。无需新 wire 字段或迁移，实际 runner 必须具备实现。见[实施计划](plans/traex-root-observation.md)。


## D176：Token 用量写时投影

原始事实与 current 身份不变。追加迁移 91–94 建立可重建的请求贡献、四小时汇总与持久 dirty 队列；app 的统一事务收尾解析受影响请求的归属、逐请求价格和有效计量，只更新受影响的汇总桶。任何失败连同业务写入一起回滚。服务在开放监听前处理历史补齐，不在 GET 中补算，也没有新的定时任务。

归属证据、执行状态、Task 标题、Issue 标题/父链、冻结 inventory 和本地 actor 的真实变化登记相应历史请求。覆盖状态、游标、普通心跳和无关 Issue 内容不触发历史重算。当前报价内容改变时写时重算价格，报价刷新但内容相同时仅推进报价时间。冲突隔离、legacy/native 重叠及其回退沿 D117/D157 原合同维护。

读取以同一快照中的汇总为主；滚动或自然时间窗切开的桶读取已投影贡献，逐请求明细最多读取 51 条。分组列表只读游标而不取明细正文。原生未来时点在查询截止之前仍算时间未知，时钟推进会自然转为已知；未知时间不进入趋势，缺失计数不填零，精确十进制逐请求计价不变。API/CLI 的窗口、归属筛选、分页与输出合同保留，runner wire 无变化。详见[实施计划](plans/token-usage-projection.md)。

D176 写入规模与 UPSERT 补充：dirty 队列使用显式 `ON CONFLICT DO NOTHING`，不依赖会受外层 UPSERT 覆盖的 `OR IGNORE`。刷新查询固定由 dirty 身份驱动索引连接；托管执行按原生 turn 定点失效，本地 actor 按 conversation 与 thread/parent/session 索引失效，避免新用量或新执行扫描历史贡献。

D176 汇总大小补充：每个四小时桶再按不可变 fact sequence 分片，每个汇总最多包含 128 条请求身份。时间未知或未来时点也遵守此界限，单次追加不再随其历史积累而扩大写事务。迁移 94 只登记投影重建，不修改原始时间或事实；读取仍按原窗口合并这些小汇总。

## D180：有界维护窗口与外部接管

独立本机 server 及每个 runner 目标共享持久预算：总计默认 360 秒，handoff 60 秒、admission fence 30 秒、观察排空 60 秒、优雅停止 30 秒、精确强杀 10 秒、备份 30 秒、启动与健康 90 秒、commit/link/恢复 30 秒。首次进入阶段先保存时间、boot 与单调截止及原 inflight 身份；轮询、重开及同请求重放不续期。显式延时必须新 request、fresh CAS 与审计，保存旧阶段完整 deadline/outcome。逻辑排空到期可使用先前保存的 force-on-timeout 权限；没有该权限时只输出真实超时及外部入口。

普通准入条件保持。新增本机显式 force 接受原组件的未完成和未知，包括不完整诊断、未响应 ACK 和 HostedDrained=false；这些事实不改写为 drained、healthy 或业务完成。独立 installer 可在 control socket 或进程 mutex 失灵时，验证固定配置、产物、实际 updater PID/start/hash、unit 与原 flock；有界请求 manager stop 以抑制自动重启，再只终止精确旧 updater 并保留 child。同 inode 的锁须真正释放且被本控制器持有，旧 state/CAS 再核无漂移后才接管。原 server/runner writer 未确认退出不得启动第二 writer；native child 的退出与业务效果仍是独立未知。丢失 stop/start 回执只观察原身份，不发第二 start。copy/fsync 的外部截止由独立 installer 进程边界保证，不能用 context 包装宣称文件系统必然可中断。

server force 绑定固定配置的实际 ledger path/device/inode，授权及停止、备份、启动边界重新核验，不能把请求中的数值本身当作物理证明。schema 版本只保留来源信息，不把迁移后仍相同 inode 的库拒于恢复之外。普通 apply 的优雅停止超时不授予强杀权限，必须有相应持久 force/takeover 或预授权超时事实。

updater 新模式使用 format 4；旧 1/2/3 继续读取，进入 4 后取消、重开、普通后续维护和前滚均不降回旧格式。旧程序在写前拒绝 4，不能通过宽松 JSON 解码擦除权限。不可变 source plan、启动/恢复 lineage 与当前预算授权分开：fresh window 可更换预算 request，但原 launch force ID、原 grant、冻结 run set 和旧窗口保持。status/reconcile 按原任一 request 读取历史，不能因为新窗口归档而遗失旧身份。候选启动或迁移之后只前滚；失败 force parent 由独立新候选及新授权承接原真实未知，不把原 false archive 当作普通排空证明。

新 server 在任何业务入口之前从实际 updater format 4、自身 kernel identity 和 predecessor exit 验证 force。注册与数据库全量 open run 保护同事务，包含停止前诊断遗漏的晚到 run，保持原 lease、pin、请求和未知；prelink 原生 fence 保留。原 started summary 没有可用 fork 身份，按实际 post-stop 数据库冻结其 source lineage，防止自动重 claim、消费、发布或覆盖旧 verdict；不同独立 source 保留原能力。该保护不是部署前等待旧 native 完成的门。

runner force 需要独立 owner/CAS grant，不从普通升级 grant 或本地超时推导。配置 NodeSelector 只证明配置成员；物理目标由本机固定 instance、实际进程及 journal device/inode/incarnation 核验。journal 17 在迁移前先核 force 身份，真实文件锁覆盖数据库 inode 的整个 writer 生命周期；旧 16 在写前拒绝 17，15 可完整迁移。新 runner 在 admission 前事务保存原完整 run/request set 的持久隔离；普通 Continue、缺少终态证明的 Stop、重开和清理不能解除，也不假释放资源。仅新的匹配 server 终态 Stop、已保存的原成功 terminal ACK 和 StoppedConfirmed 共同成立时，才窄收口该原 Run 并归档未知事实；这不授予原 native 重放。owner 的逐 run strict continuation 保原 assignment 与 request；独立 release-admission 必须有绑定原 target 与当前实际进程的 committed updater receipt，同一 lineage 只接受一次独立 release 身份并保原请求去重；它只解除全局新业务 fence，未知 run 的隔离、占用和事实继续保留。进程安装健康与业务 Ready 分开。已 committed runner 确认退出后，既有 supervisor 只重启同版本 immutable bytes：原 PID 与 launch 均无 live writer 后保存独立 restart attempt、新 launch 和旧 operation，保原 force window/SourceExit。后继无 force hint，仍读取 journal quarantine。取消维护仅保留原 updater 已 exact-owned 的 source-version 监督，不为 Process 为空的 manual source 新增永久 adoption；未知仍交独立新 source plan。App/runner 使用既有 committed UpdaterReceipt 的原 Binding 与当前 ProcessInstanceID/SHA 后继证明，接受新的 generation 和 fresh owner release；不重用旧 InstanceID，不因进程监督授予业务/native 恢复权。host usage apply 由持 writer 的 live runner 操作原 journal，维持旧 Envelope 去重而不引入第二 writer。

兼容性：wire 仍支持现有 N/N−1。新增 owner operations、本机 bounded 命令和明确能力协商的可选字段，不改变旧有效请求、普通 grant/health 或缺省 retained continuation 语义；旧 peer 不获得 force 权限。format 4、journal 17 与 server schema 是独立持久边界，发布范围须覆盖实际源 schema 的完整迁移。已安装并 ledger-linked 的 force 维护若从原不可变 force audit 及 fresh 完整 archive/host control 基线证明未建立待恢复 fence，且原 prepare CAS 已落后、原 host 均保持既有 maintenance，可在原有效 complete 窗口和 CAS 内只记录审计并完成，零外部写且不重启服务；身份或基线不符仍走原严格恢复。已 commit/link 后仅替换 updater 使用独立 versioned continuation/head 固定新 updater candidate、原 handoff journal hash 与当前 retained service kernel/proof；原服务计划与 journal 不改，启动每个边界验证来源，零服务 Stop/Start，丢失回执只对账原 attempt。已记录 active force 的精确原请求先去重，过期不阻止只读返回原结果，也不续预算；缺少 archive observation 的普通前滚明确拒绝并要求独立 force。runner 初始 Hello 释放 admission 后，仍保证 receipt/usage 循环恰好启动一次。CLI 同步提供 plan/apply/force/extend/cancel/status/reconcile/takeover、owner grant、逐 run 继续和 admission release，保原 request/CAS、完整输入输出、dry-run 与不依赖旧 socket 的诊断。

独立 updater-only 请求在 installer window 建立时即占用原 ID。controller 以真实 PID/start/hash 识别，launch 可以为空，不将业务进程的 launch 条件套到 controller。新的启动前计划以 request ID 和完整 journal hash 继承一个已 adopted 的启动凭据，保留仍安装版本的启动权；starting 或未知 Start 仍只能核对当前原 attempt。已确认 controller 退出、同锁与 retained service 完整保持且无 pending Job 的启动前失败，可以通过新独立授权继续，保留真实 failed 状态。以上不放宽业务进程身份或改变 updater state 格式。

原 committed handoff 固定的 lock device/inode 贯穿后续 updater-only 授权、启动前恢复与采用，新授权不能替换这份身份。ledger-linked 后既有正常监督可更新当前 committed process proof；后续 updater 替换及无外部写的维护完成，以稳定 successor binding、当前版本与最新 process proof 校验当前服务，原安装 receipt 与 maintenance process 仍保留并校验原身份，未完成或未知的操作不取得该权限。

原 maintenance 的只读 status 与原请求 replay 可凭已 adopted 的独立 controller 凭据识别新 updater，必须核验原 handoff、config、manager、实际 controller 和原锁连续性。返回真实持久 state/phase 与观测，保留服务 pending、unknown 或失败供诊断；读取成功本身不证明服务健康或新的安装成功，也不增加 Start。

配置摘要的读取与执行入口统一先补齐现有默认值；文件中省略默认 stop/health 超时不改变配置身份，也不改写文件或原凭据。

本次实现独立 component/host 窗口，旧批量 coordinator 已固定 targets，但总预算仍会随旧阶段/host 推进重设；该 batch 总 deadline 明确留作后续，不声称本次已修。独立本机 force 不经过旧 batch 的等待门。详见[实施计划](plans/bounded-maintenance-recovery.md)、[CLI](bounded-maintenance-cli.md)与[实际进程验收计划](plans/bounded-systemd-acceptance.md)。

## D181：验收证据文件的持久交付

验收可显式选择文件，经 D87 不可变内容存储上传后，在原 verdict 事务中绑定完整文件清单；passed、changes_required 和 blocked 使用同一合同。上传未确认时不确认 verdict，也不因本机路径可读就声明交付成功。evidence JSON 原样保存，不解析其中的路径或自动收集 scratch。原 scratch 仍按既有生命周期清理；正式文件引用提供清理后的原字节读取，不承诺恢复已经删除的历史原件。

文件能力和反馈 AcceptanceID 在首次 claim 的同一事务中冻结。后续 Hello 不给旧 run 补授权，较新验收不改变已领取返修 run 的反馈来源。文件读取按完整 manifest、精确反馈、显式输入和既有 Issue 责任范围校验；同 Task、同名或同 hash 不自行授予权限。owner、主 Agent、CLI 和后继 worker 均使用实际文件身份，跨任务输入保留 source acceptance。

恢复先封存原字节和原上传请求身份，再对账原结果。上传确认与原生会话关闭是两份独立事实：直接本机 app-server 仅在实际拥有的进程 Session.Close 成功后保存关闭记录；流连接断开或引用释放不产生该记录。此记录沿用原本机根进程退出及进程组终止调用合同，不扩张为进程组已空或所有外部业务效果已知的证明。关闭时上传仍未知的，可绑定完整封存输入及原请求身份；恢复后仅与这些原请求的真实确认组合，并固定最终 verdict 字节，不能用缓存结果补造历史关闭。关闭失败、缺失或绑定不符保持待对账，不发新模型 turn 来重建 verdict。

原封存结果通过普通启动围栏前的专用只读入口恢复，验证原工作区及关闭证明，不新增 attempt 或重新准备工作区；先保存原结果，再记录停止，使中途崩溃只能继续提交原结果。文件选择在明确尚无封存 intent、上传请求或外部副作用时可以纠正并保留拒绝事实，其他未知继续沿原身份对账。 输入验证采用独立的 fresh 只读阶段，只有该阶段能明确拒绝无效路径，读取原身份或 intent 的失败不降级。文件 verdict 确认后，任何后续调用在写 pending 前核验 durable 原输入；同输入幂等，不同输入拒绝，原 ACK 与关闭范围保持不可变。 尚无结果目录或候选的普通已停止验收交回原启动围栏，不把专用入口变成额外永久阻塞。特殊文件在阻塞打开前拒绝，打开后复核普通文件；上传取消后仍以独立有界的本机只读审计保存真实完整封存身份，使实际关闭记录不依赖已取消的网络 context。

线上协议仍为 2/1，新增可选能力必须显式协商；未协商的旧请求保持原义，缺少文件清单不代表文件已持久化。server schema 95 追加验收清单与冻结合同，runner journal 18 防止旧 17 writer 恢复时静默丢弃新引用；真实历史迁移保留原请求、执行身份与隔离。具体接口、权限与验证见[实施计划](plans/acceptance-evidence-files.md)。

## D182：已关闭 manager 的原工具生命周期事实

原成功关闭回执仍须精确覆盖事件尾游标，并在同一事务内确认全部事件已应用。
对于明确属于原内部 repair turn，且已存在同 Run/thread/turn/call 身份、成功投递且未 rejected 的原 `tool_result`，
可在该事务中消费与原调用严格匹配的 `dynamicToolCall` started/completed 生命周期
元数据。projection、raw、工具与参数身份必须一致，结果语义以原持久化 payload
为来源；业务工具失败可以是真实已投递结果，不能从 native completed 推导业务成功。
消费保留原事件和结果字节，审计分别关联原 native close 与回执 Envelope 请求、
事件序号与摘要及原结果请求。被 rejected 的 close 不授予此事实消费权限。

普通业务 turn 的 rollout/tool use 仍由原消费者保存，不经此入口跳过用户工具展示。
该路径不执行工具、不发送 native 请求、不恢复会话或 lease，不结算原逻辑执行、
repair 或业务 unknown。缺失、未确认、不匹配或其它类型的事件继续阻止关闭，
任一拒绝保持整个关闭事务回滚。原 close/stopped 真实归账后，后继 receipt reader
仍须满足原独立合同。既有 bounded fact replay 沿原 Envelope、ID、次序和单次预算
输送；不以维护完成或全局 admission release 替代逐 Run 的停止与业务证明。

工具 callback 与 native item 通知可以异步交错，匹配不要求 call 先于 started。
已无未应用事件的关闭不进入新增历史消费者；需收束的事件按原身份分组，已应用
的整组在查询证明前跳过，使新增匹配开销只对应尚待确认的生命周期事实。

无 wire、schema、journal 或 owner 权限变更；继续使用现有关闭入口与 manager
对账 CLI。详见[实施计划](plans/closed-manager-factual-events.md)。

## D183：显式声明的 Capability 与双角色冻结匹配

只读 Agent list/show 及 Task explanation 候选用 `definition_statuses` 将原精确声明与定义当前
revision/state 分开呈现。当前退役说明新引用被拒绝，但不修改旧声明、冻结需求、pin 或原匹配结果；
定义改变也使投影分页 cursor 失效。CLI 透传完整字段，原始注册历史保持不变。
见[当前定义状态投影计划](plans/capability-definition-status.md)。

Capability 为独立的版本化业务标签。定义由 owner request/CAS 追加，Agent 配置显式声明
definition revision；同 Agent revision 的声明不可变。Task implementation/acceptance 各自
冻结 required，新增 exact reference 检查 active 定义，原角色保留引用允许历史继续。
Agent 注册保留已接受 identity/revision 的原声明，包括移除后恢复；定义退役不阻断
无关 Agent 更新。新 Agent identity 或 revision 仍检查 active 准入，同 revision 改声明
仍被 revision fence 拒绝。见[兼容修复计划](plans/capability-retired-declarations.md)。
普通 executor readiness、角色、runtime/model/effort/backend、共享预算及 retained pin 约束
仍由同一 scheduler 决定，定义标签不证明 native 工具或实际执行。

claim 原事务保存 immutable binding。重连按原 snapshot 读取声明，runner 在 NextAttempt
之前验证原 pin/digest/声明并沿普通 Start 执行，保持原停止证明、unknown 与请求对账。
协议 2/1 增加可选 declarations 协商；旧 peer 和无 required 的请求语义不变。
当前 schema 97 和 journal 18 仅分别追加到 98/19，不复用未发布历史候选的冲突编号。
Agent 查询、Task 解释、CLI/API/只读工具与等待通知共用真实调度事实；cursor 固定水位，
并在可变候选事实改变时拒绝旧页。无声明、无 required 的旧数据兼容，不创建工具资格路径。

详见[实施计划](plans/capability-current-main.md)与[接口说明](capabilities.md)。

关联维护装配验证沿 D133 外部边界注入已有 controller interval，隔离测试消除固定轮询相位
等待，生产空依赖保持原默认值。它不放宽 readiness、物理身份、独立 updater/DB link 或原
总 deadline；原失败、阶段样本与完整检查分开保存。见[阶段证据](findings/capability-maintenance-deadline.md)。

后续阶段预算修订将真实 fresh 准备与 production reopen/恢复分开，各 25 秒，HTTP 仍为 2 秒，
父 helper 总期限为 55 秒（含 5 秒退出余量）。这是原单一 25 秒总窗口的明确增加，取代旧调查
不得移动起点的限制；其他指定测试预算保持。见[阶段预算](plans/production-assembly-phase-budgets.md)。

## D184：负责人判断任务约定的适用性

Issue 的已提交变化进入现有通知窗口及后台批次，由当前负责人判断状态说明与真实任务变化。自产处理不形成循环，也不取消尚未处理的外部变化。来源审阅完成后，剩余的任务影响判断继续作为正常协调责任。

负责人可选显式确认继续原约定，独立保存 Task/spec/CAS、Issue 责任代次、原冻结 digest 与已处理来源。D193 取消逐 Task 适用性确认作为调度和结果准入的正常前提；普通来源、定义和祖先 digest 变化只通知当前负责人，不自动暂停任务或拒收真实结果。原确认及冻结 spec、输入、候选和历史执行保留。负责人判断真正要求变化后明确修订、暂停或继续；实际责任变更及未知冲突仍按原身份处理。

## D185：server 统一交付观察

server 通过显式 provider 配置与原生 CLI 读取 PR 和 CI/checks，按交付身份共享事实，再沿原事件及批次通知相关负责人。读取和重试有期限；缺失配置、鉴权失败、限流、截断或未知不能伪装为通过，也不能无限 pending。CI 明确绑定候选 head，按条件跳过与成功分别保存。

新服务领取有索引的到期观察任务，不以高频全局 Issue 扫描代替调度。切换等待或记录原 runner 已发查询的期限、nonce、实例和结果；迟到事实保留审计，不能覆盖新服务的新代次。现有旧协议入口和未启用新配置的合法请求保持兼容。候选准备、执行与交付完成使用一致的程序事实。已绑定的仅合入执行在 server 证明当前目标包含原 merge 时，可在目标随后前进后沿原任务收尾；暂时无法确认时保留原绑定并等待有界观察恢复，不重发合入。部署仍核对精确授权来源。

## D186：Issue 合入授权与共享 PR review

PR 交付在 Issue 中明确自动合入或人工审批。自动合入只覆盖真实授予的仓库与范围，满足独立审阅、检查和保护规则后由 Tend 自行推进；人工模式才准备具体候选并等待版本化答复。历史批准按原范围保留，自动策略不能伪造一份人工批准，合入也不自动授予部署。

同一 PR 的 Task 与子 Issue 共用整份交付的唯一正式 reviewer。实际独立执行产生结构化 findings、处置及结论，绑定候选与需求范围；符合要求的同一执行结果同时满足 Tend 和仓库要求。建议一次初审、一次集中修复及原 reviewer 复查以促进收敛。D193 取消默认两轮后的人工授权硬门；负责人在既有真实用户范围和成本预算内安排必要后续，记录实际轮次、处置与升级历史。旧外部额外复查授权继续作为历史可读，Agent 不能伪造通过或改写真实用户预算。已通过且仍适用的结果继续复用；观察暂时未知不消耗预算、不冒充真实候选变化。原有效候选不因新增关联重复审阅，变化后明确哪些证据仍有效，不通过负责人填写 passed 代替真实审阅。

每轮冻结关联任务明确要求的模型与思考强度，按实际 Run 配置核验；省略选择字段只表示使用 Agent 的真实默认配置，不能绕过要求。报告任务经 `submit_result` 提交结构化审阅报告；普通验收通过原 `submit_verdict.evidence.delivery_review` 提交整 PR 审阅证据。可选 `reviewer_role=accept` 在结论前绑定原 Task/spec、不可变产物、实际验收 Run 和精确 reviewer profile；缺省报告及已有事后验收导入保持原义。记录只读取真实不可变结论，changes_required 保留 findings 供集中修复，blocked / inconclusive 显示等待证据，不变为通过。需修改后，同 Task 的真实新产物和新验收 Run 可用于聚焦复核；等待证据后，真实新验收 Run 可核对同一产物。旧 Run 和旧结论不能重复计轮。等待证据的原轮次可由负责人安排同一产物上的真实新验收 Run；协调状态切换不将普通续办变成人工批准。D193 的 correct 读取原 reviewer 实际完成的补正报告，追加不可变来源而不覆盖旧报告、不复活终态 Task、不增加正式轮数。

普通验收的受限观察只读当前 Task/spec/产物所关联交付的真实共享审阅；Main 与 Issue manager 的具名查询复用规范视图及分页，Issue 查询保持自身范围。正常具名 Issue 启动执行工具集 hash 与既有交接，不因禁用空闲摘要而继续旧工具表。Issue 交接只注入经已注册会话核验的本 Issue 事实和继承上下文，不使用全局 Main 事实、摘要输入或旧摘要缓存。不存在本机历史索引时不登记空路径，保留原可用索引；receipt-only 与原生未知仍只核对原身份。审阅期间范围变化不拒收原范围的真实结果，而是保存历史并单独判断当前适用性；新轮清除旧候选的适用确认。共享结果变化向修改前后所有关联 Issue 发送去重事件，复用既有通知合并与自产抑制。

## D187：交付执行的受限授权视图

`delivery_authorization` 是由服务端保存的真实执行绑定，不是冻结 spec 中的字段。原路径在创建 Task 时提交独立输入；D193 允许 live Task 用 prepare/bind 继续同一工作，无须先提交人为中间结果或另建交付 Task。worker 使用自身 run/lease 的观察或普通 read_context 入口读取该绑定、真实批准或策略来源、冻结候选、当前有效性及必要的恢复和准入事实；owner Task 及材料查询提供同源只读事实。读取其他相关历史对象不转移其执行身份；授权撤回、候选变化与历史执行保持可解释。

任务需要的证据对应真实可读字段；部署验证未确认不自动阻止仅合入任务，普通进行中的 claim 不等于未结束维护。问题接续保留原始答复与证据来源，不扩张原 run 的文件权限或借补证改写冻结交付范围。以上决策与 CLI、视图、迁移及回归要求见[统一维护计划](plans/issue-change-owner-assessment.md)。

## D188：摘要准入、固定健康期限与未提交安装接管

摘要 claim 与升级及恢复围栏在同一 writer 事务判断，覆盖 idle、启动迁移、显式预备及重新 claim。围栏前已接纳的原摘要仍能按原身份保存真实结果；围栏后可选摘要暂停不冒充模型失败。工具集交接使用 D81 的缓存、当前账本事实及历史索引，目标创建、注入和 source CAS 均真实确认后才切换 binding。creating / injecting / unknown 不重复发送；D156 仍要求组件独立排空，不因跳过新摘要而宣告未知旧工作完成。

普通候选进入 checking 后固定健康截止和 boot / 单调锚点，重开与轮询不续期。authority 失败、未确认、绑定不符、Inspect 或配置错误以及读取跨截止均记录一次原超时事实；读取 context 有界，迟到真实健康仅核对原候选的两次序列，不续 stop / start 权限。永久无法确认时输出独立前向恢复责任，不自动回滚数据或再发启动。

既有独立维护增加可选 source_operation，固定原 unfinished operation 快照摘要、阶段、候选、launch、实际已迁移格式及原账本。精确 kernel process 或持久 owned child Wait 证明支持新独立授权；Find 返回零本身保持 unknown。启动器在 Start 前持久固定已验证文件摘要与单次 dispatch，Wait 事实按原 child/launch 保存，重复未知 dispatch 不再次 exec。root child 退出不证明原生子工作或业务副作用完成。

接管归档原 operation 并移出 active，真实 successor commit/link 后在同一应用事务以原 batch fresh CAS 记录安装责任已移交；旧健康零、未知与原事件保留。被接管来源不生成 staged-not-started 取消回执。runner 的该路径只继承原 force grant、原 target、journal 和完整 protection set，不能因失败候选未 Hello 而重新采集或释放旧业务。新请求和新 launch 独立，原请求仅对账。

上述关键所有权和截止使用 updater format 5，保留旧格式历史读取，旧 writer 必须拒绝 5；不降回旧格式或修改 Current 冒充完成。新增可选 wire 字段不改变旧有效请求和默认语义，只有显式模式获得恢复能力。CLI 提供完整 typed 输入、dry-run 与原请求恢复；实际发布必须更新固定 updater，并分别核对其字节与 server / runner，不以产品程序更新代替监督器生效。详见[实施计划](plans/upgrade-startup-summary-fence.md)。

## D189：部署准备的同源受限事实

明确获得部署观察权限的只读 Task、Issue 负责人和 Main 使用同源准备投影，读取本服务配置的公有受控实例、当前完整活动目标节点、逐目标运行事实与匹配的安装回执、已结清升级基线以及相关维护和交付责任。provider 数据仅来自当前获准 Issue 的登记观察；读取不访问 provider，不创建升级或定时查询。原始事实分别保留来源、时间、水位、控制版本及已知或未知，不能把心跳、历史安装或缺省目标当作当前确认。

完整准备事实不授予部署权限，不为普通报告生成交付授权。报告可以列出未结工作、缺失回执和需要的决策，不要求全局 unknown 清零、预先建立未来升级或先激活目标围栏。真正 prepare/start 继续核验当前目标、基线、原权限和版本。旧 `deployment=false`、缺 scope、旧 Run/lease 和无关 Issue 的读取边界保持；本次增加可选响应及规范只读入口，不新增必填选择器。

Issue 的必要上下文由账本、原件索引、冻结任务和实际回执恢复，原生 session 历史不是唯一事实来源。技术补证沿原问题和同 Task 的正式后继接续，不复活已释放租约，不用原 question-input grant 绕过文件授权，不把技术答案当部署批准。owner API、具名 Main/Issue 工具与完整 CLI 同步提供该读面；详见[实施计划](plans/deployment-preparation-observation.md)。

## D190：部署基线失效的共享刷新顺序

升级完成或取消导致部署基线变化时，在原事务同时使相关 Issue 观察失效并唤醒其共享 provider job。排队复用正式交付刷新逻辑，递增共享刷新顺序并绑定成员版本，保留旧查询身份与迟到结果；旧查询或旧基线缓存不能清除新 pending。相同 Issue/delivery 的多个责任只排一次，同 PR 的多个 Issue 仍共用一次外部读取。

该刷新只更新 provider 与基线事实，不授予部署权限、不修改正式审阅或批准、不新增轮询器。已合入 PR 的成功刷新仍停止周期轮询，错误沿原有限重试预算保留未知；旧 runner 协议及正式 CLI/CAS请求恢复语义不变。详见[实施计划](plans/delivery-baseline-refresh.md)。

## D191：Agent 自主读取 Issue 资料

Issue 是持续目标、相关材料与责任。负责其工作的 Agent 默认可以自行查阅已保存的原始消息、背景、问答、关联任务、产物与验收记录，并取得原件；实现、验收和后继 session 共用普通上下文能力。Task/spec 与 Run 保留当时执行和采用输入的快照，资料未被最初选入不构成阅读拒绝，旧执行结束不使已有知识失效。

普通资料索引和按对象读取保留来源、版本、当前或历史状态、时间、水位与明确分页。原件元数据和下载复用同一真实 Issue 资料关联；没有消息来源的背景文件按已有登记关系读取。Agent 判断资料是否影响任务含义，读取不自动处理消息、修改 spec、授权新的副作用或重发未知操作。无需逐文件 grant、下一阶段许可或另建调查任务。

验收的任务资料仍排除已明确仅供实现角色使用的 `implementation_context`。D90 的可选作者报告不自动注入规则保留；验收 Agent 主动查阅已经提交的公共报告时可正常读取，并须独立核实其主张。所有关联任务保留的上下文快照均提供历史原件来源，负责人和后继 session 不依赖旧 Run 存活。

Worker 的 `read_context` 与普通下载、owner API、完整 CLI 和管理工具共用该读面。原只读观察及旧文件接口保持兼容，新能力不修改 Assignment、journal 或旧有效请求。详见[实施计划](plans/agent-issue-context.md)。

## D192：已完成部署的历史结清

新的合入、部署和执行继续验证当前候选、授权、观察及安装基线。结清已经发生的
部署则验证原不可变执行合同、原批准来源、精确绑定的升级和安装回执，以及原
Task 的真实完成和验收；后续独立维护推进 target 或基线，不撤销这些历史事实。
观察器只保存真实 provider / 安装事实并合并通知负责人；它不自动改写 duty 版本、状态或原问题。负责人判断事实变化对当前任务及既有授权的意义，具体新操作仍检查真实身份和参数。已因旧观察逻辑转入 preparing 的保留责任也使用相同历史判定，不修写旧版本或批准。

历史结清只记录证据并完成原责任，不发起部署或合入，不让旧授权重新变成有效
执行许可。当前 Issue CAS、责任代次、原执行绑定、真实未完成或未知业务副作用
继续校验；不同执行或批次不能借用历史安装。新鲜执行有效性与派生历史结清事实
分别表达，托管工具与正式事务共用同一业务判断，不新增独立恢复许可或状态机。

负责人可以在既有用户目标内修正技术方法、安排真实剩余工作的正式后继和合理
预算。保留已消耗尝试、已完成报告及真实用户限制；Agent 自设一次性计划不自动
变成人类永久禁止继续的边界。失败工具回执保留真实请求身份和状态，纯读对象
不存在按 not-found 表达，不扩大为身份失效或未知写入。详见[实施计划](plans/historical-delivery-settlement.md)。

## D193：Issue 负责人自主推进

Issue 是持续目标、授权、知识及责任的归属。已配置 Agent 默认使用其普通工具与
同项目知识，自行选择短调查、协作、任务划分、审阅和连续交付的方法。明确的用户
范围、人工审批模式、成本限制、暂停和取消继续按原来源生效。

普通来源、provider、部署和阶段事实通过现有合并通知送达当前负责人；程序不从
摘要或默认额度自动推导语义变化、暂停责任或再次向人类请求许可。具体操作仍记录
其实际参数、身份、并发版本和结果，未知写只限制实际冲突操作。快照和历史报告
记录当时发生的事实，负责人可追加补正，从 Issue 重建会话并进行有限恢复。

普通报告的独立验收和审阅续办由负责人在真实目标、质量要求及预算内安排。终态
业务与回复、回执收尾分别处理，关闭后的安全记录不重新打开业务。新能力同步
CLI、工具及必要投影。详见[统一实施计划](plans/issue-owner-autonomy.md)。

普通工具模式只用于本次实际协商的 runner Issue 负责人：新 runner 广告可选能力，
server 在领取时记录原 Run 的模式标记，runner 同时核验标记和显式线程模式。
旧 server、旧 runner、缺标记的旧 Run 及回执专用恢复保留原默认，不把新能力广告
当作旧执行已经应用的事实。模型、思考强度及设备原生登录/profile 沿原配置。

同一非空项目资料默认互通，空项目不形成全局共享；跨项目显式分享仍按原件及
目标判断。默认读取加载当前及实际关联上下文，同项目 Issue 通过分页发现后可
显式选择；精确原件按真实来源定位，不先加载项目全部历史。明确撤销分享不删除
目标已冻结保存的历史输入。审阅补正读取真实原 reviewer 的完成报告并追加原件，
原终态 Task、旧报告、结论及轮次保留。

同一真实执行 Task 可呈交已登记候选并绑定后续交付，具体合入或部署仍要求已有
真实用户授权和实际质量事实。服务端追加迁移 0102 保存候选型交付来源，历史
验收型和结果型来源逐字迁移；未修改 runner journal 或线上协议版本。

Runner 为新建 app-server 保存本地进程代次及已观测后代，重启后在有限时间内
观察与停止原执行，不重放不确定的模型回合。无法证明关闭的旧工作保留 unknown，
沿既有隔离和外部强行推进路径释放容量；正常关闭事实与外部业务结果分别记录。

同 Issue 协作分别记录原责任出处与实际执行身份；明确责任交接后，旧代次执行
不能继续推动当前任务。普通定义修改与同负责人新 session 不等于交接。普通工具
配置不取消 runner 对原生负责人进程的退出约束，系统私有摘要保持原只读用途。
最后成功的 provider 快照与最近读取尝试分开判定，后来的 timeout 不清除已知冲突。

## D194：增量上下文与交付证据复用

Issue owner、普通 Task 材料读取、owner API 和 CLI 复用同源上下文投影。新增可选
增量模式；缺省保留旧来源分页。首次读取当前工作视图及有效事实、来源索引，后续
读取固定事件区间的变化，不在每页重复全部事实或报告正文。原件及冻结历史输入
仍可按需读取，已有明确完整阅读要求保留。工具历史暴露的数值字符串 `after`
作为 `after_source` 的兼容别名实际生效，冲突位置明确报错，不静默停留在首页。

增量游标绑定 Issue 和有效读取范围，只表达读取位置，不转移权限或形成跨 runner
调度状态。分页固定事件截止；对象值为读取时的当前版本，可以已经包含截止之后
的修改，这些修改仍进入下一事件区间。该投影不冒充全对象的历史时点快照。旧消息
修改、撤回、解除关联、事实替代及项目和全局约束变化均按实际变化处理；读取范围
变化要求重新快照，不能返回已撤回正文或以空结果冒充同步完成。无变化可立即结束，
相同页内容身份可辨识。正常会话续接原 Session，新会话先恢复当前目标、约束、
未决问题、unknown 和证据引用，不默认遍历全部旧对话，不增加逐轮模型摘要。

共享 PR review 延续 D186 的唯一正式 reviewer 与真实结论。只读证据投影从关联贡献
Task 的不可变 artifact、报告和文件提供候选、已有检查记录及原件引用；未提供的
字段如实保留缺失，不形成新的强制提交协议或审阅准入门槛。report 型 reviewer 与
验收型 reviewer 均可沿普通材料入口发现对应证据。reviewer 自主核对候选及覆盖，
选择复用、定向复现或完整重跑，并在报告说明重跑原因；独立性不默认要求同候选
重复全检。普通中间报告由 owner 判断是否自查，已冻结的明确验收要求不被改写。

最终候选仍须满足仓库完整检查、独立审阅、CI 及保护规则。维护修复集中组合验证、
线性合入及受控部署，分别记录源版本、发布与实际安装事实；后续维护不抹除原交付
的历史结清证据。请求、缓存输入、未缓存输入、输出和检查耗时分别观察，不新增
成本审批或自动阻塞。周期巡检由系统守时并执行有界单次检查，不以模型反复 sleep
代替计时器。详见[统一计划](plans/issue-delivery-efficiency.md)。

## D200：普通升级的 updater 自身交接

格式 2 发布集显式要求先交接每个 updater，再更新对应服务；格式 1 和历史
service manifest 保持原有语义。新目标与运行证明采用显式能力协商，未知 peer
与首次 Hello 不发送新观测字段，保持 N/N−1。普通自身交接使用独立 sidecar
journal 与 installer，不复用故障恢复授权或维护历史；固定原请求、发布集、
配置、updater/child、原 state 与 flock 身份。stop/start 意图先持久，再派发
一次；未知只核对原 attempt。旧 updater 真实退出后才交接锁，新 updater 保留
原服务并验证收养后出具回执。共享文件安装串行，每实例单独核验当前进程。
部署仍需单独授权。见[实施计划](plans/complete-release-updater.md)。

## D198：完整发布集与普通 updater 交接

完整发布集绑定相同源码的 server、runner、updater、CLI 和实际 Web，原格式 1
service manifest 继续只描述 server/runner。main 的完整 CI 通过才公开不可变发布。
普通升级的 updater 交接作为后续批次按实施计划接入；发布集本身不授予安装权限。
见[实施计划](plans/complete-release-updater.md)。

## D199：剩余调度开销的事实驱动

后台会话资格跟随提交后的相关事实与实际截止重新评估；没有变化不重复完整检查，
执行前仍新鲜校验。历史事件不因 Issue 关闭而丢弃，未知与原请求继续保留。
性能修复以固定源码和负载的调用链及完整 API 测量为据，不引入用量响应缓存。
后台附件释放容量后单独唤醒等待方，失败按会话退避。并发控制只有活跃/未知
负载或等待需求时每三十秒采样；空组只等待提交后的负载变化、冷却和证据保留截止，
恢复时仍使缺失区间的饱和证明失效。负载标记只在事务提交成功后唤醒。
用量分组器在单次查询内索引 attribution 和 CLI/model/version，保持原完整 JSON
身份与输出，不重复序列化已有执行。证据见[调查记录](findings/remaining-idle-cpu.md)。
见[实施计划](plans/remaining-idle-cpu.md)。

## D195：空闲采集与进程监督的工作边界

原生用量采集跟随实际执行与收尾：注册、开始、恢复和完成唤醒对应采集器；
活跃执行保留两秒增量读取，turn 完成后补一次延后收尾，稳定空闲不再定时访问历史。
保留空闲 app-server 或 thread 不代表活跃执行。只有真实关闭成功或已有精确停止证明
才标记 writer stopped；停止且追平的历史退出数据库候选与采集调度。
主会话负责未停止的本地来源，中央采集器负责其停止后的最终分页收尾；runner 独立
保留本地排空与原 Envelope 上传回执。来源失败分别退避至三十秒，未知仍是未知。
已结清历史只在重新执行、恢复未决工作或显式 reconcile 时重新进入相应读取流程。

发现索引在同一 server 进程共享；Linux 文件头缓存以 inode、大小、mtime、ctime
失效，游标边界变化触发重新验证，其他平台保留完整读取。最终收尾完整验证来源。
无变化不写游标，旧生命周期的收尾不能在停止边界之后提交事实。

updater 一秒状态机节奏保持。完整产物身份在启动、接管、切换、恢复与精确信号
边界验证；日常监督只观察 PID/start/UID/launch 与可执行文件变化信息。运行期
验证缓存不持久化，身份变化重新完整验证，读取失败不作为退出证明。pidfd 信号
只发一次，之后轻量观察退出，授权、健康期限和恢复语义不变。

用量投影有效更新及采集覆盖状态变化与每事务一次的 token_usage.changed 事件原子提交；重复事实和
无变化游标不发事件。Web 共享相同查询，按事件、重连、可见性和筛选变化刷新；
隐藏时取消读取及失败重试，只有可见的当前动态时间窗口按分钟更新。用量通知不推进会话摘要输入版本。现有预聚合、
owner API、CLI 与 runner wire 保持；不新增历史巡检或服务端响应缓存。
见[实施计划](plans/idle-cpu-lifecycle.md)。

D206 补充上述失败重试边界：三十秒是失败退避上限，不代表无限重试。
具体状态、失败预算、退役证据和恢复语义见下文及[设计计划](plans/usage-source-retirement.md)。

## D203：已提交执行结果与关闭事实分别持久化

`submit_result` 保存的报告和上传文件引用是原执行已经明确提交的结果。执行器
正常结束后，runner 先持久化该结果，再对账原生执行的关闭；关闭暂时无法确认
不能让已确定结果退回未完成执行或被通用中断恢复丢弃。

实现执行提供与独立验收相同原则的只恢复结果路径：按原 Run、执行目录和
thread 读取并验证原结果，在原执行真实关闭后沿既有完成链提交。恢复不启动
模型、不重放提交或上传、不增加执行代次，也不绕过当前控制和服务端的结果
校验。缺少结果或不支持独立进程恢复时仍沿原恢复路径处理；损坏结果和关闭
未知保留原件及错误。

原生回合终态与关闭证明分开持久化，并绑定原 Run、thread 和执行次数。事件
上传后被 ACK 清理不删除该终态；关闭错误不能覆盖已观察到的完成或失败。
恢复结果只有在原生成功已确认时才能进入正常成功链，原生失败沿原失败链
收尾，缺少终态继续保留未知。旧执行可从仍保存的原始终态事件恢复该事实。
runner 停机不覆盖已观察到的原生终态；生命周期取消、暂停、停止与提问交接仍
执行原关闭和控制处理。可选结果恢复不拦截显式授权的保留会话继续；原生中断
沿原恢复路径处理，是否能恢复执行由真实关闭证明与当前控制决定。归属缺证
保持既有有限对账和 fencing，
不通过活动执行器无限重试或自动重放原工作。

关闭时观察到独立子进程或进程观察失败，不能据此记录原 Close 成功。恢复另行
对账原 leader、原进程组、已登记独立进程及其原始 PGID；独立进程不必是组长，
必须在原观察时保存其真实组号，PID 被回收后仍检查该组。观察型对账以 kernel
进程组查找确认原组均不存在；单次 `/proc` 列表可能漏掉 fork/exit 间的新后代，
不能据此认定组为空。不存在身份冲突或读取错误时，以 `known_owned_process_set` 明确记录此次
对账范围，保留原 Close 未成功的事实。记录中的 PID 退出不足以忽略该组仍存活
的晚生后代，也不要求反证任意从未观察的历史外部执行。实际活跃或观察失败仍
保留隔离与未确认；进程集合退出不能代替原生业务终态。旧本地记录可沿原身份
补写这一独立观察，旧程序不识别该可选范围时仍保留原关闭未确认语义，不改变
线上协议、存储迁移或业务请求身份。旧记录缺少独立进程 PGID，或原组仍含未回收
的僵尸、不能直接证明组已不存在时，不猜测组号或将其称为活跃执行；
本机 CLI 可先将原 Run/thread、原记录 hash、runner 代次及其 kernel cgroup 绑定
为受控关闭意图，再于停止该执行范围后、新执行启动前核验原边界为空或已移除。
独立保存 `external_execution_boundary` 来源的关闭证明，供原结果续收；原 Close
历史事实保持。统一层只提供根路径的混合 cgroup 布局，可绑定实际 mountinfo 中
name=systemd 的 v1 原 unit 层级；v1 只接受原 kernel 目录移除的证明，不以
tasks/procs 目录快照推断递归为空。原路径缺失时须有界核对原 inode 已不在
所绑定的 cgroup filesystem 中，目录或祖先改名不能被当作对象移除。v2 仍使用
递归 populated 观察或原目录移除。
实施见
`docs/plans/completed-result-recovery.md` 与 `docs/plans/owned-close-reconciliation.md`。

原生完成事件在结束及 worker_done 重入时，经既有 ACK 路径作一次有界上传，
不以物理关闭成功为前提。上传失败保留原队列；事件可见不表示成果已提交，
业务结果仍经原终态、控制与关闭对账条件进入正常完成链。

## D201：独立程序检测与单会话自维护

自维护由独立 `tend-maintainer` 运行，读取 owner API、事件与本机受管进程证据，不依赖业务调度器或直接读写业务数据库。事件唤醒与五分钟程序补查优先，系统定时器每小时提交一次有界 Agent 审计；正常等待不消耗模型请求。采集错误、诊断候选、业务阻塞分别表达，漏报检测本身属于可修复范围。

独立状态库保存全局唯一活跃原生会话、待办、输入身份、配置修订与请求收据。新问题和审计并入该会话，断线或重启先读取原生事实，不重发结果未知的输入。共享 app-server 的控制 socket 使用 WebSocket；`app-server proxy` 只透传字节，不把它误当 JSON 行协议。退出只关闭自己的连接，不停止共享 daemon。原生 App/TUI 是观察与交互入口，不创建虚构的业务 Run 归集费用。

后端仅由外部配置采用：`when-idle` 等原生执行安全终止，`interrupt` 中止并确认终态后切换，不要求 Tend 全局维护模式。只有中止交接向后继传递前一个 Agent 和 session ID 或 rollout 路径；Agent 使用普通工具自行读取，不注入历史读取工具。维护器自身更新沿 updater 的受控进程协议，保存状态后断开并在重启时对账；发布与生产启用分别验收。

首条输入前只读取原生元数据，已解析 sandbox 随会话持久化；未知输入必须查询历史，不把未加载或读取失败当作结束。daemon 重启后的 resume 显式采用原会话配置并保留解析后的权限。更新明确拒绝与结果未知分别保留为可重新申请的拒绝计划和仅可对账的提交；原更新被正常监督重启替换后，按原收据、提交证明及当前进程确认完成。review 后的修复和交付待办跨后端采用保留。

updater RPC 在维护器锁外执行，响应后重新核对同一计划与阶段，允许固定授权和健康回调获取锁。保留的回滚证明也按当前实际进程核对。独立 audit 只推进已有修订的有限状态转换，无进展或已提交到执行中的检查即结束，不等待模型；失效的原生创建连接仅被关闭并重连，不替换原会话身份。

未知创建始终先对账，配置变更不证明创建结束。失败及非请求中断保留原 turn/input、真实 outcome 和未完成触发索引，检测不自行重试模型；新 audit、resume 或显式配置才允许下一次尝试，不自动添加前会话索引。持久化故障由只读状态直接呈现，并记录日志后失败退出交给进程监督恢复，不用另一条可能失败的写入表示故障。

维护器的 updater 自身交接复用 D200 的普通独立 installer；固定角色准入包含 `maintainer`，保留实际维护进程、原监督锁和原请求收据。普通服务准入按固定角色读取发布清单，独立组件不误作完整业务发布集。先交接 updater，再经原组件计划更新维护器，不借用业务全局维护或 force 授权。

### D196：索引交接与按需上下文

Main、Issue 管理及咨询的 handoff 不启动模型 turn，不携带摘要或账本正文；每次业务 turn 仅注入身份、版本、审阅状态及只读查询入口，当前消息独立准入。索引通知水位不表示事实或来源已读，语义审阅仍通过原有版本与提交规则确认。context-index 提供 state/sources/facts/threads 分页，保持各角色原权限；缺少原生历史保留为缺失。手动 compact 独立保留，其完成后重新注入索引。旧未知原生调用先对账，已注入交接按原记录恢复；prepared/created 旧包改为索引。无关全局账本事件不触发重复交接。历史摘要保留审计，摘要准备与设置写入退役。详见 docs/plans/indexed-session-context.md。

D196 后续范围确认：不保留 D111 checker 的运行兼容，删除其角色提示、auto-mode 配置、专属工具及 assessment 状态机。非主会话只启动已登记的 Issue 负责人或咨询角色，不能因配置缺失回退为 checker。历史会话、迁移和审计只作为记录保留；共用的批次、事件唤醒、材料读取和当前角色的未知请求对账继续使用原身份。

D199 补充：并发预算有负载时按三十秒采样，周期内的唤醒合并，不因重复通知加速。正常采样间隔最多两个周期可积分，超过一分钟缺失则清空连续观测证明；拥塞反馈和显式配置仍在原事务内应用。后台扫描起点统一界定候选与未来截止，取消的 recovery 若仍有原 native 回执也保留相应截止。

D199 补充：当前并发窗口在查询副本中裁剪已观测 interval 与 success；空闲后展示不保留过期数值，不制造新的采样或写入事实。

D199 补充：调度器使用独立的进程内提交广播；普通 assistant 文本与用量投影只刷新通用读取端，不重新执行后台资格或空闲并发采样。若回复事务实际产生 followup，仍唤醒调度。执行、配置、租约、回执和具体截止沿原事实入口唤醒，不改业务请求或持久化事件。

D199 补充：原生文本增量只有在关闭收尾需要消费时才唤醒调度；原生 turn、结果与停止通知保持。候选读取失败后忽略已过期的本地附件重试时间，保持二至三十秒的错误退避。

D200 补充：业务批次不能在原监督器交接未确认时终结为 cancelled；server 及已发给 runner 的目标都先对账原回执。批次暂停或期限到达仍保存迟到证明，不续期、不重新派发 installer。恢复未替换的服务时保留未决监督器目标及控制身份，完成后再释放。

D200 补充：已验证 updater 的 normal 确认间隔仍按原逐台规则等待，不将观测目标视为新的安装。retry 保留已发出的监督器目标及当前阶段，固定原 request 与期限。

D200 补充：准备阶段在任何 installer 派发之前确定失败，可写独立 immutable rejection 回执；按原请求查询恢复，不冒充成功安装或进程停止。获准但未知的派发仍待对账，持久拒绝才允许取消并另开批次。

D200 补充：Web 与 CLI 分别展示监督器交接证明和服务升级阶段，区分缺证、拒绝与成功；历史观测不冒充当前实时进程。展示复用原 owner 事件流和查询，无新增刷新循环。

D200 补充：runner 在完整包下载校验后通过原 heartbeat 报告 updater_ready，app 以新控制 revision 明确派发准入；没有准入不能调用自身更新。legacy 停止前以可恢复的 updater 状态写围栏和新鲜 service-operation 事实排除并发安装，交接 journal 独立子目录保持可写，业务数据不受该围栏影响。

D200 补充：监督器交接准入与所有服务写操作在同一 mutex 内互斥，HTTP 解码前的检查仅用于提前拒绝，不能替代写入边界。暂停阶段保存迟到监督器证明，不要求已产生服务候选；单主机配置维护的新增能力要求限定于该主机。

D200 补充：自身交接的锁证明按已验证进程的 fdinfo 核对原锁设备/inode 与 FLOCK 持有者，并在读取前后校验进程启动身份。无关锁增删时，全局 `/proc/locks` 枚举可能遗漏持续存在的锁；新交接不使用该全局枚举的缺项作为所有权判断。无正向证据仍为未知，最终排他边界继续由取得原 flock 保证。
D200 补充：线上无须兼容历史部署。新建普通升级统一要求 format 2 完整发布集，不提供 service-only 或 format 1 新升级入口。CLI 缺省在 server 本机选择官方最新已公开 main fleet 包，显式完整提交选择固定标签，本地目录亦执行同一格式与源码验证。官方资产的原始请求输入、固定标签、大小和 SHA-256 在下载前原子持久化；重启及并发重试只读取原选择，不能跟随 main 前进更换版本。下载不创建业务批次，不续期既有维护或安装授权；原 journal/账本及运行中的未知请求继续按原身份对账。独立维护组件及 runner N/N−1 通信保持原合同。见[实施计划](plans/complete-release-default.md)。

## D204：可读成果与最终沟通

用户主要阅读已验收的最终成果。报告通过原 report.body 和不可变文件交付，正文恢复最初的现象、目标与调查问题，再解释答案、影响、仍未知之处及建议。执行身份、哈希、详细验证和验收报告作为支持材料。负责人组织面向用户的结果；执行者与既有审阅者共同判断内容是否清楚，不增加格式硬门、固定字数或额外审阅阶段。

Issue 的业务关闭、报告责任登记与最终消息发布是不同事实。原有效责任会话在相同责任代次与现有执行身份内仍可完成本项最终通知、读取及受限异常记录；关闭不授予新的业务执行或其他责任的权限。正式发布保留原请求、正文、文件与消息身份，失败和不确定不自动重发。观察器分别核对结果及真实发布回执，不能把工具事件入账当作发送成功。

业务批次系统通知是实际的回合来源，可能没有外部通信输入行；回复先确认真实批次，再沿原准入与可见性检查使用既有 Web 发布路径，不伪造用户输入。普通输入缺失返回明确的未找到错误，内部异常在既有服务日志保留请求及执行身份而不记录正文。最终附件可引用同责任下已提交成果或验收的不可变原件，不能把尚未提交的上传当成交付。原有效负责人关闭后可记录非阻塞的行动或信息；不新增决策、关闭阻塞或执行权限。

界面以报告正文为主，提供 Markdown/文本原件阅读与下载，简短显示真实验收状态并按需展开证据。沿原 Issue 安排报告修订或重跑时保留已完成 Task、报告、验收及未知历史。协议字段和原文件接口继续复用，不新增报告账本或发布格式。见[实施计划](plans/readable-report-delivery.md)。

## D205：Worker 运行时临时目录

scratch 是下载原件、报告、实验副本和待提交证据的暂存位置。工具、构建和测试可以自行创建、使用并清理私有系统临时目录，按实际工具要求选择 `TMPDIR`；Unix socket 等要求短路径时使用短的真实目录，不要求其运行时字节仍落入 scratch，也不使用符号链接别名规避目录检查。这是普通执行环境适配，不新增人工审批、映射或全局权限机制。

共享 worker brief 和工作区指令对实现、验收保持相同语义。验收仍不能修改工作树或 Git 历史，生产权限及 sandbox 检查不变。需要保留的证据另存 scratch，并沿原有 `submit_result` 或 `submit_verdict` 文件提交取得不可变引用；scratch 自身不代表永久保存。历史运行及已提交原件不移动，运行时目录仅由其创建者清理。见[实施计划](plans/worker-temporary-directories.md)。

## D206：共享 PR 的当前 provider 事实

Issue 保存 PR 引用及自身需求、贡献和交付责任。候选、CI、合并状态等当前 provider 事实按 provider、repository、number 唯一保存；server 和仍受支持的 runner 查询结果经原请求身份准入后均更新这份事实。Issue 查询只保留 nonce、generation、截止、查询 baseline 和通知游标，不再复制 provider Snapshot。展示、审阅、授权和交付结算均读取同一份当前事实，无当前事实时保留未知，不回退到旧 Issue 副本。

共享审阅仍关联全部 Issue、贡献 Task 和原独立审阅轮次，逐项校验需求、贡献范围、正式执行要求及注册引用。当前候选只按唯一 PR 事实核对；错误、缺失身份、正在读取或尚未覆盖的显式刷新保留未知语义，真实 head/base 变化仍使原结论失效。审阅轮次、授权、执行及已接受结算保留各自冻结的候选证据，作为历史交付事实，不随当前 PR 变化改写。

事实 revision 在实际查询结果提交时递增，语义变化 revision 标识 Snapshot 或错误的变化；每 Issue 的通知游标只记录已消费的 revision，同毫秒的不同结果也不会漏通知。当前变化的前一状态和错误类型只在 PR 侧保存一份，供各批通知使用；有界分发不制造新事实，也不依赖下一次 provider 查询才通知其他责任。刷新中的判断来自仍未完成的实际请求，取消最后一项请求即可清除，保留历史计数。查询开始前的多个刷新可由同一结果结清；开始后新增的刷新继续等待新结果，其间被抑制的通知不推进消费游标，也不能阻止新查询领取。手动刷新被暂缓的通知保留独立的待送达责任，即使 Issue 已关闭也可结清，但不因此重新查询 provider。暂不可通知且无查询元数据可结清的成员不占分发批次，避免阻塞其他 PR。

查询 baseline 在领取时冻结，实际事实保留原查询 baseline，不被后续 Issue 领取覆盖。同一 PR 的新 runner 查询串行领取；已存在的并发旧查询保留原身份，合法结果仍可按原协议入账为历史，不覆盖较新结果。旧 server 交接回执的返回语义保留，私有历史处置位只区分回执到达后的响应方式，不代表回执已经到达。观察者切换保留原查询与迟到回执身份。

追加迁移将旧 Issue 观察原件保留于历史归档，并把活动观察转换为查询元数据；已有 PR 事实优先，缺少 PR 事实时归并已有 legacy 结果，保留错误和未决身份。线上字段和既有有效请求保持不变；这是存储格式迁移，不新增通信必填条件。见[实施计划](plans/shared-review-provider-observation.md)。
## D207：用量来源的有限重试与永久退役

原生端返回 rollout path 不代表文件已经创建；Tend 只登记身份与定位信息，
不因 thread 或 app-server 存在启动采集。执行发送意图必须先持久化，再调用原生端；
发送结果未知不能回退成未执行。既有来源缺少完整发送证据时保持 unknown。

采集调度独立于计账覆盖：休眠、执行、收尾、需对账和已退役分别持久化。真实 writer
停止后，已完整读取、可证明未执行、历史不可用分别以 complete、empty、unavailable
原因退役；未知停止只能进入需对账。empty 不制造零用量样本，unavailable 不改成
caught_up。按来源退避仍为二至三十秒，读取失败的无进展预算默认为五分钟；
超期退出自动读取，不以无限重试维持未知。成功分页继续排空，正常活跃无新增不是失败。

预算、来源代次和证明跨重启保留，重复唤醒不续期。退役不删除事实、游标或原请求，
不停止既有上传回执对账；没有到期读取或待确认上传时阻塞。新执行与显式恢复建立
新采集代次，旧收尾不能覆盖新代，来源归属与原样本去重键不变。

主会话、中央采集器和 runner 使用同一状态派生，保持原唯一所有者。runner 的采集
观测与处置通过新增协商能力传递，旧样本上传语义不变；CLI 完整覆盖查询、退役和
恢复，展示采集结束与用量完整性的区别。设计、协议边界和验收见
[实施计划](plans/usage-source-retirement.md)。实现与验收进度见实施计划；部署另行明确授权。

异常退出后的主会话交接须独立核对原进程身份，取得真实退出证明后才记录旧 writer
停止。显式补采在读取前冻结生命周期，每批事务原子返回自身提交的快照；并发停止
或换代不授权旧缓冲提前宣告读完。live runner 保留本机停止事实，独立补采 journal
可接纳来源 API 后来确认的停止事实，两者均不将文件定位当作新 writer 接入。

## D208：Manager 回执恢复保留原执行模式

已接受执行的原生工具模式参与已有线程 binding 的身份。进入关闭、park 或严格回执读取时，选择器保留原模式，由当前 `ReceiptOnly`、`RequireReceiptReader` 及业务围栏决定可执行的操作；不把临时停权写成另一个执行身份。恢复已冻结的执行时，只重绑原责任的传输 Run，不以 reader 的临时许可覆盖原快照的模式。

已有 binding 算法、线程与执行快照保持不变。新普通执行仍要求实际协商能力，runner assignment 和原生命令队列继续禁止 fenced manager 创建线程、注入或开始新 turn。原 close、事件、未知请求和业务回执只能按真实证据结算，不因成功附着就推断关闭。该修正不改变通信、存储或兼容版本。见[实施计划](plans/manager-receipt-binding.md)。

## D209：技术等待的负责人判断与已停止责任承接

原答案明确记录 Agent actor 且答复来自 Agent 时，当前已准入的负责协调者可依据
真实依赖状态显式解除技术等待，无需制造一条新 owner 消息。实际 owner 设置或
转交的等待、无法分类的旧记录仍要求合法 owner 恢复。恢复不重写答案，不授予
合入或部署权限；当前负责范围、版本和未知请求规则继续适用。

管理会话替换使用自己的新责任代次，原业务 Task/spec 与原运行历史保持。仅在
原命名 Agent 和完整执行 pin 仍符合当前负责人、原 handoff 已证明停止且没有
其他活动或未知执行时，可随正式 resume 显式提供当前 assignment epoch 与
理由，事务内追加该 Task/decision/原 Run 的承接事实。当前责任投影统一供任务
准入、执行身份保留和后继 claim 使用；后继写入当前 epoch，旧运行责任不改写，
只对精确已停止前驱识别本项承接，不使其他旧 live worker 重新获得权限。
已排队但未领取的原续行动作可在承接后继续，保留 action 身份；已有实际后继
Run 的动作不能据此重放。完整执行 pin 复用 Task 创建时的 worker 派生与 Task
类别扩展解析，管理会话的传输 runtime 不作为实际业务 worker runtime 比较。

不根据同名 Agent 自动承接，不重建 Task/spec/generation，不改写历史合同或
原生回执。原请求去重、两项 CAS、唯一后继以及真实 owner 等待均保留。新增
owner payload/view 字段为可选，旧请求语义不变；追加存储迁移，不改变现有
runner 通信或 journal 格式。见[实施计划](plans/technical-wait-continuation.md)。


## D210：停止后的 manager 关闭事实恢复

runner 已持久化成功的原 native close 后，即使 owner 后来停止该 manager，也必须
能在有界 runner lifetime 中续传原事实。attempt 的取消禁止新 native 工作，不应
取消原完成记录的读取和回执恢复。原事件顺序、close 游标、请求身份和错误继续保留。

真实物理退出与业务回执分别记录和结算；未应用业务事件或未知工具回执不代表
进程仍运行，也不能被物理停止改成成功。普通恢复和受控更新的事实重放均先传原
事件，再传真实 stopped，最后续传原 close reply，避免业务应用等待挡住资源释放。
每项 ACK 只消费自身；正式 Stop 的既有本地归档保留原未 ACK 的请求与字节。
没有成功 close 的既有 attempt 仍保留未知，不能因本次未打开 session 就确认先前
进程退出。stop 已使原 Run 进入终态时，迟到 close 按原身份、游标与事件校验入账，
保留原 Run/lease/action 终态，不再次完成或替换该运行；reader 先结清业务也不改变此规则。
retained 期间不自行结清，正式 Stop 后
只恢复原关闭事实，不重新附着或调用 provider。没有成功 close 的原执行继续原有
核验路径。不改变 wire/API/schema；见[实施计划](plans/manager-close-recovery.md)。

## D211：Manager 回执阶段的单次总预算

正常 manager 原生操作完成后，原 reply 的投递以既有 RequestTimeout 作为整个阶段
预算。该 deadline 在 retry 循环外建立，不因逐次 HTTP 重试或退避续期；更短的调用方
预算仍生效。native command 与模型执行本身在该阶段之外，不增加模型时间限制。

回执超时不改变原请求身份、payload、游标或 unknown，不授权重放。已有真实 close
与物理退出确认仍按原条件收尾，然后分别传原事件、stopped 与迟到原 reply；只读
传输按真实原历史结算业务。没有真实退出证明的原执行仍保持未知与 fencing。
不新增配置、协议、迁移或权限；见[实施计划](plans/manager-receipt-deadline.md)。

## D212：写连接超时的可归因诊断

写入诊断保留原 request/trace，并为内部 manager 事件处理和 applied ACK 标注固定
操作名、原 Run/原事件序号，另列当前 attachment Run/协调合成序号；两组身份在本地
transport 中传递（不进入 wire），没有原身份时明确未知，不将合成游标冒作原事件。
事件作用域独立于嵌套请求元数据，不由正文或当前游标推测。日志不保存业务 payload、SQL、环境、凭据或本地源码路径。

单个 Store 为诊断写入分配局部编号，记录开始、结束、原 context deadline、池状态
与有界函数调用链；编号和活跃写入记录仅供诊断，不参与业务身份、授权或调度。
连接获取失败时采样当刻已观察到的 writer、阶段和连接占有时长；它不是整个等待
窗口的阻塞者证明。连接等待、BEGIN、callback、commit、rollback 分列，连接占有
wall time 也不等同 CPU 时间或 SQLite 锁时间；累计池排队统计不是单次持有时间。

Claim 的 manager refresh、候选收集、选择准备使用固定 inclusive 阶段计时，作为
apply 的子集；attention 仍是 persist_outcome 子集，不能重复相加。没有工作时原有
NoEffect 回滚仍可能显示通用 error，诊断不改变其业务结果或请求恢复语义。

活跃诊断同步不包住数据库、业务 callback 或日志 callback。观察回调仍在事务和
连接释放后运行；只保留一条活跃记录，不新增后台查询或全量快写日志。reply draft 的 deferred/ticker/关闭持久化及
prefix ACK 在各自边界从保留事件重建源身份，attachment Run 也随本地 draft 保留，
不继承另一个触发事件的作用域。deferred Rollback 只将 live writer 标为
rollback_cleanup（显式清理尝试，可能是 no-op），终态失败阶段与原计时不变，
也不由该字段宣称仍持 SQLite 锁。不变更 wire/schema/超时/事件应用顺序，
见[实施计划](plans/write-timeout-diagnostics.md)。

## D213：自动只读回执 Run 的原线程恢复

补全 D208 在自动派生 generic receipt-only Run 上的身份继承：原已接受的
native_owner_tools 模式属于 native thread binding，和本次传输是否只读分别表达。
自动生成 reader action 只表达 receipt_only，不在普通 Close、Issue 刷新或全局
Claim 中预先读取历史 binding 证明。实际准备该 reader 的 selection/attachment
时，统一从同一管理责任可核对的原 action/冻结执行事实恢复原模式，不覆写历史，
不按当前配置或可用性猜测。
旧 action 未保存该位时，可使用原 Run 已接受的 hosted selection，保留其原 false
缺省语义，核对相同责任与完整执行 pin。缺失证据只在该 manager 的附着处保留
unknown，不将其提升为所有 runner 的 Claim 失败；真实数据库错误仍如实返回。

所有 receipt-only 启动均读取并恢复原线程；不得通过 StartThread、ForkThread 或
工具集迁移寻找替代会话。缺少可信原身份或原绑定时保留明确不确定性。严格
RequireReceiptReader 的原证明要求保持，generic reader 继续使用其原有事实边界，
不被追加严格 reader 专用的不可得前置条件。

读传输的恢复不解除原业务 fence，不创建或重放业务输入，不改变 Task/spec、
执行 pin、模型配置、原请求、事件和审阅历史。没有 wire、schema 或新配置变更；
见[实施计划](plans/receipt-reader-bootstrap.md)。

## D214：交付答复准入与历史原候选恢复

具体交付决策的新批准与结束答复为纯 `approve`/`end_delivery`，不能同时提供
`text`。明确 `defer` 仍可带说明并进入持久延期，不批准执行或结束责任。
通用答复、owner 来源中继及 Attention 共用此准入；批准/结束的审计解释放
`Envelope.reason`。纯文本保留真实答复但不授予执行权限，不能靠关键词判断
条件已满足。历史已接受的相同答复仍可幂等读取；原 AnswerJSON 和来源不改写。

显式 `prepare` 原候选复用尚未回答的问题，或来源仍有效的纯 `approve`、
`defer`、`end_delivery`。历史带文本或来源失效的答复不再永久占据幂等分支；
在原候选、epoch、执行/租约/工作区及未知升级等检查通过后，恢复原具体人工
确认，不能借后来 auto 策略变成自动授权。live 来源替换问题前重核原 Task
control/lifecycle；已完成历史来源不套 live 前提。新问题仍需真实答复，恢复
本身不制造批准。延期、已结束
责任及未结清执行保留原围栏，不重开任务或安装。没有 schema、runner wire
或新操作；见[实施计划](plans/delivery-answer-recovery.md)。


## D215：已完成交付中的历史贡献结算

复用 `issues.update_delivery action=complete` 的 `execution_task_id`，为尚处于
`preparing` 且没有自身候选和执行的已接受贡献，记录同 Issue 已完成执行已经
交付该贡献的事实。原贡献与执行必须在同一 PR 的 shared review links 中有
明确 Task 关联；同一 Issue、名称或 follow-up 链本身不足以证明贡献，也不将
follow-up 链设为并行贡献的必需条件。

引用执行必须经过自身实际完成/接受与原交付责任 complete，保持原执行账本和
冻结 candidate 一致。结算保留贡献自身 artifact/acceptance、引用 execution 及
该执行已保存的 provider/upgrade 证据；可选 upgrade_id 必须属于原执行。
不复制 authorization，不生成执行行、Task、批准或安装，不覆盖已有候选/执行。
历史事实不依赖当前 main、review 有效性或后续维护基线。

Issue revision、assignment epoch、原请求去重、单事务事件与 outbox 保持；
不能借结算接受尚未完成/验收、无关 PR、跨 Issue 或未结清执行。现有首次
complete 路径不变，无 schema、wire 或新操作；CLI 使用原完整输入和请求恢复。
见[实施计划](plans/contribution-delivery-settlement.md)。


## D216：SQLite 文件路径与 URI 身份一致

Journal 的主库、独立进程代次库及 Store 读写池以标准库编码 file URI：字面 `#`、`?`、`%`
属于路径，不成为 fragment、query 或预解码后的另一个文件。原 pragma 和事务
选项独立放在 RawQuery，相对路径仍相对当前工作目录，绝对路径仍指向原文件；
不能把相对路径的首段当 URI host。真实主库文件必须与 acquireWriter 持有的
设备/inode 相同，进程代次仍在原独立文件中单调递增。

Store 保持原 WAL/foreign_keys、busy_timeout/synchronous、writer immediate 与
reader query_only，不改变池或写事务边界。原版本/迁移检查、writer 锁、
请求去重和未知事实保持，不删除错误路径产生的
历史文件、不改测试名称规避，也不自动合并另一文件中的状态。无 wire、迁移、
配置或 owner 操作变化。见[实施计划](plans/journal-sqlite-uri-paths.md)。


## D219：持久上下文、Task 续行与可替换会话

所有角色的执行输入以业务账本为准：初始背景、当前需求与授权、负责人判断、
执行身份与真实环境、已产生的成果、审阅与检查、失败及未知请求、等待条件和
下一步都有可读取记录。大的报告、检查和 rollout 通过稳定原件索引提供，不递归
打包历史。新 session 接收完整业务上下文，复用 session 接收当前增量。
Session 复用仅为 token 优化；无法复用时正常创建新 session，不需要额外许可。

Task 表示持续的同一协作目标，Run 表示一次实际执行。明确同目标续行使用
`tasks.continue`，追加 generation、action 和 Run，保留历史 spec、Run、artifact
与 acceptance。续行不消耗失败重试次数，不改写旧完成事实。真正的目标或约束
变化才产生新 spec；独立目标才产生新 Task。未知操作与尚未停止的真实执行必须
先按原身份对账，不能通过续行重复执行。

同一个真实 Run 可以产生多次成果和交付结算。历史交付以 Run 和不可变责任版本
共同定位；读取、引用和贡献结算使用同一份历史事实，不能仅取该 Run 最近一次
结算而遮蔽早先已完成的结果。普通当前上下文仍能直接重建，不依赖旧 session
记住历史选择参数。

合法 Task 作用域中的实现背景对审阅者同样可读。独立审阅依靠独立执行身份和
真实证据判断，不依靠职责标签隐藏作者的调查输入；上下文区分原始材料、作者
假设和已验证结果，不能把实现者的陈述自动当作审阅结论。

## D220：正常来源处理与持续的 PR 审阅

Agent 能力由 category / capability 配置决定。主 Agent、Issue owner、实现、审阅、
交付只表示责任分工，不形成另一层能力白名单；提示词与装配代码不能仅凭角色
禁止其调查、实现或使用已有工具。真实调用身份、对象归属、授权作用域、租约、
资源占用和独立审阅事实继续按实际记录校验。历史 `purposes` 清单不再作为职责权限；同一 Agent 按已配置且可用的适配器提供执行与负责人入口，调度继续校验真实接入类型。

Issue owner 自主选择直接调查、实现、自测或同 Issue 协作。审阅直接关联原 Issue
和 PR；不能仅因审阅、修正或 rebase 创建子 Issue。一个 PR 持续使用同一审阅
Task，后续真实结果关联新的 Run/artifact 并保留各次候选。审阅者判断旧结论
对实际增量是否适用，复用实现者的原始检查证据；最终候选仍满足独立审阅、
检查、CI 与仓库保护规则。Issue 仅保存 PR 引用与自身需求、贡献和交付责任，
不保存另一份候选缓存。

独立来源核对、显式提交和相关收尾门槛退出正常执行路径。系统以投递版本和
实际终止回执记录处理位置，不将其伪造为语义理解、事实批准或用户授权。
负责人在普通进展/结果中记录实质判断，不生成只补手续的模型回合。
历史显式核对记录仍可审计；真实未知、人工停止、待回答问题和未结回执继续保留。

## D221：外部等待与真实资源释放

外部等待保存检查、审阅、答复、Task 或升级目标、恢复动作和截止时间。
runner 收到等待请求后停止当前原生执行，只有已确认物理停止并记录原回执后，
server 才释放租约、slot 和并发占用。仍运行的本地进程继续计入资源。
server 沿既有 sweep 观察条件并合并唤醒原 Task/Issue，不安排模型 sleep/poll，
也不将普通机器等待变成人工审批。无操作截止时间时默认每二十分钟产生一次
合并处置提醒，负责人判断下一步；到期不自动重放未知外部操作。

等待登记时记录的事件水位仅表示事件已存在，不证明负责人已处理这些输入。
真实停止后的唤醒同时检查当前会话及活动 Issue 关联内、仍待投递的 owner inbox，
包括登记等待之前已入队的消息；依据原 outbox、原生 turn/steer 准入及撤回事实，
不因已处理历史、其他 Issue 或普通 system 通知重复唤醒。尚未确认的原生输入
继续沿原回执对账，不把 unknown 当作未发送，也不重放外部操作。每份 wait 只
生成一份同 Task 的续行；上下文索引或事件水位不替代消息实际投递和消费证据。
main 已准入或消费原消息不代表关联 Issue 已处理；路由输入按该 Issue 的待投递
background batch/source 及其实际原生回执判断，不使用 main 的投递结算替代。
直接 inbox 仅检查当前 Issue 会话的投递目的地；main 原消息仍待投递也不能绕过
接收 Issue 自己载体的消费或 unknown 围栏。

协议 3 的 assignment 通过可选 `session_stop_reports` 明确协商 session 停止元数据。
它不是业务完成或物理停止的必要回执；单次有界上报失败时保留原 Envelope，
不阻塞正式成果和独立 Stop。真实 yield/停止证明仍按原执行身份确认后才释放资源。

## D222：连续范围授权、明确部署入口与最终交付

Issue 保存真实用户授权的来源和合入/部署范围；auto PR 策略的可选 deployment
限定 server instance 与完整 host 集合。未声明部署时仍只有合入权限。
范围内候选、维护和 rebase 由负责人协调适用审阅与检查；超范围才请求新许可。
原授权 scope digest 保留作审计锚点，不因普通文本变动要求维护者转录批准。

自执行的交付责任可在原 Task 内更新尚未产生外部交付效果的候选。Task 持续到
实际交付结束；当前合法 Run 活跃或一次等待不要求先终结 Task。prepare 原子保留
旧候选和授权的不可变历史、撤销旧当前执行绑定，再核对当前候选的审阅、检查和
有效授权；随后 bind 沿原 Task 继续。已经合入、已启动的升级或相关未知效果沿
原身份先对账，独立交付执行的实际停止与资源规则保持。能力由既有 category /
capability 决定，实际执行身份和有效租约不以名义角色白名单替代。

自绑定 Task 已真实完成时，候选替换仍沿原 terminal、实际停止与资源释放校验；
准备新候选不复活原 Task，也不修改已提交的成果和独立验收。仅持续中的责任
使用 live 刷新，不能按相同 Task 身份把已完成执行送入 live 绑定校验。

负责人沿当前真实 Run 登记阶段成果时，即使原 Task 已绑定部署执行，也不要求
尚未发生的升级引用。报告及合法上传原件作为不可变结果登记，沿已有 Issue
权限共享；不会完成 Task、替换当前候选或授权、清除已绑定批次或宣称部署。
显式提供 `upgrade:<id>` 时仍严格验证并绑定实际批次。独立部署 worker 的执行
报告及最终部署结算保留原有升级引用、目标范围、完成和安装证据要求。
实施与回归见 [负责人阶段成果登记计划](plans/owner-progress-artifacts.md)。

被替代的 executing 快照不变成 complete，也不授予历史结算或再次执行的权限。
已存在的产物引用真实 producing Run；无产物时记录实际快照 Run，尚无执行时
Run 与产物均保持缺省。原 Run+duty_revision 历史查询保留，同时支持
Task+duty_revision 精确读取，完整上下文不依赖伪造产物或旧 session 记忆。

部署投影显式提供配置的 transport、target、user、当前受控 CLI 以及 control socket，
连接事实与授权分别表达。最终报告可以从当前责任主动发布，旧输入不可回复时
不阻止通知；成功消息回执、正文和合法附件关联才构成交付证据。正式子成果
通过真实贡献/父子关系或显式分享读取，撤销分享及对象级权限继续有效。
报告先说明初始背景、目标、结论和实际交付状态，原始检查按引用展开。

新配置字段不能在旧版本仍可能重启时提前写入。先以原配置完成受控版本切换
并保留实际安装证明，再写入口元数据，在既有受控维护窗口加载并核对投影。
配置备份不替代与回退绑定的恢复机制；后续同版本启动仍保存自身真实进程证明。

## D223：持久原件与可恢复的正文归档

命令正文保存一份，入队、领取及请求回执保存引用和当时状态，读取恢复原历史
返回。rollout 原文以 server 持久可读的不可变文件保存，DB 保存稳定身份、窗口
offset/length、执行索引和必要状态；不能引用临时 runner 路径，不能为已有原件
片段再各造一份正文。

文件先持久化，业务引用在同一事务提交；缺文件或损坏返回真实错误。历史迁移
按有界批次恢复、核验原内容与读取结果后再清理重复字段，无原 rollout 的内容
保留独立原件。备份同时覆盖 DB 与文件。分页、游标、Unicode 窗口及历史重试
语义保持。分别衡量 DB、原件、备份和总占用；物理压缩在核验后受控执行。

## D224：可核实用量与有界候选构建

原生响应身份与 token/content 事实用于去重，归因元数据补全不构成不可变正文
冲突；真实计数或内容冲突继续明确显示。生命周期结束、EOF 和已持久化且归属
完整分别记录，未观测不能声称完整。恢复可重扫与断点补采，保留原上传身份，
重复补采不能重复计费，也不重跑业务任务。

原生来源清单按真实 Run/thread（托管来源按 conversation/thread）归一多种持久证据，
沿原协调执行和持久负责人 Task 绑定补齐负责人会话身份，包含已经完成或取消的
历史责任；实际 Run 的 host 仍决定来源边界。空元数据可以补全；真实非空归属、provider、
purpose 或 parent 冲突明确拒绝，不放宽 journal 的原归属与 CAS 校验。
前台核验的失败保留实际阶段、source/run/thread、原请求和安全错误码，
不将本地 journal 拒绝折叠成无上下文的 HTTP 状态，也不返回内部 SQL、原件或凭据。

候选构建分别观测查询、读取、解码和组装，保留 writer 排队、事务及活跃写诊断。
读侧预筛和批量读取减少 writer 工作量；正式领取在事务内重读控制版本、租约、
并发及可调度事实并原子提交。普通日志/ACK 不触发全 runner 的完整调度重算。

完整检查和独立 race group 共用有效 CPU、包并发及实际并行子进程的预算规则。
已经由父检查下发的预算不被子入口覆盖；独立入口补足缺省预算并输出实际值。
资源调度不修改测试截止时间、动态测试根覆盖或缓存语义。没有 CPU 观测的旧
超时继续保留未知；writer 排队时间不能直接解释为 CPU 时间。
使用相同快照和负载对照候选构建、writer 持有/排队和 API 延迟；尚未确诊的历史
长停顿保留 unknown，不能以增大超时宣称修复。

历史正文按规范 hash 匹配时，查询显式保留既有部分索引的非空条件；函数提前
排除空 hash，因此不改变匹配集合。以实际驱动的查询计划验证索引使用，不能
仅凭索引存在推定查找有界。原件读取、精确重建和未知请求对账语义保持。

## D225：业务幂等、单份原件与有界存储

本决策修订 D11/D28 的通用请求存储和 D223 的请求回执副本做法。业务记录是 SoT；
业务对象和外部动作使用自身稳定唯一 key、唯一约束、状态机与控制 revision 完成幂等。
创建对象由调用方提供业务 ID；同目标的重复创建读取真实对象，同键不同意图明确冲突。
普通变更重试读取当前业务状态，不承诺返回第一次调用的逐字节响应。
同一外部原生命令的 runner 结果上送确认与 coordinator 消费确认分别记录；两方均已
确认后才退出该未结队列，正文已转为实际 Run/工具事实时只保留紧凑身份和未结状态。
传输 request_id
可以用于关联和轮转诊断，不为它保存请求档案、参数摘要、请求到对象的映射或返回缓存，
也不把这些内容迁移成文件档案。原生事件、用量和消息分别用自身业务身份去重。

外部部署、真实执行、待投递消息和其他未结副作用保留动作身份、输入版本或引用、
授权、状态和 unknown。旧请求表中的必要事实先迁入相应业务记录后再退出旧表；
不能丢弃唯一授权、将未知当未执行，或因没有通用回执自动重放。角色不构成新权限层；
来源和授权校验直接读取真实身份、业务对象及其版本，不能从最近一条请求猜测 owner。

rollout 和工具、日志正文通过 server 可持久读取的不可变原件保存一次，业务 DB
仅保存必要索引、窗口和状态。内部紧凑 key 和二进制 hash 减少重复长文本及二级索引
放大；对外稳定 ID 和完整原文不变。普通展示 JSON 从 SoT 组装，确需精确恢复的
共享规则保存一次，不在每个引用处嵌入，也不为已有原件片段再生成正文文件。
server 派生事件按必要结构与原文引用组装语义 JSON，不为外壳键序、空白或
无消费用途的空字段保留整段副本；真实原文、字段语义和文本窗口仍须保真。
归档来源/Run/主机关联如果只有 DB 具有，必须保留最小关联或可核验 manifest。
缺失或损坏返回真实错误，不能降级为空正文或“迁移成功”。
历史子窗口索引的需求来自真实消费者展开后的正文；已有引用的外层 hash 或 ID
不能替代其中的实际子字段需求。阶段内去重解析引用，重启可重建需求集合，并沿
固定原窗口高水位推进；不为无消费者的全部子字段创建派生索引。

纯刷新通知不属于永久业务历史。按通知种类和对象范围覆盖当前版本/水位，合并
未完成通知；消费确认只能覆盖已观察的版本，不能清除并发更新。旧游标越过保留
范围时明确要求从 SoT 重取，不能默默跳过真实业务变化。已有 `(run,seq)` 等唯一
业务键的记录直接原子去重，不再增加同键 inbox 档案。真实业务变更、待投递状态和
必要消费位置仍与状态变化同事务提交。

用量只保留紧凑原生计数、真实归属及覆盖状态，以及有实际筛选/分页价值的增量
投影。原始样本 JSON、展示详情和多层维度不能反复保存。重扫按原生响应身份去重，
不能重复计费或通过重跑业务补用量。OpenRouter 目录仅保存最新有效价格和获取时间；
成功刷新覆盖旧目录，失败保留最后有效值并显示时间。金额一律是基于最新目录的
估算，历史 token 计数不变，不保留历史价格目录、逐次估价或估价重算历史。

迁移先停止新冗余写入，按有界批次核验原件与业务读取，再移除旧字段。备份同时
覆盖 DB、配置、文件 manifest 和被引用的不可变内容；活动业务、保留备份、未结动作
与暂存写入的引用并集决定回收范围。unknown 和恢复锚点不因年龄或容量预算自动
删除。先 preview 并固定实际清单，验证恢复后才 prune；持久文件不能引用临时路径。
runner journal 保留离线可恢复的动作/租约/消费事实，大原文通过持久 spool 保存，
server 持久确认且本地/备份不再引用后才能回收传输副本。

空间和性能按相同历史集分别报告主 DB、WAL/SHM、runner DB、唯一文件、备份与
总范围；内部页空隙和重复字段不能重复计收益。物理压缩在迁移与恢复核验后有界执行，
未知压缩或启动先对账，不通过反复重放越过失败。45–80 MB 是当前历史集的工程预算，
不是最低必要空间或已实现缩减；实际结果与未知必须如实报告。
总空间下降不是验收前提；真正独有且仍有用途的内容允许迁入文件。DB、文件、备份和
演练副本均须避免重复保存或保留无价值内容，不能仅以 DB 文件缩小判定完成。
追加中的 pack 备份复用已核验前缀的对象片段，只保存新增尾部；manifest 保持扁平，
恢复时流式核验全部片段和完整文件，保护及回收按实际片段引用计算。

新增 claim 的实际 Run 身份与原生命令确认改变通信语义，runner 协议提升至 4，
保留 3 的集中适配并拒绝 2；旧已领取 claim 先从原 run.assigned 业务事实恢复原 Run，
不以新派生身份覆盖它。协议 3 库存查询只返回实际当前目录，未保留的传输回执明确
表示未记录；同一进程可按不可变快照业务键重试内部登记，先返回已有快照，再判断
新登记的控制版本。协议 4 直接按快照身份查询，不依赖请求映射。迁移前核对实际节点
与未结执行。

实施、迁移和退出条件见 [存储治理计划](plans/storage-footprint-governance.md)。

## D226：事件传输不依赖原件归档

日志读取边界约束的是读取原文件，不构成 Agent 业务工具或已授权执行的前置许可。
配置目录的别名与原生返回的真实目录应识别为同一来源，实际打开文件仍经目录根
约束；原生 session 身份、对象归属和目录外符号链接限制保持。

原生事件先持久记录其顺序与身份，业务工具回调、完成事件和执行回执不等待 rollout
归档。归档由 runner 生命周期内的独立有界观察处理，合并同一 Run 的新增水位；
实际完成及重启后仍可继续补齐。失败保留原上传身份、字节范围及原错误，不能
静默当作已完整，也不能触发业务 turn 重放。归档与用量各自沿其真实持久化水位。

晚到原件复用既有正文及展示索引；必要暂存的事件证据不能因归档不可用丢失。
新原件已可验证时可将相同 compact value 重新绑定至其窗口，不改变业务身份、
正文、分页或历史结论。已有共享规则和备份依赖不能因新位置可用被破坏；不在
执行通道故障恢复中重写或删除混合原件包。

事件持久化或转发失败必须反馈主管循环，记录实际 Run/thread、阶段及底层错误。
不能只返回通用工具错误、退出事件消费者并留下仍宣称运行的空主管。故障恢复沿
原事件序号和未知操作对账；资源只按真实进程停止及回执事实释放。

实施与回归见 [事件传输恢复计划](plans/coordinator-event-recovery.md)。

归档有界调度分页在整页实际遍历后才回绕，同一 Run 的不同原件独立尝试并汇总真实
错误，避免忙碌前缀或缺失原件阻塞后续原件。journal 27 为逐事件脏标记增加 Run
前缀索引，不重写 journal 26 迁移，不改变通信协议或业务完成语义。

未登记原生日志来源的执行不产生归档依赖；已登记来源的原件缺失或不可读则保留真实
错误与待补齐水位。两者不能合并为一个缺失错误，也不能把后者当作已归档。

回执对账是原生历史读取，不是以只读权限恢复一次业务线程。读取不得改写该线程
的权限、工具、开发者指令或模型；历史句柄不具备模型和执行能力。正常责任之后
仍沿原配置继续，不继承对账临时约束。未观察到的原生配置保持未报告，不把已知
执行 pin 或读取器的限制冒充当前原生权限。

读取附件观察自身原生进程退出；准备取消后退出附件并重走正常启动，完成前不接收
新执行。事件转发器的状态独立于历史句柄是否存在，转回可执行句柄时启动转发；
普通执行仍按事件流收尾，不能用提前观察进程退出丢弃已有完成事件。

原生回执中的空回复可以是合法执行事实，不自动成为用户可见消息。历史对账与实时
消息流保持一致：未发布的空回复不创建消息，已有消息的权威空终态仍可更新；完整
原生回执继续保留，turn 按真实终态结算，不以是否有回复正文判断执行完成，也不将
原生完成冒充业务结果交付。实施见 [空回复对账计划](plans/empty-native-reply-reconciliation.md)。

## D227：停止交接与持续事实的恢复

本决策补齐 D219、D221、D222、D226 的共同交接边界。Run/lease 保存一次真实执行与
资源占用，原 request/thread/turn 保存已发生操作，Issue/Task 保存持续目标；三者不能
互相代替。外部等待、暂停或关闭在确证物理停止后可以归还槽位，同时在同一事务保留
原执行的未结回执。历史记录的读取不能依赖旧会话恰好完成清理，也不复活旧执行权限。

事件消费者先交付已持久的尾部，再按原载体终态退出。退出连接不宣称业务完成；原事件
序号和 ACK 保留，最后事件与结束同时发生时不能遗漏。后继沿原责任的真实 Task、Issue、
epoch 和原件身份接手有限历史读取；暂停新业务不阻断这种读取，也不要求先有可用模型。
正常执行的模型与权限校验继续独立生效，历史句柄不执行 resume、输入或工具调用。

普通续行、机器等待、暂停恢复与升级 park 使用相同的未结责任范围。没有载体时可沿
现有只读 reader 调度创建真实通信执行；不能为读取新建业务 Task、恢复旧租约或清除未知。
升级检查所承担的历史责任，不仅检查当前 Run；models/find/usage 等纯观测结果未知不
等于业务效果未知。停止前 drain 检查真实动作、启动/清理与资源；后继健康就绪另行核验，
不要求已空闲的旧模型先恢复才能安装修复。

适配器观察到的原生终态与用户消息、来源消费和最终交付分别推进。输出投影失败保留
原始回执及可恢复位置，不把观察过的事实退回未发生，也不伪称用户已收到。投影重试
从同一份原件经适配器纯解码恢复，不为低频重建保存第二份完整消息正文，也不要求再连
provider、启动模型或复活旧执行载体。来源消费尚未被完整历史覆盖时，只在既有事件
保存小的原生终态观察并继续同身份读取；完整原件须在来源证据充分后才冻结。维护中
后来领取的只读载体也在原批次绑定实际 Run、lease 与唯一 close 请求，消费完成不能
替代其物理退出。同 Task 原件复用核对实际 producer，未提交的验收草稿不因此发布。
真实原件按
原 producer/request 保存，同责任后继可以引用；不为当前 Run 身份重新上传、重跑检查，
也不为阶段报告要求尚未发生的部署证明。对象归属与真实副作用边界保持。
既有同 Task、同生产 Run 的精确 ID-only 成果引用可从原件表解析完整元数据；不改写
历史引用，也不把该历史读取形态扩大为跨 Run 或部分 manifest 的授权。

已确认上传的 file manifest 沿真实 Task、上传 Run 与原字节保留；结果 artifact 的 Run
表示当前登记动作，不要求与上传 Run 相等。同 Task 后继经现有材料读取和下载入口恢复
尚未登记为结果的原件，负责人也可读取其 Issue 直接负责的 Task 上传；未完成原件不会
因此向整个项目公开。材料的 upload 来源保留原上传 Run，artifact 来源表示结果登记，
引用旧检查不自动声明它适用于新候选。worker 的 submit_result 可以用可选 existing_files
引用原 manifest，原本地文件上传方式不变；独立验收和显式分享继续使用其真实关系。

实施与验证见 [连续责任停止交接计划](plans/upgrade-historical-receipts.md)。

## D228：独立维护控制按业务状态收敛

D225 退役通用请求回执后，D155/D180 的维护准备与释放不能重发旧 CAS 来读取成功。
updater 用原不可变计划及当前 archive/host SoT 识别精确转换：准备为 false/P-1 → true/P，
释放为 false/P+1；原 normal host 为 normal/R → maintenance/R+1 → normal/R+2，原本
maintenance 的 host 始终保留原 R。后状态满足原授权转换，不证明某次传输请求是唯一执行者。
只执行尚缺的转换，额外修订、错误模式、具名 host 缺失或被另一升级接管拒绝，不刷新原 CAS。

prepare 重开只补原准备中尚未完成的对象。cancel/finish 共用恢复逻辑，先识别实际状态，
不先创建 preparation 或 host maintenance 再取消；未触碰对象不写，已正式恢复对象不写。
每步原输入/CAS和现有 context/期限保留，错误停止后续动作；回复丢失后从实际对象继续。
partial prepare、部分释放、重启、操作者以正式入口先恢复以及重复 finish 均沿同一状态收敛。
没有全局 run-zero、历史 native unknown 清零或重新审批前提；已有业务保护及物理结果不因此结算。

ArchiveAuthorization 继续表示现有维护观测，不能将 bounded extend 后的新快照当作不可变
原 prepare 回执。对于原 force/takeover 审计已证明计划 fence 未建立的具体情况，后续偶然
匹配的模式/修订不能倒推出原 fence；仍由原严格 unchanged-baseline 完成路径核验。
未准备取消的零写入不替代已安装窗口的该事实门。原 linked proof、健康、generation、
bounded remaining、failed-parent/source_operation 与未建立 fence 规则保留。

不新增表、请求档案、返回缓存、权限、wire 字段或状态格式。真实 App 保持 stale-CAS 拒绝，
回归必须通过真实 App/archive handler，不能用仍缓存请求结果的 fake 来证明恢复。
见[维护控制续行计划](plans/maintenance-control-continuation.md)。

## D229：连接级断连独立于单个 thread 消费者收尾

协调连接可以先恢复旧 thread，再为同一责任建立新 thread。连接级断连不属于其中
随机一个 thread，必须在保留已排队尾事件顺序和原 source 身份后通知全部受影响的
附件，并结束 Session 与其未结读取/回调；不能等待某个无人消费的旧 thread 再发起关闭。
重复 Close 不重复外部作用。取消或物理断连不替代原操作的终态，不将 unknown 转为成功。

历史 thread end 只结束原历史段，不能关闭当前连接或重放输入。Manager 与 pool 沿既有
退出路径释放本地 attachment，同 Task 的真实只读后继继续原回执；不清理业务账本或
以手工 ACK/清 pool 冒充交接。实现与验证见[协调断连计划](plans/coordination-disconnect-lifecycle.md)。

D229 的连接结束与本地消费者生命期分开：先收到断连的新 server 仍可沿原 thread
已确认的 closing metadata 附着并消费持久尾部，读完即 EOF；这不是原生 resume，
也不恢复模型、工具或新输入权限。事件传输一旦结束，缺本地原 metadata 的请求
不得重新排原生操作。空 thread 的 live/history 边界在首个消费者出现前仍保留原
身份与序号；历史边界不结束当前连接。原生结束、原件可读、消费 ACK 和资源释放
各保其真实事实，不以取消连接代替这些后续责任。

结束后的启动观测可以立即失败，但不能越过当时已排队尾部的消费与 ACK；先处理
该有界尾部，再保留原观测或消费错误退出，不把本地附着当作新原生执行就绪。

## D230：上下文索引与完整原生原件摄入

上下文投影不自动内联工具原文，避免读取上下文的结果递归成为后续上下文正文。
工具项保留身份、顺序、状态及明确的省略/原件引用；owner 原文和精确资料读取不变。
同源 API、CLI 与管理工具使用相同语义，不通过角色权限或新 grant 限制原件发现。

已认证 runner 的大原件沿现有 RPC 与 Envelope 传输，固定缓冲写入私有临时文件，
再按受控单原件并发标准解码并走原 App 校验、原件 staging、序号和 ACK。
未知长度先以小包预算加一字节的有界前缀判定真实大小；实际小 chunked 请求
不等待大原件处理预算，保留原精确历史回执认证。实际大包在继续读取正文前
完成大原件预认证；历史 receipt 预认证不准入其他业务操作。
未知长度前缀使用原小包内联预算；大原件暂存不整包入内存，既有解码/存储
仍按需物化一份原件，不宣称全链恒定内存。
原件不截断、不改字节/业务身份，不新增持久包装副本、协议代际或请求账本。

永久拒绝不能触发密集 executor 重入；原 pending 保留并按现有 RetryDelay 退避。
实际 session_stop 可先作为物理事实保存，不替代 Run/业务终态。其可选单次请求
预算与必需尾回执预算独立；首次停止和 releasing 恢复都从未取消的 runner lifetime
为后者取得原有有限期限，不继承可选请求已经耗尽的期限。父级取消仍有效，
不借此延续已停止的 runner。原 native_stopped 和 close 仍按完整尾部高水位
收敛。见[实施计划](plans/native-event-ingress-and-context-projection.md)。
