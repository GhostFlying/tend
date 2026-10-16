# 阶段 C 验收结论

计划 §8 的验收分两步：

- 先在一台开发机上做本地预验收，覆盖不依赖 tailnet 的部分（本节）；
- 剩下的项目和 owner 一起在真机上完成，见最后一节。

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
2. Codex 的 memories 子会话也会触发 SessionStart，每次都拿到完整快照，增加了不必要的请求和水位记录。
3. 完整快照只列未结束的任务。会话开始时 agent 看不到刚刚完成的任务，只能依赖增量。可以考虑附上最近结束的任务。
4. 增量不说明变化来自谁（owner、其他会话还是 tend 自己），agent 无法区分。
5. worker 的工作区只有 `origin/main`，没有本地的 `main` 分支。按 `main` 写的验收命令会失败，这一点应该写进 worker 的说明。
6. runner 重新连上服务端时没有日志（小问题）。

## 待真机验收

以下项目需要真实部署和 owner 的设备，和 owner 一起完成：

- 服务端和 runner 加入 tailnet：https 证书、只有 http 时的回退、ACL；
- 在 Codex 和 traex 的 TUI 里第一次启动时信任 tend 的 hook；
- 在 owner 自己的 agent 配置上运行 `tend setup`；
- 用一个真实仓库跑一个真实需求。
