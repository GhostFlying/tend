# 阶段 E 计划：orca 承载的 TUI worker 与 harness 能力（E1–E7）

**日期：2026-10-21，owner 已确认 §9 的选择**
**依据：`docs/design.md` D33–D38、D41、D51、D57、D61、D63；`docs/findings/orca-worker.md` 的 spike 结论；约定见 `AGENTS.md`。**
**前置：阶段 D 已合并，真机验收完成（`docs/findings/phase-d-acceptance.md`）。**

本阶段的工作项编号为 E1–E7。

D63 定了方向：接入 cove 之后，worker 是 cove tab 里的 agent TUI，人在 cove 里取得终端控制权就是接管；在那之前用 orca 临时承载单仓库任务。每台 host 显式选择 harness，harness 的能力向上约束 tend 接受什么。本阶段把这些落地：harness 能力（E1），orca 的工作区和 TUI worker（E2–E5），经 orca 的人工接管（E6），真机验收（E7）。

## 0. 范围与完成标准

- **交付物**：
  - host 的 harness 在服务端配置里显式指定，能力向上约束建任务、调度和上下文（E1）；
  - orca CLI 的客户端（E2）；
  - orca 的工作区提供方：单仓库任务的工作区是 orca worktree（E3）；
  - worker 经 runner 本机的 MCP 桥交付产物和验收结论（E4）；
  - orca 的 TUI worker：Codex 和 traex（E5）；
  - 人工接管：在 orca 里操作 worker 的终端即接管，tend 察觉后停止自动推进，owner 交还后继续（E6）；
  - 真机验收（E7）。
- **完成标准**：
  - `make check` 通过；§8 的测试全部通过；
  - 集成测试（build tag `integration`）在一台有 orca 的 host 上用 Codex 跑通实现、验收、completed；
  - 真机验收（§7）。
- **不在本阶段**：
  - cove 的接入（等 cove 的 tend 集成排期，D63 列出的接口就绪后另做）；
  - Claude Code 作为 TUI worker（它的项目级 MCP 需要逐个批准，另行验证）；
  - orca 承载多仓库任务（orca 不支持，按 D63 由能力约束拒绝）；
  - 托管主 Agent（D45）。

## 1. 写入 design.md 的决策

| 编号 | 决策 | 落地 |
| --- | --- | --- |
| D64 | harness 能力的落地（细化 D63）：服务端配置为每台 host 写明 `worker`（`appserver` 或 `orca`）；能力表由 tend 定义；runner 在 hello 中报告自己的 worker，与配置不符时拒绝（§2）。 | E1 |
| D65 | 工作区提供方记录自己建出的分支（修订 D33 的分支命名）：gitws 仍用 `tend/<工作区名>`，orca 用它自己的命名；tend 在第一次工作区报告时记录分支，之后同一工作区的报告必须一致；产物仍记录实际分支（D57）（§3）。 | E3 |
| D66 | orca 的 TUI worker（细化 D41、D61）：启动可重放；经 runner 本机的 MCP 桥交付，服务端协议不变；暂停发 Esc、保留终端；日志只传状态变化和每轮的提示与回复（§4、§5）。 | E4、E5 |
| D67 | 经 orca 的人工接管（细化 D51、D63）：出现一轮不是 tend 发起的工作，即视为 owner 接管，任务转为 `human_control`；owner 交还后按 §9 的选择继续（§6）。 | E6 |

## 2. harness 能力（E1）

- **配置**：服务端 `hosts[].worker`，默认 `appserver`（现有部署不变）。runner 配置的 `worker` 段决定实际启动哪种 harness；hello 时 runner 报告 worker 类型，与服务端配置不符时 hello 失败并说明原因，不静默按任何一方运行。
- **能力表**：定义在 `protocol`（两端共用），纯数据：
  - `appserver`：多仓库工作区；
  - `orca`：单仓库工作区；可接管。
