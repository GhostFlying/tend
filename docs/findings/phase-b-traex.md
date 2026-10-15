# 阶段 B traex 接入结论

**环境：traex 0.209.1，模型 GPT-5.6-Sol，dev container。**
**复现：`go test -tags integration ./internal/harness/traex/`（`make it` 包含它）。**

## 动态工具在 resume 之后仍然有效

- `thread/resume` 的参数里没有 `dynamicTools`。实测：在一个 app-server 进程里用 `thread/start` 注册工具，关闭进程，再在新进程里 `thread/resume`，模型仍然能调用该工具，调用照常以 `item/tool/call` 到达新的连接。
- **影响**：主会话恢复时不需要重新注册工具；工具定义以 thread 创建时为准。如果以后要改工具集合，需要新建 thread（或确认 traex 是否支持更新），这一点在主会话管理器（B12a）里处理。

## 协议能力（来自 generate-json-schema）

- `turn/start` 支持 `userMessageId`。主会话在 `turn/start` 结果不确定时，可以用它在 thread 历史里找到这条用户消息，作为对账依据（B12a 的 Reconcile）。
- `turn/start` 和 `thread/start` 支持 `runtimeWorkspaceRoots`、`environments`。D37 的 AGENTS.md 加载问题是否能用它们原生解决，留到 worker 驱动（第 12 步）时实测。
