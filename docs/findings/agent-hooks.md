# C5 spike 结论：三个 agent 的 hook 与 MCP

**日期：2026-10-16**
**环境：Claude Code 2.1.301（spike 期间从 2.1.299 自动升级），codex-cli 0.162.1，traex 0.209.1；宿主机直接运行。**
**复现：`spikes/agenthooks`（见其 README）。输出写入 `spikes/agenthooks/out/`（不提交）。**

证据来自三处：probe 记录的 hook 输入、环境和 MCP 消息；agent 自己的会话记录（Claude Code 的 transcript、Codex 和 traex 的 rollout）；模型复述的暗号。模型的回答只作辅助判断：Codex 的模型会把“列出上下文里的行”当作泄露隐藏指令而拒绝，改问“你收到了哪些暗号”之后才正常回答。

## 结论摘要

三个 agent 都满足 D45 的接入要求，支持列表保持 Claude Code、Codex、traex。对计划的影响见最后一节。

| 项 | Claude Code | Codex | traex |
| --- | --- | --- | --- |
| SessionStart 来源 | startup、resume、compact（文档还有 clear） | startup、resume、compact（源码还有 clear、fork） | 同 Codex |
| UserPromptSubmit 输入 | `session_id`、`prompt`、`prompt_id` | `session_id`、`turn_id`、`prompt` | 同 Codex，另有 `event_type` 和嵌套的 `user_prompt_submit` |
| 附加上下文模型可见 | 是 | 是 | 是 |
| 输出格式 | `hookSpecificOutput.additionalContext` 的 JSON，或纯文本 | 同左 | 同左 |
| 超时 | 放行，不注入 | 放行，不注入 | 放行，不注入 |
| 退出码 1 | 放行，不注入 | 放行，不注入 | 放行，不注入 |
| 退出码 2 | **拦下用户输入** | **拦下用户输入** | **拦下用户输入** |
| 输出不是合法 JSON | 原文当作上下文注入 | 忽略 | 未测（与 Codex 同源） |
| 上下文长度上限 | 约 1 万字符，超出后存成文件，只注入约 2KB 预览 | 默认 2,500 token，超出后截断（保留头尾）并存成文件；每个 hook 可用 `additionalContextLimit` 调整，`0` 关闭 | 同 Codex |
| hook 继承环境变量 | 是 | 是 | 是 |
| MCP 子进程继承环境变量 | 是 | **否**，需要 `env_vars` 转发 | **否**，需要 `env_vars` 转发 |
| MCP 调用能否得知会话 | 进程环境里有 `CLAUDE_CODE_SESSION_ID`；`tools/call` 的 `_meta` 带 `claudecode/toolUseId` | 每次 `tools/call` 的 `_meta.x-codex-turn-metadata` 带 `session_id`、`thread_id`、`turn_id` | 同 Codex |
| hook 需要信任 | 否（写在 settings 里即生效） | **是**，未信任的 hook 不运行 | **是** |
| MCP 工具调用的审批 | 交互模式下询问；可以在 permissions 里放行 | 默认询问；无人值守时直接拒绝；`default_tools_approval_mode = "approve"` 放行 | 同 Codex |

## 1. 配置

- **Claude Code**：hook 写在 settings 的 `hooks` 里（用户级 `~/.claude/settings.json`，或用 `--settings` 传入）。MCP 用 `claude mcp add` 或 `--mcp-config` 配置。工具名是 `mcp__<server>__<tool>`，交互模式下需要在 permissions 里放行，否则每次询问。
- **Codex / traex**：hook 写在 `config.toml` 的 `hooks.<Event>` 里，或者配置目录下的 `hooks.json`；MCP 是 `[mcp_servers.<name>]`。两者都可以用 `-c` 临时覆盖。hooks 功能默认开启（`features.hooks` 为 stable）。
  - **更正（阶段 C 预验收，traex 0.210.1）**：traex 不读配置目录下的 `hooks.json`，只读 `traecli.toml` 的 `[hooks]` 表；`hooks.json` 只对 Codex 成立。spike 当时对 traex 只用 `-c` 验证过，`hooks.json` 这一条是从 Codex 源码推断的。
- **hook 信任（Codex / traex）**：
  - 新出现的 hook 是 untrusted，不会运行。TUI 启动时会列出新 hook，选择“全部信任并继续”后，信任记录写进配置的 `hooks.state."<key>".trusted_hash`。
  - hook 的命令变了，hash 跟着变，需要重新信任。
  - 非交互的 `exec` 和 app-server 不会询问，只能事先信任，或者用 `--dangerously-bypass-hook-trust`（app-server 在 thread 配置里传 `bypass_hook_trust`）。spike 用的是后者。

