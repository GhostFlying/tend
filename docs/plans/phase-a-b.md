# tend 实施计划：阶段 A（harness spike）与阶段 B（持久控制骨架）

**日期：2026-10-14**
**依据：`docs/design.md`（v0.2）、`docs/design-v0.1.md`，约定见 `AGENTS.md`。**

## 阶段 A：traex harness spike

目标：用 traex 0.209.1 的实际行为确认 design.md 中标为“待验证”的事项。产出 `docs/findings/spike-a.md`，每项结论写明：结论、复现脚本、traex 版本、限制。spike 脚本放在 `spikes/`，不进入正式包。

traex 使用 tend 专用的 home：宿主机目录 `~/.local/share/tend/traex-home`，挂载到容器内相同的绝对路径，并设置 `TRAE_HOME`。工作目录也挂载到相同路径，保证 state db 里记录的绝对路径与宿主机一致。traex 通过 SSH 完成鉴权：宿主机的 `~/.ssh` 以只读方式挂载到 `~/.ssh-host`，`postStartCommand` 每次启动时复制一份可写的到 `~/.ssh`（traex 鉴权时需要在 `~/.ssh` 下创建临时文件）。tend 自己的 hook 等配置通过 `-c` 参数或 thread 级 config 传入。

测试默认使用 `GPT-5.6-Sol`（环境变量 `TEND_TRAEX_MODEL`），避免 GPT-6.1 排队影响测试。

已知：用专用 home 后，一句话 prompt 消耗约 10k token；共享 `~/.trae` 时约 21k，多出的部分来自用户全局的 skills 和 plugins。A5 还要确认能否进一步裁掉内置 skills，只保留 tend 需要的工具。

| # | 验证项 | 通过标准 |
| --- | --- | --- |
| A-1 | dev container：Go、Node、pnpm 可用；traex 只读挂载后能在容器内运行 | **已完成**：Go 1.27.2、Node 24.21、pnpm 10.34；容器内用 GPT-5.6-Sol 跑 `traex exec` 返回正常，记录的路径与宿主机一致 |
| A0 | 鉴权与 home：专用 `TRAE_HOME`，通过 SSH 鉴权 | **已完成**：首次运行时自动完成 headless 鉴权，凭据保存在专用 home 中 |
| A1 | app-server stdio 生命周期：initialize → thread/start → turn/start → 流式通知；记录 thread_id、turn_id、item id 的稳定性 | **已完成**，见 `docs/findings/spike-a.md` |
| A2 | `thread/inject_items`：在 turn 之间注入 developer 消息，再用一个“暗号”问题确认模型能看到；测试 turn 进行中注入的行为；测试重复注入的效果 | **已完成**：可以注入 developer 消息；turn 进行中也可以注入；重复注入会追加两份 |
| A3 | Compaction：用 `thread/compact/start` 触发压缩，观察 `contextCompaction` item 和 `postCompact`、`sessionStart` hook 的输入和输出；确认注入内容压缩后是否保留 | **部分完成**：`contextCompaction` item 是可靠的失效信号，压缩会丢掉注入的原始条目；hook 推迟 |
| A4 | 主 Agent 工具的调用身份：原计划验证 MCP，实际改为验证动态工具 | **改为动态工具并已验证**：`item/tool/call` 带 threadId、turnId、callId |
| A5 | 主 Agent 隔离：只读 sandbox、`approvalPolicy=never`；确认能否禁用 shell、写文件工具、插件和 skills，只保留 tend 的动态工具 | **已完成**：shell、插件、skills、AGENTS.md 都已移除，只读 sandbox 拦住了写入；code mode 等由模型目录决定的工具关不掉；保留 subagent |
| A6 | 中断与恢复：turn/interrupt 的语义；app-server 进程重启后用 thread/resume 恢复，历史和注入内容是否还在 | **已完成**：重启后 resume 能恢复完整历史 |
| A7 | Worker 形态：在 worktree 中以 workspace-write 运行一个 thread，完成后取 diff 或 commit；确认审批请求如何自动处理 | **已完成**：worker 在 worktree 中完成改动并通过测试；runner 负责 commit；Docker 中需要放开 user namespace |
| A8 | 协议绑定：用 `generate-json-schema` 生成 Go 类型，或手写所需的子集，放入 `internal/harness/traex/generated/`，附带版本信息文件 | **已决定**：手写子集，用抽取的 schema 做一致性测试，B12 实现 |
| A9 | tsnet：在 dev container 中用 ephemeral key 启动节点，手机通过 tailnet 访问；WhoIs 能拿到用户和节点身份；检查 HTTPS 证书是否可用；runner 节点交叉编译到 windows/amd64 后能加入 tailnet | **已完成**：入网、WhoIs、HTTPS 都已验证；证书在首次握手时签发；交叉编译通过，非 Linux 实机待验证 |

