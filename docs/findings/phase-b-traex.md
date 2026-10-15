# 阶段 B traex 接入结论

**环境：traex 0.209.1，模型 GPT-5.6-Sol，dev container。**
**复现：`go test -tags integration ./internal/harness/traex/`（`make it` 包含它）。**

## 动态工具在 resume 之后仍然有效

- `thread/resume` 的参数里没有 `dynamicTools`。实测：在一个 app-server 进程里用 `thread/start` 注册工具，关闭进程，再在新进程里 `thread/resume`，模型仍然能调用该工具，调用照常以 `item/tool/call` 到达新的连接。
- **影响**：主会话恢复时不需要重新注册工具；工具定义以 thread 创建时为准。如果以后要改工具集合，需要新建 thread（或确认 traex 是否支持更新），这一点在主会话管理器（B12a）里处理。

## 协议能力（来自 generate-json-schema）

- `turn/start` 支持 `userMessageId`。主会话在 `turn/start` 结果不确定时，可以用它在 thread 历史里找到这条用户消息，作为对账依据（B12a 的 Reconcile）。
- `turn/start` 和 `thread/start` 支持 `runtimeWorkspaceRoots`、`environments`。D37 的 AGENTS.md 加载问题是否能用它们原生解决，留到 worker 驱动（第 12 步）时实测。

## 工具调用期间 tend 崩溃（计划 §8.5）

**复现：`go test -tags integration -run TestIntegration_ToolCallCrash ./internal/harness/traex/`。**

做法：模型调用动态工具后，tend 不回答，直接关闭 app-server（关闭 stdin 后结束进程组，和 tend 进程退出时 app-server 看到的一样）；在新进程里 `thread/resume`，观察 90 秒，再开一个新 turn。

- **调用不会重发，也不会挂起，而是以失败结束。** 恢复后的历史里，这个 turn 的状态是 `interrupted`，工具调用 item 是 `dynamicToolCall`、`status=failed`、`success=false`，错误为 “dynamic tool call was cancelled before receiving a response”。恢复后的 90 秒里，新连接上没有任何事件，也没有重新收到这次调用；thread 状态是 `idle`。
- **模型把它当作失败。** 在下一个 turn 里，模型说上一次调用 “was aborted and returned no result”，并按要求发起了新的调用（新的 call ID），新调用正常完成。
- **call ID 不一定是简单的 `call_…`**：模型通过 code mode 调用工具时，ID 形如 `code-mode-nested:<n>:call_…:exec-…`。D28 用 thread、turn 和 call ID 共同标识一次调用，不依赖 ID 的格式。

**影响：**

- 工具副作用可能已经提交，而模型只看到失败。模型之后重试时用的是新的 call ID，D28 的原生 ID 映射认不出这是重复，只能靠业务去重。规划类写入带来源消息和 `commit_seq`，同一条消息里的重试会被识别；但 owner 换一条消息再提同样的要求时，来源不同，去重识别不了。模型也拿不到被中断调用的 request ID，无法用 `requests_read` 查询。所以主 Agent 的说明里要求：没有收到回答的工具调用可能已经生效，再做同样的事之前先读账本确认。
- tend 重启后，崩溃时还在运行的 turn 在账本里不能一直停在 `started`。现在主会话恢复 thread 之前，先把这个 thread 上仍处于 `started` 的 turn 标为 `interrupted`，和 harness 的记录一致。这条消息的投递仍然算已送达，不会自动重发：它可能已经产生了部分副作用，是否再处理由 owner 决定（`TestFault_TurnRunningAtCrash`）。
- 没有验证 app-server 自己被强杀（例如 OOM）的情况，这时它可能来不及写下失败记录。tend 的处理不依赖这条记录：turn 在账本里的状态由 tend 自己收敛，副作用靠去重保护。

## 工作区根目录的指令文件（D37）

**复现：`go test -tags integration -run TestIntegration_WorkspaceInstructions ./internal/harness/traex/`。**

工作区根目录不是 git 仓库，根目录有 `AGENTS.md`，子目录是一个 git 仓库，也有自己的 `AGENTS.md`。

- 以根目录为 cwd 时，`instructionSources` 里只有根目录的 `AGENTS.md`，模型只知道根目录文件里的内容。dev container 里没有用户级的 AGENTS.md；在宿主机上它会作为另一个来源出现。
- `thread/start` 接受 `runtimeWorkspaceRoots`（实验 API），但传入根目录和仓库目录之后，`instructionSources` 不变，仓库的 `AGENTS.md` 仍然没有加载。
- **结论**：traex 0.209.1 不能原生加载各仓库的 AGENTS.md，D37 的做法（在根目录生成 `AGENTS.md`，要求 worker 进入仓库前先读该仓库的 AGENTS.md）保持不变。
