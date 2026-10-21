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
  - **建任务和改需求**：spec 的 mount 多于一个、而没有任何活动 host 支持多仓库时，`tasks_create`、`tasks_update_intent` 以 `invalid_request` 拒绝，消息写明“当前的 worker 只支持单仓库工作区”；引用多仓库模板同样拒绝。
  - **调度**：`scheduler.Candidate` 带上是否需要多仓库，host 带上能力；不支持的 host 跳过，原因“host's worker does not support multi-repository workspaces”，`tend status` 可见。
  - **上下文**：没有 host 支持多仓库时，完整快照和 `tasks_create` 的工具说明写明这项限制；`repos_list` 的模板标明不可用。
- 能力随配置变化：host 改配置、退役或新增后，限制在下一次配置同步时更新；已经建好的多仓库任务不受影响，只是在没有合适 host 时排队等待，原因可见。

## 3. orca 工作区（E3）

- **仓库映射**：runner 配置 `repos.<repo>.orca`，写 orca 里的仓库（`id:` 或 `path:` 选择器）。tend 不在 orca 里登记新仓库（orca 没有移除仓库的 CLI），由 owner 事先加入 orca。
- **建工作区**：实现 run 第一次 Prepare 时，`orca worktree create --repo … --name <工作区名> --base-branch <基点> --no-parent --setup skip --json`。先按名字查找已有的 worktree，保证重试和续接是幂等的。基点按 spec 的 mount 解析：分支或 commit。
- **工作区事实**：由返回的路径、`head`、分支生成 `WorkspaceReport`。按 D65，服务端在第一次报告时记录 orca 建出的分支，不再要求 `tend/<名字>`；之后同一工作区的报告必须一致。这是工作区的准备事实；产物照旧记录 worker 实际所在的分支和 HEAD，worker 换分支或 detach 仍按 D57 处理，不要求与这个分支一致。
- **准备命令**：profile 的命令照常在 worktree 里执行（D35）。
- **收集和验收**：读取 HEAD、未提交改动的快照、验收前的位置核对，和 gitws 相同，抽成共用代码（先做一个纯重构提交）。
- **清理**：和 gitws 相同的时机；删除时用 `orca worktree rm`，只删 tend 建的 worktree。

## 4. worker 的 MCP 桥（E4）

- runner 为每个 run 在自己的 home 下开一个 unix socket（0600），由一个随机 token 保护；harness 的 `ToolHandler`（现有的 `submit_verdict` 等）在 socket 上应答。
- `tend-runner worker-mcp --socket … --token-file …` 是 stdio MCP server，把 `tools/list`、`tools/call` 转给 socket。它在 runner 的二进制里，不依赖服务端的任何包。
- **写入 agent 的项目级配置**：在 Prepare 之后、启动 agent 之前写入工作区：
  - Codex：`.codex/config.toml` 的 `mcp_servers.tend`；
  - traex：它的项目级配置文件（实现时核对路径）。
  - token 放在这个 worktree 自己的 git 目录下（`git rev-parse --git-dir`，0600），配置里只写路径。orca 的 worktree 是 linked worktree，工作区里的 `.git` 是指向文件，不能直接当目录用。
- **排除**：这些文件写进 `git rev-parse --git-path info/exclude` 给出的文件（linked worktree 共用主仓库的这个文件），worker 不会把它们提交；验收的位置核对和快照也忽略它们。
- worker 继续带 `TEND_ADDON=off`（D45），主 Agent 的 tend MCP 和 hook 不会在 worker 里生效；worker 模式的 MCP 是另一个 server 名。

## 5. orca 的 TUI worker（E5）