## 2. SessionStart

- 三者在新会话、恢复会话、压缩之后都会触发，`source` 分别是 `startup`、`resume`、`compact`，模型都能看到返回的上下文。
- Claude Code 的 print 模式支持真正的 `/compact`。Codex 和 traex 的 `exec` 把 `/compact` 当作普通文字发给模型，不会压缩；压缩用 app-server 的 `thread/compact/start` 验证。
- **压缩摘要会带上旧的注入内容**：三者压缩之后，模型仍然能复述压缩前注入的暗号，因为摘要把它们写了进去。因此，压缩之后的完整快照必须明确说明它取代之前所有的 tend 上下文，否则模型可能同时看到新旧两份账本状态。
- 非交互模式下，每次 `--resume` 或 `exec resume` 都会触发一次 `resume` 来源的 SessionStart。

## 3. UserPromptSubmit

- 输入都带 `session_id` 和用户输入的原文。
- 返回的上下文放在本轮用户输入**之后**：Codex 和 traex 是一条 developer 消息，Claude Code 是一条附加内容。
- 用户输入是 slash 命令（如 Claude Code 的 `/compact`）时不触发。

## 4. 失败与超时

- 超时（hook 配置的 `timeout`，单位秒）和退出码 1：三者都放行用户输入，不注入任何内容。
- **退出码 2：三者都拦下用户输入**，并把 stderr 显示给用户。`tend hook` 任何情况下都不能以 2 退出。
- 输出不是合法 JSON：Claude Code 把原文当作上下文注入，Codex 忽略。`tend hook` 只输出合法的 JSON。

## 5. MCP

- 握手是标准的 `initialize`、`notifications/initialized`、`tools/list`。Claude Code 还会先发一个 `server/discover`，probe 返回 method not found 后不影响使用。
- **环境变量**：
  - Claude Code 的 MCP 子进程继承完整环境；
  - Codex 和 traex 默认不传任何 agent 进程的环境变量，`env_vars = ["TEND_ADDON"]` 之后才转发。在 traex 的 app-server 上（也就是 worker 的进程链）验证过。
- **会话归属**：三者都能确定一次工具调用属于哪个会话。
  - Claude Code 的 MCP 进程随会话启动，环境变量 `CLAUDE_CODE_SESSION_ID` 就是 hook 输入里的 `session_id`；
  - Codex 和 traex 的每次 `tools/call` 都在 `_meta` 里带上 `session_id` 和 `turn_id`，与 hook 输入一致。
- **审批**：Codex 和 traex 默认要求批准 MCP 工具调用，无人值守（approval never）时直接拒绝；配置 `default_tools_approval_mode = "approve"` 后放行。

## 6. 对计划的影响

1. **D45 成立**，三个 agent 都保留在支持列表里。
2. **`tend hook` 的输出**：三者共用同一种 JSON（`hookSpecificOutput.hookEventName` 加 `additionalContext`）。
   - 不需要按 agent 区分格式；
   - 任何错误都以 0 退出，不输出或者只输出一行说明；
   - 绝不使用退出码 2。
3. **上下文预算要缩小**：
   - 原计划的完整快照 16000 字符超过了 Claude Code 的约 1 万字符，也超过了 Codex 和 traex 默认的 2,500 token。
   - 改为完整快照最多约 8000 字符，增量最多约 4000 字符。
   - Codex 和 traex 的 hook 配置设 `additionalContextLimit = 0`，避免中文内容按 token 计算时意外超限；预算本身由 tend 控制。
4. **压缩后的完整快照**：开头写明“取代之前所有的 tend 上下文”，并带上 epoch 和水位。
5. **`tend setup` 要处理的配置**：
   - Claude Code：settings 的 SessionStart 和 UserPromptSubmit hook；`claude mcp add` 添加 `tend mcp --agent claude`；permissions 放行 `mcp__tend__*`。
   - Codex / traex：`[mcp_servers.tend]`，加上 `env_vars = ["TEND_ADDON"]` 和 `default_tools_approval_mode = "approve"`；两个 hook 带 `additionalContextLimit = 0`；提示用户首次启动 TUI 时信任这两个 hook。
   - hook 命令保持稳定（例如 `tend hook codex session-start`，不带版本号或临时路径），否则每次升级都要重新信任。
6. **会话归属可以确定**：工具调用都能带上会话 ID。因此增量里过滤掉本会话自己的写入（owner 于 2026-10-16 决定，规则见计划 §4.3）。
7. **worker 不接入（`TEND_ADDON=off`）**：
   - hook 在三者中都能看到这个变量；
   - MCP 在 Codex 和 traex 中需要 `env_vars` 转发，这正是 `tend setup` 必须写入的配置。
