# 运行中 worker 的补充消息

## 调查结论（编码前）

- 主代理工具在 `internal/tools` 同时注册到托管会话与外挂 MCP，写入经 `app.Execute` 请求去重、身份校验和单事务审计。
- runner 使用 run 租约凭据、HTTP RPC、持久 journal 和独立事件上传。worker owner question 已有可重读的回答通路，不结束 run；本功能不改变它。
- `tasks_update_intent` 会改变正式 spec 并收束旧执行，不能作为补充消息通路。
- TraeX app-server 当前原生 `turn/steer` 接受 `threadId`、`expectedTurnId` 与文本，返回 `turnId`；前置条件可拒绝结束竞争，不需要 interrupt/start/resume。
- Orca 的 `terminal send` 是终端输入，可能排队或在空闲后开启 turn。公开接口缺少指定活动 turn 的原子前置条件，不能据此保证本功能。禁止用 Escape、Ctrl-C、续开 turn 或重建模拟。
- 原生接受只证明执行器接受，不能声明模型已读取。未知结果必须保留且不能重复投递。原生连接的错误原文可能含敏感信息，公开失败使用固定原因码。

## 实施计划

1. **M1**：新增独立持久消息及投递历史，冻结 task/run/action/spec/generation/control revision、认证主代理来源和请求身份。提供主代理 `runs_send_message` 与 `runs_read_messages`，不修改 spec、输入、decision、验收或控制模式。
2. **M2**：以能力协商增加可选 assignment 开关与 run 凭据的待投递读取、投递领取、结果报告。领取与状态变更在服务端事务串行化；只领取最早消息。领取后的结果未知不自动重发、不投给后续 run。
3. **M3**：runner 在独立消息循环中持久化原生调用意图与回执，调用可选 worker 接口。TraeX 固定原 thread/turn 调用 steer，结束、暂停、取消、takeover 和不支持明确返回结果；不中断原 worker。Orca TUI 通过原 run 专属 MCP 桥，在同一执行中的安全点调用 `receive_messages` 领取上下文，随后 `ack_message` 确认接收；返回工具文本不冒充原生接收。断线重试仅重试相同服务端请求，原生调用或新工具投递最多一次。
4. **M4**：覆盖身份、重复请求、顺序、过期、结束竞争、离线恢复、拒绝/未知与 decision 并存。增加 integration 标签的真实 TraeX 与 Orca TUI 路径：活动工具/执行过程中发送，原会话与 turn 连续，后续响应包含补充标记；验收可在独立环境复现。
5. **M5**：执行 `make check` 和必要独立审查，修复有效缺陷，核对任务起始基点与本地 Git 身份，提交代码与隔离证据报告。不推送、不建 PR、不合并、不部署、不操作既有任务。

## 兼容性与界限

这是能力协商后的新增消息通路，普通协议请求、旧 assignment 与旧运行语义不变；不提升 runner 协议。旧 runner/执行器明确不支持。消息正文只作为现有任务中的补充上下文，不代表 owner 决策或新增授权。本轮不实现完整 rollout/live 渲染器。

## Owner 判断后补充（TUI 编码前）

Owner 明确要求覆盖 Orca TUI，不接受仅支持 app-server。进一步调查确认原 run 专属 MCP 工具桥将工具结果放入原 agent 执行上下文，支持所有当前 TUI 的 Tend 工具，不依赖危险的终端输入。采用协作领取：brief 要求在开始、长操作后与结果提交前检查消息；主代理发送时可以仍在执行命令，消息保持排队直至安全点领取。没有有界的即时投递保证，agent 不检查就保持 queued。领取后的响应丢失不再返回消息正文；显式 ack 才报告 accepted，且不会替代正在等待的 owner decision。