- **向上约束**：
  - **建任务和改需求**：spec 的 mount 多于一个、而有活动 host 且都不支持多仓库时，`tasks_create`、`tasks_update_intent` 以 `invalid_request` 拒绝，消息写明“当前的 worker 只支持单仓库工作区”；引用多仓库模板同样拒绝。
  - **调度**：`scheduler.Candidate` 带上是否需要多仓库，host 带上能力；不支持的 host 跳过，原因“host's worker does not support multi-repository workspaces”，`tend status` 可见。
  - **上下文**：没有 host 支持多仓库时，完整快照和 `tasks_create` 的工具说明写明这项限制；`repos_list` 的模板标明不可用。
- 能力随配置变化：host 改配置、退役或新增后，限制在下一次配置同步时更新；已经建好的多仓库任务不受影响，只是在没有合适 host 时排队等待，原因可见。

## 3. orca 工作区（E3）

- **仓库映射**：runner 配置 `repos.<repo>.orca`，写 orca 里的仓库选择器（`id:`、`name:` 或 `path:`）；runner 用 `orca repo show` 得到它的本地路径。tend 不在 orca 里登记新仓库（orca 没有移除仓库的 CLI），由 owner 事先加入 orca。
- **实现方式**：orca 是 gitws 的另一种布局，不是另一个包。gitws 现在假定工作区根目录下每个 mount 一个子目录、状态放在根目录的 `.tend/`；先做一个纯重构提交，把“worker 从哪里开始”“mount 在哪里”“状态放在哪里”“新 mount 怎么建出来”抽成布局，读取、快照、验收前核对和准备命令都只经过布局拿目录。现有布局（runner 自己的 bare clone 加 git worktree）行为不变。
- **orca 布局**：
  - 只接受单个 mount（E1 已保证）。worker 从 orca worktree 的根目录开始；状态放在 `$TEND_HOME/workspaces/<工作区名>/.tend/`，不写进 worktree；不写工作区根目录的 `AGENTS.md`（worktree 就是仓库本身，仓库自己的 `AGENTS.md` 照常生效）。原来 `AGENTS.md` 里关于基点和规则的说明改由 `Prepared.Guide` 带给 worker，E5 放进 brief。
  - **建 mount**：先在 owner 的仓库里 fetch origin 的所有分支（显式 refspec，不依赖也不改动 owner 仓库的 fetch 配置），用同一个 `ResolveBase` 把 spec 的基点解析成 commit（D36），再 `orca worktree create --repo … --name <工作区名> --base-branch <sha> --no-parent --setup skip`（已验证 `--base-branch` 接受 sha）。
  - **幂等**：orca 对重名不报错，而是建出 `<名字>-2`。所以在请求 orca 之前，先把解析好的基点作为待建的 mount 记进状态。重试时沿用记下的基点，按显示名找 worktree：找到一个且基点一致就接着用；没有记录却已有同名 worktree（例如 owner 自己建的）、基点不一致或有多个同名时失败并说明，交给 owner。请求的回执丢失时（CLI 中断不会中止 orca 里的创建），重试先等待一段时间（2 分钟）看 worktree 是否出现，期间不重新请求（约束 4）；orca 明确拒绝时才立即重试。建好后 worktree 的路径记进状态。
  - **工作区事实**：路径、`head`、去掉 `refs/heads/` 的分支。按 D65，服务端在第一次报告时记录 orca 建出的分支，不再要求 `tend/<名字>`；之后同一工作区的报告必须一致。这是工作区的准备事实；产物照旧记录 worker 实际所在的分支和 HEAD，worker 换分支或 detach 仍按 D57 处理。
  - 产物的提交照旧用 `refs/tend/runs/…` 保持可达；对 orca 来说这些 ref 在 owner 的仓库里。
- **准备命令**：mount 的 profile 在 worktree 里执行；工作区级的 profile 也在 worktree 里执行（单仓库时两者是同一个目录）。
- **清理**：和 gitws 相同：任务的工作区不由 runner 删除，只删除验收用的 scratch 目录。`orca worktree rm` 留给以后的工作区清理。
- **启用**：runner 配置 `worker.kind: orca` 选择这个布局；在 E5 的 TUI worker 落地前，runner 仍拒绝 `orca`。

## 4. worker 的 MCP 桥（E4）

