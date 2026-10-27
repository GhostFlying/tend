# D111 原生后台 Agent 能力验证

状态：原生 ctx 和 compact 已在临时 thread 验证。按 owner 的后续指示，后台暂时保留原生工具并使用 auto mode，已移除严格原生白名单的启用门槛；完整后台验收仍未完成。

## 验证范围

运行时版本：traex 0.211.1。测试使用临时工作目录；工具清单探测使用本地模拟模型服务，不向外部模型提交实际工作项、owner 对话或附件。上下文和 compact 测试使用真实模型的空白临时 thread，不能据此声称完整后台权限或故障恢复已经验收。

1. `TestIsolatedContextUsageAndCompact` 收到原生 `ContextUsageSnapshot`，包含同一 revision 的 active context tokens 和 effective window；调用 `thread/compact/start` 后确实观察到 `contextCompaction` 完成事件。
2. `TestIntegrationNativeBackgroundAllowlistProbe` 检查实际模型请求而非仅检查配置对象。主会话的执行类 feature 关闭、显式选择 codex harness 和 direct tool mode、关闭继承 MCP 与 view image 后，start 和 resume 的请求仍暴露 `apply_patch`、`view_image`、`request_user_input` 和子 agent 工具。动态 Tend 工具列表不是原生工具白名单。
3. `TestIntegrationBackgroundAutoMode` 使用真实 app-server 与本地模拟模型，启动 turn、完成动态 Tend 工具调用后关闭进程，再恢复同一 thread 并再次完成调用。创建和恢复的原生响应均确认 `workspace-write + on-request + auto_review`，即使临时用户配置为 `bypass_permissions` 也被显式覆盖。原生返回的 `guardian_subagent` 是 `auto_review` 的别名。此测试验证权限协商及动态工具通路，不代表完整调度、真实模型推理或审批器批准/拒绝路径已验收。

## 重现

后台 auto mode 创建与恢复：

```sh
go test -tags integration ./internal/mainthread \
  -run '^TestIntegrationBackgroundAutoMode$' -count=1
```

历史严格白名单诊断（当前运行时预期失败，不再作为启用门槛；只作用于测试的临时本地模型端点）：

```sh
TEND_PROBE_NATIVE_TOOL_ALLOWLIST=1 go test -tags integration ./internal/mainthread \
  -run '^TestIntegrationNativeBackgroundAllowlistProbe$' -count=1
```

真实临时 thread 的 ctx / compact：

```sh
go test -tags integration ./internal/harness/traex \
  -run '^TestIsolatedContextUsageAndCompact$' -count=1 -v
```

## 当前权限边界与剩余验证

后台创建和恢复显式传入 sandbox、approval policy 和 approvals reviewer，不同时设置 `config.permission_mode`；当前原生版本拒绝同时指定 permission mode 与 sandbox override。每个后台逻辑会话使用独立工作目录，主 dispatcher 与 worker 的现有权限不变。

Tend 继续限制后台动态工具的身份、工作项范围与业务写权限。原生工具受运行时 auto mode 管理，可能读取文件、执行命令或申请升级；不能宣称它们具备工作项级数据隔离。Tend 不自动批准意外的交互审批请求，也不降级为 yolo。尚需验证审批器实际决策，以及真实后台并发、重启和关闭/重开全流程。

已保存在 rollout 中的 compact 完成事件现在可用于恢复，并与旧基线失效一起提交；compact 还需独立补齐未采集事件时的原生历史对账、unknown 的完整确认路径、与交接互斥的故障覆盖和自动 compact 后继续采样前的关键事实恢复。当前成功样例不是全部恢复场景的证明。

## 临时权限调整复审

独立复审未发现本次权限调整的可证实缺陷，并单独通过后台权限配置、独立工作目录、原生权限读取、意外交互审批拒绝和 Tend 工具写入限制测试。真实 app-server 创建/恢复测试另外在 race 模式通过；本地 Windows amd64 / macOS arm64 命令入口交叉编译通过。本次调整后的完整 `make check` 通过。以上结论不替代完整 D111 验收或 PR CI。
