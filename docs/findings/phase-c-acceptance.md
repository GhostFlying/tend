# 阶段 C 验收结论

计划 §8 的验收分两步：

- 先在一台开发机上做本地预验收，覆盖不依赖 tailnet 的部分（本节）；
- 剩下的项目和 owner 一起在真机上完成，见“真机验收”一节。

## 本地预验收

**环境：**

- Claude Code 2.1.301、Codex 0.162.1、traex 0.210.1；worker 使用 traex，模型 GPT-5.6-Sol。
- 服务端、runner 和三个 agent 在同一台机器上。服务端不加入 tailnet，用 `listen` 和 `local_owner`（D49）。
- 测试仓库是一个本地 bare 仓库，内容是一个很小的 Python 模块和它的单元测试。
- 每个 agent 使用单独的配置目录，由 `tend setup <agent> --write` 写入配置。agent 在仓库之外的空目录里启动。
- 会话用非交互模式驱动：Claude Code 用 `-p` 和 stream-json，Codex 和 traex 用 `exec`。非交互模式下 Codex 和 traex 要跳过 hook 信任，TUI 里的信任留到真机验收。

### 结果

| 项 | 结果 |
| --- | --- |
| 基本闭环 | 三个 agent 都通过：建任务、实现、验收、completed，下一次输入时 agent 主动报告结论和分支 |
| 会话开始看到快照 | 通过 |
| `tend status`、`tend logs`、`tend task` | 能用，但输出难读，已修（F3、F4） |
| artifact 的分支和 commit 在 runner 上可查 | 通过 |
| 会话中途停掉服务端 | 通过：输入不被阻塞，hook 注入一行不可达提示，工具调用返回原因；恢复后继续 |
| 主动压缩 | 通过（Claude Code）：压缩后重新得到完整快照，模型能区分压缩前的记忆和新快照 |
| 长会话中的增量 | 通过 |
| 两个写入者修改同一任务 | 通过：后到的写入得到 `stale_revision` 和当前状态，模型重新读取后正确处理 |
| 用 `tend decide` 回答待决事项 | 部分通过：agent 下一次输入时看到已回答，但回答本身没有被执行，见下面的问题 1 |
| 暂停与恢复 | 通过：agent 下一次输入时注意到 owner 暂停了任务 |

待决事项通过 git_tampered 路径产生：验收条件故意写成实现无法满足。

### 发现并已修复的缺陷

- **F1**：traex 0.210.1 不读 `hooks.json`，只读 `traecli.toml` 里的 `[[hooks.<Event>]]` 表，所以 `tend setup traex` 写入的 hook 不生效。已改为写入 toml 表（#42）。
- **F2**：tend 不可达时启动的会话里，tend 工具全部缺失。`tend mcp` 现在缓存上一次的工具列表，`tools/list` 有超时（#44）。
- **F3**：`tend logs` 输出原始 JSON。现在每个条目一行（#45）。
- **F4**：`tend task` 不显示 artifact，验收意见显示为原始 JSON（#45）。
- **F5**：`tend setup` 在什么都不需要改时，仍然提示加 `--write`（#42）。
- **F6**：外挂形态下，主 Agent 没有暂停、恢复和取消任务的工具。已补 `tasks_set_control` 和 `tasks_cancel`（#43）。

### 未决问题

1. **待决事项的回答不会被执行。** 按 D31，回答只是记录，由主 Agent 据此行动。外挂形态下 owner 常用 `tend decide` 直接回答，这时要等 owner 回到某个 agent 会话，agent 看到增量后才会执行；在那之前任务一直停在 `blocked: waiting_decision`。已按 D53 解决（C11，`docs/plans/decision-effects.md`）：tend 自己开出的待决事项里，重试和取消类的选项在回答时由 tend 直接执行，agent 下一次输入时从增量里得知。
2. Codex 的 memories 子会话也会触发 SessionStart，每次都拿到完整快照，增加了不必要的请求和水位记录。已按 D54 解决（C15）：只有调用过 tend 工具的会话才会收到上下文。
3. 完整快照只列未结束的任务。会话开始时 agent 看不到刚刚完成的任务，只能依赖增量。不做：控制入口是一个长期会话，通过增量得知任务结束。
4. 增量不说明变化来自谁（owner、其他会话还是 tend 自己），agent 无法区分。已按 D55 解决（C14，#51）。
5. worker 的工作区只有 `origin/main`，没有本地的 `main` 分支。按 `main` 写的验收命令会失败，这一点应该写进 worker 的说明。已解决（C13，#50）：工作区说明写明每个挂载的基线 commit；验收条件仍由主 Agent 按任务决定。
6. runner 重新连上服务端时没有日志（小问题）。已解决（C12，#49）。

