# 阶段 C 预验收的后续修改（C12–C15）

**依据：`docs/findings/phase-c-acceptance.md` 的未决问题 2–6 与 owner 的取舍。**

owner 的使用方式是一个 agent 的长期会话作为控制入口，不考虑跨 agent 的场景。因此未决问题 3（快照附上最近结束的任务）不做：长期会话通过增量就能得知任务结束。

## C12 runner 的连接状态日志

runner 连不上服务端时，每次失败都记一条 warn，恢复后没有任何记录，看日志无法判断是否已经恢复。

- `client` 记录连接状态：第一次传输失败时记一条 warn（tend unreachable），之后的失败不再重复；恢复后第一次收到响应时记一条 info（reconnected），带上断开的时长。
- 只要收到了服务端的响应，包括业务错误，都算连上了。

## C13 工作区说明里写清基线

worker 的工作区只检出任务分支，基线分支只在 `origin/` 下，没有本地的 `main`。预验收里按 `main` 写的验收命令失败了，验收 worker 只能自己猜。

- 不预设验收条件的写法，验收条件由主 Agent 按任务决定；tend 只把事实交给 worker。
- 工作区根目录的 `AGENTS.md` 对每个挂载写明：基线的完整 commit、它来自哪里（例如 `origin/main` 的末端、指定的 commit），并说明工作区里没有本地的基线分支，要和基线比较时用这个 commit。

## C14 增量写明变化的来源（D55）

增量里的一行只有对象和事件，看不出是 owner、另一个会话还是 tend 自己造成的，agent 可能把 owner 有意的操作当成意外。

- 事件写入时记下发起者：迁移给 `events` 增加 `actor_json`，`app` 在一个请求里写入的事件记下请求的 principal；没有请求的事件（runner 上报的状态变化、租约过期等）不记。
- 增量里每行带上来源：`by owner`、`by <agent> session <id 前缀>`、`by the hosted main agent`；runner、run、system 和没有记录的统一写 `by tend`。
- 会话自己的写入仍然不出现在它的增量里（D47）。

## C15 只处理用过 tend 工具的会话（D54，修订 D47）

Codex 的记忆整理会另起后台会话，它同样触发 hook，tend 给它发了完整快照，账本内容可能被写进 Codex 的长期记忆。更一般地，agent 的子会话、后台会话和与 tend 无关的会话都不应该收到 tend 的上下文。

- **绑定**：会话第一次通过 MCP 调用 tend 的工具（带有它的 session ID）时，tend 把它记为已绑定。绑定是幂等的记录，不产生领域事件。
- **hook**：未绑定会话的 hook 什么都不返回，tend 也不记录它（不建基线、不写 packet、不留请求记录）。已绑定的会话按 D47 处理：SessionStart 返回完整快照，UserPromptSubmit 返回增量；绑定后第一次 hook 没有基线，返回完整快照。绑定时记下当时的账本位置作为水位；完整快照附上水位之后结束的任务和被回答的待决事项，避免会话建了任务、任务在下一次输入前结束时，结论随完整快照一起被跳过（压缩后的完整快照同理）。
- **新会话怎么知道 tend**：MCP 的工具描述，以及 `initialize` 里的 `instructions`，提示在 owner 要把工作交给 tend 或问到 tend 的任务时使用这些工具，并先调用 `tend_context`。`tend_context` 返回完整快照和外挂说明。
- **已有会话**：迁移把已有的会话记为已绑定，正在使用的控制会话不会因为升级而收不到上下文。
- 风险：Claude Code 的 MCP 进程从环境变量取 session ID。如果会话在同一个进程里换了 ID（例如 `/clear`），之后的工具调用仍记在旧 ID 上，新 ID 无法绑定，也就收不到上下文。长期会话一般不这样用；遇到时再考虑在 hook 侧补充绑定。

## 提交拆分

| 步骤 | commit | 内容 |
| --- | --- | --- |
| 1 | `fix: log when the runner loses and regains tend` | C12，同时提交本计划 |
| 2 | `feat: state each mount's base in the workspace notes` | C13 |
| 3 | `feat: name who made each change in deltas` | C14，D55 |
| 4 | `feat: give context only to sessions that use tend` | C15，D54 |