阶段 A 结束后，根据结论修订 design.md 的 §2–§4，再进入阶段 B。

## 阶段 B：持久控制骨架

目标：在不依赖真实模型的情况下，跑通账本、幂等、outbox/inbox、runner 租约和调度，并让 v0.1 §17.2 中与 MVP 相关的故障场景测试全部通过。真实 traex 适配只在阶段 B 末尾接入。

### 仓库结构

见 `AGENTS.md` §1。阶段 B 实现其中除 `web/` 以外的部分。

工具链：Go 1.27（tailscale.com v1.104 要求 Go 1.27.1 以上）、标准库 `net/http`、`modernc.org/sqlite`、`tailscale.com/tsnet`、`github.com/modelcontextprotocol/go-sdk`。测试使用标准 `testing` 包。迁移是嵌入二进制的手写 SQL 文件。

### 工作项（按顺序）

1. **B1 协议包**：请求信封（request_id、parent_request_id、trace_id、operation、target、expected_control_revision、payload），payload_hash 规范化，统一错误码（`conflict_payload`、`stale_revision`、`unknown_outcome`、`forbidden` 等）。
2. **B2 数据库 schema 与迁移**：request_records、tasks、task_specs、plan_actions、runs、artifacts、acceptances、decisions、approvals、conversation_messages、conversation_bindings、events、outbox、inbox、context_packets、thread_baselines（水位）、hosts、leases。
3. **B3 请求去重中间件**：实现 v0.1 §8.4 的接收方规则表；按身份域与 request_id 唯一；处理中的请求返回同一句柄；重放结果时重新校验权限。
4. **B4 领域服务与状态机**：任务生命周期、控制模式、run 观测状态三者分离；`expected_control_revision` 校验；spec 版本化；不提供无条件的 completed 写入，completed 只能由“通过的验收与交付条件均满足”这一规则派生。
5. **B5 事件、outbox 与 inbox**：事件应用和后续动作派生放在同一事务；dispatcher 在事务提交后投递，child request_id 预先持久化。
6. **B6 调度器**：静态 host 配置（OS、标签、slot）；按优先级排队；依赖与授权检查；不做抢占。
7. **B7 runner 协议（服务端）**：hello、claim（长轮询）、heartbeat、events、complete；租约与 run 级 token；对账逻辑（runner 上报持有、服务端无记录的 run，以及反过来的情况）。
8. **B8 runner（客户端）**：journal、自我 fencing 计时、harness 驱动接口、内置 tsnet；先用 testkit 的 fake harness。CI 交叉编译 windows/amd64、darwin/arm64、linux/amd64。
9. **B9 验收流程**：实现 run 提交 artifact 后，由程序派生验收 action；验收 run 使用独立凭据，结论绑定 spec_version 与 artifact_id；迟到的结果按 v0.1 §9.4 处理。
10. **B10 工具面**：design.md §3 的第一批动态工具，request_id 按 A4 的结论推导。
11. **B11 Context builder**：完整快照与 delta 的生成、预算裁剪、水位推进与失效处理（按 A2、A3、A6 的结论实现）。
12. **B12 traex 适配接入**：主 thread 管理器（前台串行队列、注入、动态工具注册与回调）；runner 的 traex worker 驱动。

### 阶段 B 必须通过的测试（来自 v0.1 §17.2）

- 同一 request_id 的重试：消息只创建一条，派发只发生一次。
- 并发请求使用相同 ID、相同参数：只执行一次，另一方拿到同一个句柄。
- 相同 ID、不同参数：返回 `conflict_payload`。
- 子动作使用各自独立的 ID，重试时保留原 ID。
- 主 Agent 用新的 call_id 重复启动同一阶段：被 `(action_id, generation)` 唯一约束阻止。
- runner 已开始执行但 claim 回执丢失：重连 hello 后对账，不会产生第二个 run。
- 数据库提交后、outbox 投递前进程崩溃：重启后用原身份继续投递。
- 心跳丢失但 runner 仍在运行：标记为不可确认，不重新分配可写工作区；runner 超时后自我 fencing。
- 任务要求变更后才到达的旧验收通过：保存为证据，但不关闭新版本任务。
- 验收环境失败：结论为 blocked，不算 changes_required，也不算通过。
- 注入失败或 compaction 之后：基线失效，下一轮注入完整快照；水位不会错误推进。
- 后台事件不改变对话焦点：在 binding 测试中构造“讨论方案 → 收到 WPT 通知 → 用户说‘按第二个来’”的序列。

### 不在阶段 A/B 范围

Web UI（阶段 C），人工接管、多机器和断线 UX（阶段 D），导入、Orca、Electron（阶段 E）。