## 真机验收

**环境：**

- 服务端和 runner 在 owner 的一台开发机上，都通过 tsnet 加入 tailnet。这台机器是 tagged 节点，按 D56 配置为 owner 设备；机器出网要经过 HTTP 代理。
- owner 用自己日常的 Codex（TUI）作为控制会话，配置由 `tend setup codex --write` 写入；traex 的配置同样写入，但这轮没有用 traex 会话。Claude Code 的配置没有改。
- worker 使用 traex，模型 GPT-5.6-Sol。
- 仓库是 tend 自己，需求是给 CLI 加 `tend version` 命令。这是一次测试，结果不合入 `main`。

需求跑了两次。第一次暴露出提交模型的问题，任务被取消；按 D57 修改后，第二次一代就通过了。

### 结果

| 项 | 结果 |
| --- | --- |
| 服务端和 runner 加入 tailnet，https | 通过；tagged 的 owner 设备需要 D56（#53） |
| `tend login`、`tend status` 经过 tailnet | 通过；CLI 原先把 tailnet 地址也发给 HTTP 代理，已修（#54） |
| 在 owner 自己的配置上运行 `tend setup` | 通过；原先会改写 owner 已有 hook 里的 `&`、`<`、`>`，已修（#55） |
| Codex TUI 里信任 tend 的 hook | 通过 |
| 会话绑定（D54） | 通过：会话第一次调用 tend 工具时绑定，之前的 SessionStart 不注入任何内容 |
| 建任务、调度、工作区、实现、验收、completed | 第二次通过：一代完成，worker 按任务要求自己提交了一个符合仓库规范的 commit，验收核对了提交、作者身份和未推送 |
| 下一次输入时报告结论 | 通过：两次都在 owner 下一次输入时报告了验收结论、run 失败、新开的待决事项和任务完成 |
| 增量写明来源（D55） | 通过 |
| 在会话里回答待决事项，tend 执行（D53） | 通过：选择取消后 tend 直接取消了任务 |
| artifact 的分支和 commit 在 runner 上可查 | 通过；结果没有推送到远端 |

### 发现并已修复的缺陷

- **R1**：CLI 把 tailnet 上的服务端地址也交给 HTTP 代理，代理无法解析，`tend login` 失败。tailnet 的地址现在绕过代理（#54）。
- **R2**：`tend setup` 用 Go 默认的 JSON 编码重写配置，把 owner 已有 hook 命令里的 `&`、`<`、`>` 转义，预览的 diff 也先列新行再列旧行（#55）。
- **R3**：tagged 节点在 tailnet 里没有登录名，无法作为 owner 设备。按 D56 增加 `owner_nodes`（#53）。
- **R4（主要问题）**：runner 替 worker 提交，worker 不许提交。任务要求“交付为一个符合仓库规范的 commit”，验收据此拒绝 runner 写的记录提交；实现 worker 无法提交，第三代把 runner 的提交 reset 掉，run 以 `git_tampered` 失败；即使重试，也会从同一个被改动的状态出发再次失败。任务无法通过。按 D57 改为提交归 worker：tend 不提交，也不限制 worker 怎么用 git，只如实记下每个 mount 的分支、HEAD 和未提交改动的快照（#56 的说明修改为过渡，#57 为正式修改）。
- **R5**：`make check` 里的 `go run staticcheck@…` 在模块之外构建，在本地 Go 比模块旧的机器上会切到 staticcheck 自己要求的旧版 Go，无法加载本模块。两个 worker 都靠 `GOTOOLCHAIN` 绕过。现在 staticcheck 固定使用构建本模块的工具链（#56）。
- **R6**：traex 把长时间的命令转到后台时先报一次“完成”，状态为 `backgrounded`、退出码为 0，`tend logs` 把它显示成 `exit 0`。现在只有结束的命令显示退出码（#56）。
- **R7**：`TestEndToEnd_RunnerCompletesTask` 在任务完成后立刻停 runner，runner 可能还没从 journal 删除这个 run，CI 偶发失败（#57）。
- **R8**：runner 的请求没有超时。故障演练中服务端重启时，runner 正在等待领取；tsnet 的用户态网络栈不会因为对端进程退出而断开连接，这个请求一直没有返回，runner 不再领取新的 run，直到它自己重启。现在每个请求都有超时，超时后用同一个 request ID 重发。同一轮还发现服务端不可达时，事件上传每 0.5 秒记一次 warn；现在每次连续失败只记一次（#60）。
- **R9**：runner 停止时，为被自己取消的请求记 "request failed, retrying"；服务端把 runner 断开导致的领取取消记为 ERROR。两者都不是故障，现在不再这样记（#61）。