- **桥**（`internal/harness/bridge`）：worker 的工具（现有的 `submit_verdict`，由 harness 的 `ToolHandler` 在进程内应答）在 runner 本机的 unix socket（0600）上提供，每个请求带 run 的 token。`tend-runner worker-mcp --state … --run …` 是 stdio MCP server（initialize、ping、tools/list、tools/call），每个请求都重新读状态文件里的 socket 和 token 再转给 socket，runner 重启换了 socket 也能接上。状态文件记着所属的 run，和启动参数里的 run 不同（同一 worktree 已换成下一个 run）或文件已删除时只回错误，上一个 run 留下的 agent 碰不到后来的 run。它在 runner 的二进制里，不依赖服务端的任何包。token 由调用方注入的 ID 生成器产生（约束 5）。
- **写入 agent 的项目级配置**（TUI worker 启动 agent 前，E5 调用）：
  - Codex：worktree 的 `.codex/config.toml`；traex（orca 里的 `trae`，即 `traecli`）：`.trae/traecli.toml`。两者是同一格式，写一个 `[mcp_servers.tend_worker]`，command 是 runner 自己的可执行文件，`default_tools_approval_mode = "approve"`（否则无人值守时工具调用被拒）。orca 启动 agent 时不能传参数，D38 里 runner 要覆盖的无人值守设置（`approval_policy = "never"`、`sandbox_mode = "danger-full-access"`，D39）也写在这个文件里。
  - 文件已存在且不是 tend 写的（仓库自己的或 owner 的）时失败并说明，不合并、不覆盖。
  - run 的 token 和 socket 写在这个 worktree 自己的 git 目录下（`git rev-parse --absolute-git-dir`，0600 的 `tend-worker.json`），配置里只写路径。
  - **排除**：配置文件写进 `git rev-parse --git-path info/exclude`（锚定路径，如 `/.codex/config.toml`），worker 的 `git add -A` 不会带上它们，收集的快照也不含它们。linked worktree 与主仓库共用这个排除文件，因此 owner 主 checkout 里同名的未跟踪文件也会被 `git status` 忽略，部署文档写明。
  - run 结束时删除配置文件和 `tend-worker.json`；排除行保留（幂等）。
  - Codex 和 traecli 只对受信任的项目加载项目级配置，worktree 的信任跟随仓库根目录：owner 需要事先在 agent 里信任这个仓库（部署前提）。
- **worker 不是主 Agent**（D45）：orca 启动的 agent 继承 orca 的环境，runner 无法设置 `TEND_ADDON=off`。tend CLI 的 hook 和 MCP 另外检查当前目录所在 worktree 的 git 目录里有没有 `tend-worker.json`，有就和 `TEND_ADDON=off` 一样什么都不做。

## 5. orca 的 TUI worker（E5）

`internal/harness/orca` 的 `Workers` 实现 `harness.Workers`；runner 配置 `worker.kind: orca` 时使用它和 `gitws.NewOrca`，`worker.agent` 选 codex 或 traex（orca 里叫 `codex`、`trae`），`model`、`effort` 可选，每个仓库用 `repos.<name>.orca` 指明 orca 里的登记。

- **worker 的状态**：agent 活在 orca 里，比 worker 活得长。worker 把找到它所需的东西作为 run 的 thread 记进 journal（D61 的 thread 由此变成 harness 自己的状态，`OnThread` 可以多次调用）：启动标记和发起时间、回执丢失时的 `operationId`、终端 handle 和 pane key、tend 最近一轮的开始时间。traex 的 thread ID 解析不了，视为没有状态，重新开始。
- **启动**：`orca agent launch --agent <codex|trae> --worktree id:… --prompt <brief> [--model … --effort …]`。
  - 先在 journal 记下标记（写在 brief 开头）和发起时间，再启动。CLI 报告回执丢失时记下 `operationId`，用 `--retry-launch` 重放，不重新启动。
  - 有标记而没有终端和 `operationId`（runner 在启动途中退出）时，按标记在这个 worktree 的 agent 里找；在发起后的 settle 时间（默认 2 分钟）内一直找不到，才换新标记重新启动（约束 4）；读不到 orca 不算找不到，这次尝试失败，启动保持未知。
  - orca 明确拒绝时清掉标记；结果无法确认时保留，下一次尝试按上面处理。
  - **必须是终端**：结果不是终端时关闭它，run 以明确的错误失败，提示在这台 host 的 orca 里为该 agent 关闭 Chat UI。
  - TUI 没有单独的 developer instructions，角色说明放在 brief 前面。agent 在仓库的 worktree 里启动，brief 用 gitws 给出的 `Guide` 描述工作区（D65），不提 AGENTS.md。
