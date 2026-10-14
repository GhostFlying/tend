# 阶段 A spike 结论

**环境：traex 0.209.1，模型 GPT-5.6-Sol，dev container。**
**复现：`go run ./spikes/appserver`（见 `spikes/appserver/README.md`）。输出写入 `spikes/appserver/out/`（不提交），包括完整的收发 transcript 和结果 JSON。**

结论的证据来自两处：app-server 的通知和响应，以及 thread rollout 文件里的 `history_mutation` 记录。模型自己的回答只作为辅助参考。

## A1 app-server 生命周期

- 传输方式是 stdio 上逐行传输的 JSON，消息没有 `jsonrpc` 字段。请求格式为 `{id, method, params, trace?}`，其中 `trace` 是可选的 W3C Trace Context，以后可以用来传 trace_id。
- 握手：先发 `initialize`（`clientInfo` 和 `capabilities.experimentalApi=true`），再发 `initialized` 通知。
- `thread/start` 的响应里有 thread id、rollout 路径（`thread.path`）、实际使用的模型，以及 `instructionSources`（加载了哪些 AGENTS.md）。
- `turn/start` 立即返回 turn id；之后依次收到 `turn/started`、`item/started`、`item/completed`、`turn/completed` 等通知，item 都带 threadId 和 turnId。
- **基础上下文约 12.9k token**：包括 developerInstructions、插件说明、multi-agent 说明，以及 cwd 所在仓库的 AGENTS.md。后续 turn 的缓存命中约 12.8k。
- **影响**：主 Agent 的 cwd 必须是一个专用目录，不能放在任何带 AGENTS.md 的仓库里，否则会把无关的项目说明带进上下文。插件说明能否关闭，留给 A5。

## A2 `thread/inject_items`

| 验证项 | 结果 |
| --- | --- |
| turn 之间注入 developer 消息 | 成功。下一轮模型准确答出了注入的暗号。rollout 里这条记录标记为 `client_authored: true`，挂在一个伪 turn id（`auto-compact-N`）下。 |
| turn 进行中注入 | 成功，不报错。这条消息插在当前 turn 的用户消息之后、模型回复之前，下一轮能看到新值。当前 turn 内后续的采样（比如工具调用之后）理论上也能看到，但这次 turn 只有一次采样，没法验证。 |
| 重复注入相同内容 | **会追加两份**，app-server 不做任何去重。 |

**影响**：
- 注入只依赖协议本身，hook 不再是主路径（D17 成立）。
- 去重完全由 tend 负责：同一个 packet 只注入一次，重试前先确认上一次是否已经成功（对应 v0.1 §7.4）。注入请求没有幂等键，结果不确定时，宁可再补一份带版本标识的小快照，也不要重放旧的 packet。
- turn 进行中注入可以作为状态变化的快速通道，但不能代替服务端的版本校验。

## A3 Compaction

- `thread/compact/start` 会执行一个独立的压缩 turn，通知顺序是：`turn/started` → `item/started` 和 `item/completed`（类型为 `contextCompaction`）→ `warning`（提示线程过长）→ `turn/completed`。**traex 这个版本不会发 `thread/compacted` 通知**；可靠的信号是 `contextCompaction` 这个 item。
- rollout 里的记录是一次 `replace` 操作：历史被替换成“保留的用户消息 + 压缩续接说明 + 结构化摘要”。**之前注入的 developer 消息作为原始条目全部被丢掉**，其中的事实只能靠摘要保留。这次暗号碰巧被摘要保住了，但不能依赖这一点。
- 压缩之后的下一个 turn，app-server 会自动重新加入 developerInstructions 和 AGENTS.md。
- **影响**：
  - 看到 `contextCompaction` item 就让基线失效，在下一轮之前重新注入完整快照（D18 成立）。
  - 稳定的角色约束放在 `developerInstructions` 里，它在压缩后会自动恢复，不需要 tend 重复注入。
- 还没验证：`postCompact` 和 `sessionStart` hook。注入既然已经不依赖 hook，这一项的优先级降低了。

## A4 主 Agent 工具：动态工具（dynamic tools）

- `thread/start` 有一个实验性参数 `dynamicTools`（需要 `experimentalApi`），用来注册由客户端实现的工具。模型调用工具时，app-server 向客户端发出 server request `item/tool/call`，参数是 `{threadId, turnId, callId, namespace, tool, arguments}`；客户端返回 `{success, contentItems}`。
- 实测工具调用成功。模型是通过 code mode 间接调用的，所以 callId 是一个复合字符串（例如 `code-mode-nested:<n>:call_<id>:exec-<uuid>`），作为不透明字符串使用即可。
- **影响（D20 已据此确认）**：主 Agent 的工具用动态工具实现，不再单独起 MCP server。
  - 调用身份天然由 thread、turn 和 callId 组成，直接推导 request_id：`harness:<instance>:<threadId>:<turnId>:<callId>`。
  - 不用给每个 thread 分配 MCP 地址和 token，也省掉一个 listener。
  - 工具调用和主会话走同一条 stdio 连接，tend 是唯一的执行方。
  - 动态工具在 Codex 中已经存在很长时间，虽然标记为实验性，但不作为风险项。升级 traex 时用这个 spike 做回归。
- 还没验证：工具执行期间 tend 进程崩溃、app-server 重连之后，这次调用会怎么处理（结果丢失还是重新发起）。B12 需要专门做故障测试。

## A6 重启与恢复

- 关闭 app-server 进程，重新启动并握手之后，`thread/resume {threadId}` 成功，历史完整（包括压缩后的状态），模型答对了暗号。
- **影响**：app-server 重启本身不会让基线失效，只要 resume 成功、并且期间没有发生 compaction。tend 重启后仍然要按“无法确认就重建”的原则：如果水位记录和 thread 的状态对不上，就注入一份完整快照。

## 未完成项

| 项 | 状态 |
| --- | --- |
| A3 hook | 推迟，注入不依赖 hook |
| A5 主 Agent 隔离 | 待做：插件和 skills 的关闭、只读 sandbox 下禁用 shell |
| A7 worker 形态 | 待做 |
| A8 协议绑定 | 待做：用 `generate-json-schema --experimental` 生成 Go 类型，或手写所需的子集 |
| A9 tsnet | 待做 |