### 未决问题

1. 取消任务时没有清掉阻塞原因，已取消的任务仍显示 `blocked: waiting_decision`（小问题）。已解决（C17）。
2. `tend logs` 只接受 run ID，传任务 ID 返回 not_found，帮助里也没写参数是什么（小问题）。已解决（C18）：传任务 ID 时按顺序显示它的每个 run。
3. runner 的日志不记录领取和开始 run，排查时只能查账本（小问题）。已解决（C19）：领取和最终报告各记一条。
4. 增量里的行是事件的原样输出，只有任务 ID 没有标题；`changes_required` 不带验收意见，agent 要再读一次任务才知道原因。已解决（C20）。
5. worker 继承本机 agent 的配置（D38），也会读 owner 的个人记忆。第二次实现时 worker 从记忆里沿用了 R5 的绕过方式。好处是经验能沿用，坏处是过时的经验也会被带入。owner 决定不隔离：worker 继续继承本机 agent 的配置和记忆。
6. 第二次实现里，worker 的 `make check` 看起来用了约 15 分钟。查 traex 的会话记录：命令本身 43 秒就结束了，之后 worker 向模型请求下一步，这一次模型响应用了约 15 分钟。慢在模型服务，不在 tend 或命令。
7. 故障演练只在本地预验收里做过（停服务端、两个写入者等），真机上没有专门演练 runner 中途重启这类场景。已在真机上演练，结论如下（R8、R9 修复后部署）：
   - **服务端重启，worker 正在运行**：worker 继续工作；服务端不可达期间，runner 把最终报告留在 journal，重连后提交；任务验收通过。
   - **服务端重启，runner 空闲等待领取**：修复前领取请求一直挂住（R8）；修复后请求超时、重发，runner 重连并正常领取之后创建的任务。
   - **SIGTERM 停 runner，验收 run 正在运行**：runner 和 worker 进程都退出。重启后 runner 向 tend 报告它持有的 run，tend 让它继续，runner 重新启动验收 worker，run 日志的序号接续；验收通过。
   - **SIGKILL 停 runner，实现 run 正在运行**：worker 的整个进程树随之退出，没有残留进程。重启后实现 run 在中断时的工作区上重新开始，提交 artifact，验收通过。
   - 观察：tsnet 节点刚启动时，第一次连接常在约 15 秒后超时，重试即成功，因此 runner 重启后约 30–45 秒才恢复工作。这是 tailnet 建立路径的耗时，不处理。
   - 观察：中断后重新开始的 run，worker 会重新收到 brief、从头执行，工作区保留中断时的改动，中断前的部分工作会重做。目前可以接受。
8. Claude Code 在同一个进程里 `/clear` 之后，MCP 调用仍然带旧的 session ID，新会话无法绑定（C15 已记录的风险）。这轮没有使用 Claude Code。