- **续接**：下一次尝试时终端还在：
  - agent 还在 tend 的那一轮里（working），只等它结束，不发新的输入；
  - 这一轮已经在 worker 不在时结束（done、没有被中断、晚于这一轮的开始），实现 run 直接当作结束；验收 run 的结论只在上一个 worker 的内存里，不当作结束，发一轮继续的说明请它重新调用 `submit_verdict`；
  - 否则发 `harness.Continuation`（`terminal send --enter --wait-submit`）。回执丢失时把 orca 的请求 ID 和这一轮的文本记进 journal，用 `--retry-request` 和原来的文本重放，下一次尝试也先重放它，不发新的一轮；重放沿用原来这一轮的开始时间和中断标记。
  - tend 发 Esc 前先在 journal 记下“已中断、未续接”；orca 晚些才报告中断时（续接时还显示 working），等到它报告 done 且被中断，再发继续的说明。
  - 终端不在了就启动新的 agent，带完整 brief 和“之前被中断”的说明。
- **一轮结束**：`terminal wait --for tui-idle`（分段等待；一段内没等到时 orca 回 `timeout` 错误，不算读取失败），并且 `worktree ps` 里这个 pane 的 agent 是 done、没有被中断、状态开始于这一轮之后。waiting、blocked 不算结束（等人处理，E6 会把它们和接管联系起来），orca 立即返回时按轮询间隔再看；被中断的一轮也不算。终端消失或 agent 退出时 run 失败；连续多次读 orca 失败时 run 失败。
  - 实现 run 结束时照常收集产物；验收 run 以经 MCP 桥的 `submit_verdict` 为准。结论只在这个 worker 的内存里，接上的一轮可能已经把结论交给了之前的 worker，所以一轮结束仍没有结论时先发一轮提醒（`harness.VerdictReminder`）请它重新调用，仍没有才记为 inconclusive。
- **日志**：`turn.started`（提示）、`agent.state`（状态变化）、`turn.completed`（最后一条回复）。完整过程在 orca 里看。
- **中断与停止**：worker 的 context 结束时按原因处理：
  - `harness.ErrStopped`（tend 不再需要这个 run）关闭 tend 启动的那个终端，不动 worktree 里其他人的终端；
  - 启动的回执还没到就被停下时，先按标记找一会儿（最多 10 秒），找到就按下面处理。
  - 其他原因（暂停、fencing、runner 退出）发 Esc（原始字节，不带回车；orca 的 `--interrupt` 发的是 Ctrl-C，会让 Codex 退出）中断这一轮、保留终端，下一次尝试按上面续接。
  - 正常结束时终端保留，owner 可以在 orca 里查看；worktree 随任务保留（D65）。
  - **释放**：runner 放弃一个没有结果的 run（取消、失败、hello 时被 tend 停掉）时，在发出 release 之前调用 harness 的 `Releaser`：按 journal 里的状态关闭这个 run 的终端（只有标记时按标记找，等到这次启动的 settle 时间过去）。这样暂停后取消、worker 失败等没有 worker 在跑的情况也不会留下继续写工作区的 agent；关闭失败只记日志，release 照常发出。
- **MCP 桥**：每次尝试开 socket、写配置（E4），结束时关闭并删除，Esc 后的 agent 在下一次尝试重新写入状态后接上。
- **信任**：traex 的目录信任由 orca fork 处理（owner 确认上游已修复），E7 在真机上确认不会停在确认上；Esc 的效果、`worktree show` 和 agent 状态的取值同样在 E7 核对。
- **agent 的状态来自它的 hook**：orca 靠 agent 的 hook 得知状态，hook 被关掉（E7 在一台 host 上遇到 Codex 的 `[features] hooks = false`）或未被信任时，orca 里一直没有这个 agent。worker 发现终端还在、orca 却持续 `Settle`（默认两分钟）没有它的状态时，以指明 hook 的错误失败，而不是一直等下去。