- **启动**：`orca agent launch --agent <codex|trae> --worktree id:… --prompt <brief> [--model … --effort …] --json`。模型和推理强度来自 runner 配置。
  - 启动前先在 journal 记下这次启动（尝试编号和写进 brief 的一个随机标记）。CLI 报告回执丢失时，错误里带 `operationId`，记下后用 `--retry-launch` 重放，不重新启动。
  - runner 在启动途中退出、没拿到 `operationId` 时，不直接重新启动：先在这个 worktree 的 agent 里找带这个标记的那一个，找到就接上；找不到时这次启动的结果未知（约束 4），稍等后再查一次，仍没有才重新启动，之后若又出现带旧标记的 agent，关闭它的终端。
  - 返回的终端 handle 记进 journal，作为这次尝试的标识（类似 D61 的 thread）。
  - **必须是终端**：orca 在 Chat UI 打开时会以 chat 启动（`outcome.kind` 不是终端）。runner 检查启动结果，不是终端时关闭它，run 以明确的错误失败，提示在这台 host 的 orca 里为该 agent 关闭 Chat UI。部署文档把这列为前提。
- **一轮结束**：
  - Codex：`terminal wait --for tui-idle`；
  - traex：同样（orca fork 的上游已修复）；
  - orca 的 agent 状态（`worktree ps` 的 `done`）作为第二个依据。
  - 实现 run 的一轮结束后，runner 照常收集产物；验收 run 以 `submit_verdict` 的调用为准，一轮结束仍没有结论时记为 inconclusive，和现在相同。
- **日志**：上传 agent 状态的变化、每一轮的提示和最后一条回复（§9 的选择 3）。
- **中断与续接**：
  - 暂停时发 Esc 中断当前一轮、保留终端，恢复时发一轮继续的说明（§9 的选择 2）；
  - 下一次尝试优先续接原来的 agent 会话（orca 有 agent 的 resume 记录，实现时核对），不能续接时启动新的 agent，带 D61 的“之前被中断”的说明。
- **停止**：run 取消时关闭 tend 启动的那个终端（`terminal close --terminal <handle>`），不动 worktree 里其他人的终端。
- **信任**：traex 的目录信任由 orca fork 处理（owner 确认上游已修复），实现时在集成测试里确认不会停在确认上。

## 6. 人工接管（E6）

- **察觉**：runner 记下 tend 自己发出的每一轮（启动时的提示、`terminal send` 的请求 ID 和 orca 的轮次序号）。agent 状态里出现一轮不是 tend 发起的工作（提示不同，或轮次序号前进而没有对应的请求），就视为 owner 接管。
- **记录**：runner 报告 `runs.report_takeover`，服务端把任务转为 `human_control`，产生 `task.taken_over` 事件（需要处理的一类，主 Agent 下一次会提到）。不需要 owner 先执行命令，但 owner 也可以用 `tend takeover <task>` 显式接管（用于“我想先接手，再动手”）。
- **接管期间**：runner 不再把空闲当作完成，不收集产物，不发任何输入；终端保持原样，worker 的租约照常续期。和 owner 主动暂停不同，接管不打断 agent。
- **交还**：`tend resume <task>`，按 §9 的选择 1 继续。
- **cove 之后**：察觉换成 cove 的控制者变化，其余不变。

## 7. 真机验收（E7）

- 两台 host 都改为 `worker: orca`，Codex 作为 worker；确认 tend 只接受单仓库任务，多仓库的 `tasks_create` 被拒绝并说明原因。
- 一个任务经 orca 完成实现、验收、completed；任务出现在 owner 的 orca 里，产物的分支是 orca 的命名。
- 在 orca（桌面或手机）里给正在工作的 worker 发一句话：tend 察觉接管、任务转为 `human_control`、主 Agent 提到；`tend resume` 后 worker 继续并完成。
- runner 中途重启：worker 续接或按 D61 的说明重新开始；回执丢失时启动不会重复。
- 两台 host 中一台改回 `appserver`：多仓库任务又可以建，只由这台 host 领取。

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
| 3 | `refactor: share workspace reading between providers` | E3 的前置重构 |
| 4 | `feat: build single-repository workspaces in orca` | E3（D65） |
| 5 | `feat: let workers deliver through a runner-local MCP bridge` | E4（D66） |
| 6 | `feat: run agent TUI workers in orca` | E5（D66） |
| 7 | `feat: notice the owner taking over a worker in orca` | E6（D67） |
| 8 | `docs: record the phase e acceptance` | E7 |
