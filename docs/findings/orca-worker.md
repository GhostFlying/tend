# orca 承载 TUI worker 的 spike 结论（D63）

问题：单仓库任务能否以 agent TUI 的形式运行在 orca 的 worktree 里，经 tend 交付结果，并让 tend 察觉有人接管。计划见 `spikes/orca-worker/README.md`。

**环境：** 一台已运行 orca server（headless，1.4.229）的 Linux 开发机，经 owner 同意在其中操作；一个没有远端的本地 scratch 仓库。traex 0.210.1、Codex 0.159.2，都使用 owner 的默认配置。spike 结束后删除了 worktree、分支、终端、scratch 仓库，以及 agent 配置里为这些路径加入的信任记录；orca 里的仓库登记只能在界面上移除，由 owner 处理。

## 结果

| 检查 | 结果 |
| --- | --- |
| 建 worktree | 可行。`orca worktree create --repo … --name … --json` 返回路径、`head`（即基点 sha）、分支（`<用户名>/<名字>`）和 `baseRef`，能映射成 tend 的工作区事实。 |
| 启动 agent | 可行。`orca agent launch --agent <id> --worktree … --prompt … --json` 以终端启动，返回终端 handle 和用于重放的 `operationId`；任务说明作为启动参数交给 agent。traex 在 orca 里的 id 是 `trae`（按 `traecli` 识别），不是 `traex`。 |
| 无人值守 | Codex：owner 的配置是 YOLO，没有任何确认，直接执行。traex：权限同样是 YOLO，但第一次进入目录时停在“是否信任这个目录”的确认上；orca 能识别这个状态（`terminal wait` 返回 `blockedReason: agent-trust-workspace`）。信任作用于仓库根目录。runner 需要事先在 agent 的配置里信任工作区，或者应答这个确认。 |
| 交付结果 | 可行。runner 在 worktree 里写入项目级 MCP 配置（Codex 的 `.codex/config.toml`），新启动的 agent 加载它并调用了工具，工具进程的工作目录是这个 worktree。run 的凭据可以经同一份配置的 `env` 或工作区里的文件传入。这份配置是 worktree 里的未跟踪文件，必须加入 `.git/info/exclude`，以免 worker 把它提交。 |
| 一轮结束 | Codex：`terminal wait --for tui-idle` 正常，约 6 秒判断出空闲。traex：agent 已经空闲，`tui-idle` 一直超时；但 `orca worktree ps` 的 agent 状态是 `done`，带最后一条提示和最后一条回复。traex 需要改用 agent 状态，或修复 orca 的 `tui-idle`（owner 的 fork 刚有相关修复，尚未部署到这台机器）。 |
| 程序化输入 | 可行。`terminal send --enter --wait-submit` 的回执有 `input_accepted` 和 `turn_started` 两个阶段，带可重放的请求 ID 和 orca 的轮次序号。 |
| 察觉接管 | 有依据，未完整验证：agent 状态里有最后一条提示，send 回执里有轮次序号；出现一轮不是 tend 发起的工作，就说明有人接手。具体规则在阶段 E 设计。 |
| 重启 | 未验证：重启 orca server 会打断这台机器上 owner 其他正在运行的终端，不在这次授权的范围内。 |

## 其他发现

- `orca agent hooks status` 在这台机器上报模块缺失（`Cannot find module './impl/format'`），是 orca 本身的问题，不影响上面的结果。
- 一个 worktree 里可以同时有多个 agent 终端；tend 要只认自己启动的那个 handle。
- orca 没有移除仓库的 CLI 或 RPC，只能在界面上移除。tend 用 orca 时只建 worktree、不登记新仓库，仓库由 owner 事先加入 orca。

## 对阶段 E 的影响

- worker 的启动、输入、空闲判断都有现成接口；需要 tend 自己做的是：工作区事实的映射、项目级 MCP 配置的写入和排除、agent 的信任处理、按 agent 选择空闲判断方式，以及接管的识别规则。
- traex 作为 TUI worker 的两个问题（信任确认、`tui-idle`）需要先解决或绕开；Codex 现在就能用。
- 重启后的续接要在 owner 安排的时间里验证。