## 6. 人工接管（E6）

- **察觉**（orca worker）：worker 在 journal 里记下 tend 最近一轮提示的开头（空白归一后的前 200 个字符；orca 可能不保存长提示的全部）和这一轮的开始时间。这一轮开始之后，orca 的 agent 状态里出现下面任何一种，就视为 owner 接管：
  - agent 在做的提示和 tend 的不同（两者开头在共同长度内不一致）；
  - 这一轮被中断，而 tend 没有发过 Esc（journal 里的中断标记）。
  - worker 在两处检查：等一轮结束时，以及下一次尝试接上 agent 时（覆盖“暂停期间 owner 在 orca 里操作，然后恢复”）。
- **记录**：worker 调用 `WorkerSpec.OnTakeover`，runner 报告 `runs.report_takeover`（run 自己的凭据，载荷是察觉的原因）。这是事实而不是 owner 的控制请求，不带 `expected_control_revision`（约束 3 针对控制类写入），由领域规则决定：
  - 当前的 run（实现 run 是当前 spec 版本的当前代次，验收 run 是正在验收的产物）：任务转为 `human_control`，控制版本加一，产生 `task.taken_over`（需要处理的一类）；已经是 `human_control` 时不重复记录；
  - 过时的 run（包括 start over 之后的旧代次）或已结束的任务：只记一条 stale 的 `task.taken_over`。
  - 请求先进 runner 的 journal 再发（见下文“被接管”由 runner 记）。
  - owner 也可以用 `tend takeover <task>` 显式接管（`control.set_mode human_control`，主 Agent 不能这样做，见现有授权）。
- **接管期间**：`human_control` 下 run 的 desired 是新的 `held`（之前和暂停一样是 `paused`，那会让 worker 发 Esc 打断 owner）。runner 以 `harness.ErrHeld` 停下这次尝试，等到 desired 变回 `run`。
  - **“被接管”由 runner 记**（journal 的 `held`），不由 worker 记：runner 从 tend 看到 `held`（包括暂停期间、没有 worker 在跑时 owner 用 `tend takeover`），或自己的接管报告被 tend 收下时，都记下来。报告被收下后 runner 立即在本地当作 `held` 停下这次尝试，不等心跳（owner 可能在下一次心跳前就已交还）；比这次报告更早发出的心跳带回的 desired 不会把它改回去。接管报告先进 journal 再发：tend 可能已经收下而 runner 没收到回执，下一次尝试先用原来的请求 ID 重发它（tend 按 ID 去重，不会再记一次接管，也就不会撤销这期间 owner 的交还），收下后才记 `held`。从没被 tend 收下的报告不算接管，下一次尝试会再次察觉并报告，而不是把还在 owner 手里的 agent 交还给 worker。`held` 在 runner 接受 tend 发来的 `held` 时立即记下，不依赖之后读到的 desired。已知限制：owner 在 runner 离线时（或两次心跳之间）先 `tend takeover` 再 `tend resume`，runner 从没看到 `held`，接上 agent 时会把 owner 的提示当作新的接管，任务回到 `human_control`；结果是保守的（tend 再次让开，owner 再 resume 一次），要完全避免需要服务端按 run 记录交还，暂不做。
  - orca worker 察觉后不判断完成、不收集、不发任何输入；被停下时（`ErrHeld`、runner 退出、fencing）都不碰 agent，只有 tend 不再需要这个 run（取消）时才关闭终端。租约照常续期。
  - app-server worker 不会被接管；它的任务被 owner 设为 `human_control` 时，`held` 和暂停一样打断 worker。
- **交还**：`tend resume <task>`。runner 记着 `held` 时，下一次尝试的 `WorkerSpec.HandBack` 为真，worker 接上 agent 后（有回执丢失的一轮时先把它重放完）发 `harness.HandBack` 作为单独的一轮（“owner 和你一起处理过，检查工作区后完成任务”，验收 run 另要求重新调用 `submit_verdict`），之后照常等一轮结束、收集、验收；worker 启动成功后 runner 清掉 `held`。
- **cove 之后**：察觉换成 cove 的控制者变化，其余不变。

## 7. 真机验收（E7）

- 两台 host 都改为 `worker: orca`，Codex 作为 worker；确认 tend 只接受单仓库任务，多仓库的 `tasks_create` 被拒绝并说明原因。
- 一个任务经 orca 完成实现、验收、completed；任务出现在 owner 的 orca 里，产物的分支是 orca 的命名。
- 在 orca（桌面或手机）里给正在工作的 worker 发一句话：tend 察觉接管、任务转为 `human_control`、主 Agent 提到；`tend resume` 后 worker 继续并完成。
- runner 中途重启：worker 续接或按 D61 的说明重新开始；回执丢失时启动不会重复。
- 两台 host 中一台改回 `appserver`：多仓库任务又可以建，只由这台 host 领取。
- 集成测试 `TestIntegration_OrcaWorkers`（`make it`）：`TEND_ORCA_REPO` 指向 owner 在 orca 里登记的一个临时仓库（`TEND_ORCA_AGENT`、`TEND_ORCA_MODEL` 可选），实现和验收各由一个 orca 里的 agent 完成，验收结论经 MCP 桥交回；测试结束删除它建的 worktree 和终端。没有设置时跳过。

## 8. 测试

- **单元测试**：
  - 能力表与约束：建任务的拒绝、调度的跳过、快照文字（E1）；
  - orca 客户端用假的 CLI（testkit 里的 fake，记录调用、按脚本返回 JSON），覆盖重放和错误映射（E2）；
  - orca 工作区：幂等的 Prepare、分支记录、排除文件（E3）；
  - MCP 桥：socket 鉴权、工具转发、错误 token 被拒（E4）；
  - TUI worker 的状态机：启动重放、一轮结束、取消、续接（E5）；
  - 接管的察觉规则与 `human_control` 的转换（E6）。
- **故障场景**：
  - `TestFault_OrcaLaunchReplyLost`：启动回执丢失，重放不重复启动；
  - `TestFault_OwnerTakesOverMidTurn`：接管期间不收集、不发输入，交还后继续；
  - `TestFault_HostWorkerMismatch`：runner 的 worker 与配置不符，hello 失败。
- **集成测试**（`integration`）：在有 orca 的 host 上用 Codex 跑一个任务的完整流程。

## 9. owner 的选择（已确认）

1. **交还后怎么继续**（已定：A）：owner 在 orca 里手动处理后执行 `tend resume`，worker 收到一轮“owner 和你一起处理过，检查工作区后完成任务”，之后照常结束、收集、验收。
2. **暂停对 TUI worker 意味着什么**：
   已定：A。中断 agent 当前这一轮（发 Esc），保留终端；恢复时发一轮继续的说明。
3. **上传哪些 worker 日志**：
   已定：A。agent 状态的变化、每一轮的提示和最后一条回复；完整过程在 orca 里看。
4. **部署**（已定）：两台 host 都改为 orca，tend 暂时只接受单仓库任务。§7 最后一项（改回 `appserver`）只作为验收中的临时切换，验收后两台都是 orca。

## 10. 实施顺序与提交拆分

| 步骤 | commit | 内容 |
| --- | --- | --- |
| 1 | `feat: declare each host's worker and let its capabilities shape tend` | E1（D64） |
| 2 | `feat: add an orca CLI client` | E2 |
| 3 | `refactor: route workspace paths through a layout` | E3 的前置重构 |
| 4 | `feat: build single-repository workspaces in orca` | E3（D65） |
| 5 | `feat: let workers deliver through a runner-local MCP bridge` | E4（D66） |
| 6 | `feat: run agent TUI workers in orca` | E5（D66） |
| 7 | `feat: notice the owner taking over a worker in orca` | E6（D67） |
| 8 | `docs: record the phase e acceptance` | E7 |
