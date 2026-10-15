# tend：系统设计 v0.2（相对 v0.1 的调整）

**日期：2026-10-14**
**基线：《中心化 Agent 任务管理服务：系统设计 v0.1》（下称 v0.1，原文应放在 `docs/design-v0.1.md`）。**

本文只记录 v0.2 相对 v0.1 的新增决策和修订。没有提到的部分仍按 v0.1 执行，D01–D14 保持不变。
标“已确认”的是用户已同意的内容；标“待验证”的需要阶段 A spike 用实际二进制确认。

## 1. 新增决策

| 编号 | 决策 | 状态 |
| --- | --- | --- |
| D15 | 项目名 `tend`，使用独立仓库。 | 已确认 |
| D16 | 主 harness 先接 traex（Codex fork），实现与协议细节参考上游 Codex 源码，以 traex 实际生成的协议为准。 | 已确认 |
| D17 | 主会话上下文以 app-server 协议注入为主：由服务端在 turn 之间通过 `thread/inject_items` 写入 developer 消息。Hook 只覆盖我们无法发起的时机（compact 之后、harness 自行恢复）。 | 已确认，接口已验证（spike A2） |
| D18 | 不做每轮注入。只有基线失效或账本发生相关变化时才注入（见 §2）。 | 已确认 |
| D19 | 执行节点一律用 pull 模式的 runner：runner 主动连控制服务，领取任务、续租、上报。控制服务不主动连节点。 | 已确认 |
| D20 | 主 Agent 唯一的写入路径是 tend 提供的工具，通过 app-server 的动态工具（`dynamicTools` / `item/tool/call`）实现，经由主会话同一条 stdio 连接回调 tend。主 harness 使用只读 sandbox，看不到应用数据库和根凭据。 | 已确认 |
| D21 | 远程访问走 tailscale。服务只监听 tailnet 地址，应用层仍保留鉴权与角色（tailscale 身份不替代角色权限）。 | 已确认 |
| D22 | 技术栈：服务端和 runner 用 Go，单个 module；Web 用 TypeScript。SQLite 用 `modernc.org/sqlite`（纯 Go，便于交叉编译）。harness 适配是 Go 包，服务端与 runner 共用。领域与 API 类型以 Go 为准，导出 JSON Schema 后生成 TS 供 Web 使用。 | 已确认 |
| D23 | 服务端和 runner 都内置 `tsnet` 加入 tailnet，不依赖设备上安装或运行 tailscaled。 | 已确认 |
| D24 | Web 采用响应式设计，以移动端为一等场景，桌面端是同一套布局的展开。 | 已确认 |
| D25 | 日常开发在 dev container 中进行；最终客户端（Electron 等桌面外壳）和 Windows、macOS 上的 runner 实机验证不在容器内。 | 已确认 |
| D26 | 开发和早期测试阶段，主 Agent 和 worker 都运行在 Docker 中，以容器作为隔离边界，暂不依赖 traex 的 sandbox。worker 使用 `danger-full-access`；主 Agent 保留 `read-only` 设置，但不把它当作安全边界。实际部署测试时再评估各平台的 sandbox。 | 已确认 |
| D27 | 同一个用户可以同时使用多个客户端（多台设备上的浏览器，以后的 Electron 外壳）。所有客户端共享同一个入口、同一段会话和同一份账本视图，协议按多客户端并发来设计（见 §9）。 | 已确认 |
| D29 | runner 协议的凭据分两种：host 凭据（`X-Tend-Host` 加 bearer token，服务端只保存 token 的 SHA-256）用于 hello、claim、heartbeat；run 凭据（`X-Tend-Run` 加 bearer token）用于 run 自己的上报，token 由服务端密钥对 lease_id 做 HMAC-SHA256 派生，不落库，claim 重放时重新计算；lease 释放后失效。两种凭据都要求请求来自该 host 配置的 tailnet 节点。 | 已确认 |
| D30 | 仓库预先注册：服务端配置定义仓库（逻辑名、远程地址、默认基线候选），启动时同步进账本。要停止新任务挂载某个仓库、同时让已有任务做完，在配置里把它标记为 `retired`，host 仍可继续服务它；从配置中直接删掉的仓库同样变为 retired，但不再有 host 能服务它。runner 把仓库 bare clone 到 `$TEND_HOME/repos/<repo>.git`。账本只记录逻辑名和相对路径。 | 已确认 |
| D31 | MVP 没有后台推理 thread，run 的失败一律交给 owner：当前的实现 run 失败（runner 放弃、worker 崩溃、没有改动等）后，任务回到 queued 并以 `waiting_decision` 阻塞，同时开启待决事项，下一代实现需要显式请求；验收 run 自身失败等同 `blocked` 结论，任务以 `acceptance_env_unavailable` 阻塞，可以显式请求下一代验收。待决事项的回答只关闭问题，后续动作由回答方用对应命令显式发起。 | 已确认 |
| D33 | 工作区由 runner 管理：`$TEND_HOME/workspaces/<name>/` 是工作区根目录，每个仓库是其中一个子目录，worker 以工作区根目录为 cwd。工作区名称在建任务时保留（规划方可以指定，例如 `feature_a`），分支名为 `tend/<name>`。工作区跟着任务走，同一任务各代实现复用，并固定在创建它的 host 上；验收也在这台 host 上进行。 | 已确认 |
| D34 | TaskSpec 的 `workspace` 由一组 mount 组成（`repo`、`path`、`base`、`profile`），可以引用服务端配置的工作区模板，建任务时展开成显式的 mount。同一仓库在一个工作区内只能挂载一次。工作区在 host 上创建之后，spec 只能新增 mount，不能删除或修改已有 mount 的仓库和基线，也不能修改 host 要求。 | 已确认 |
| D35 | 工作区准备可插拔：checkout 之后，runner 先按各 mount 的 profile、再按工作区级 profile 执行 runner 配置中的命令（例如 template-assembler 的 `lcm sync`、`hab sync`）。profile 可以声明 `rerun_if_changed`，复用工作区时只有这些输入变化才重新执行。prepare 失败按环境问题处理，不算实现缺陷。 | 已确认 |
| D36 | 基线：mount 可以指定分支或完整 commit；不指定时由 runner 在 fetch 之后按仓库配置的候选、远端 HEAD、`main`、`master` 的顺序解析。spec 只保存意图，解析结果作为事实记在工作区上。 | 已确认 |
| D37 | 工作区根目录本身不在任何 git 仓库里，harness 不会自动加载各仓库的 AGENTS.md（Codex 只加载从 `.git` 所在根目录到 cwd 路径上的文件）。runner 在工作区根目录生成 `AGENTS.md`，写明布局（子目录、仓库、分支、基线）和工作规则，并要求在某个仓库里工作之前先读它自己的 AGENTS.md。这个文件不属于任何仓库，不进 artifact。 | 已确认 |

## 2. 上下文注入（修订 v0.1 §7）

### 2.1 已观察到的能力（traex 0.209.1 生成的协议）

- `thread/start` 接受 `developerInstructions`、`baseInstructions`，以及按 thread 覆盖的 `config`。
- `thread/inject_items { threadId, items: JsonValue[] }`：把原始 Responses API item 追加到模型可见历史。turn 进行中也可以注入；重复注入会追加多份，app-server 不去重。
- `turn/start` 的 `input` 只有用户输入类型（text、image、skill、mention），没有 developer 角色，因此不用它来承载上下文。
- Compaction 以独立的压缩 turn 进行，其中包含 `contextCompaction` item；压缩会替换历史，之前注入的原始条目会被丢掉。traex 0.209.1 不发 `thread/compacted` 通知。Hook 事件包含 `postCompact`、`sessionStart`、`userPromptSubmit`。
- `thread/compact/start` 可以主动触发压缩，便于测试。

### 2.2 注入策略

基本前提是：模型在同一个上下文里不会轻易忘掉已经注入的事实。因此 tend 把“模型上下文中已有的账本视图”看作一条有水位的基线，只补变化的部分。

1. **建立基线（完整快照）**：thread 新建、恢复，以及检测到 compaction 之后，下一次 turn 开始前注入一份有界的完整快照：角色约束、未完成承诺、待决事项索引、近期讨论关联。稳定的角色约束放在 `developerInstructions`，不重复注入。
2. **turn 之间（按需增量）**：用户消息进入前台队列后，Context Builder 比较账本与该 thread 的已注入水位：
   - 自上次水位以来没有相关变化：**不注入**。
   - 有相关变化：注入一条简短的 delta 消息，带上新水位。相关变化包括：任务状态跨越生命周期边界、新增或已解决的待决事项、约束或优先级变更、已到达的验收结论、unknown 或受阻状态。
   - 普通进度、心跳和日志永远不注入；模型需要时通过工具读取。
3. **水位推进的条件**：只有 `thread/inject_items` 返回成功、并且随后的 `turn/start` 成功时，才推进该 thread 的水位。任一步失败，下次重算 delta，或者退回完整快照。
4. **基线失效**：观察到 `contextCompaction` item、`postCompact` hook、thread 被 harness 重建或回滚，或者 tend 无法确认历史状态时，基线作废，下一轮注入完整快照。如果 compaction 发生在 turn 中途，并且 traex 允许在 turn 进行中 inject，可以立即补注入；否则等下一个 turn 前补（待验证）。
5. **turn 进行中的状态变化**：不依赖注入来拦截。所有写入类工具都携带 `expected_control_revision`，版本不符时服务端拒绝，并在错误结果里附上当前状态摘要。这样模型在同一 turn 内就能拿到新事实。

ContextPacket 仍按 v0.1 §7.3 的内容组织。注入的 developer 消息以 `[tend context packet <packet_id> epoch=<n> watermark=<event_seq>]` 开头，便于 trace 和排查重复注入。

### 2.3 Hook 的剩余用途

- `postCompact` / `sessionStart(source=compact)`：通知 tend 基线已失效；如果 hook 支持回传 additionalContext，也可以直接补一份快照。
- Hook 进程只携带 thread 级的短期凭据回调 tend，不读全局“当前任务文件”。
- traex 的 hook 配置使用 tend 专用的 home 目录（不修改用户的 `~/.trae`），具体环境变量待验证。

## 3. 主 Agent 的工具面（细化 v0.1 §14.3）

- 工具以动态工具的形式在 `thread/start` 的 `dynamicTools` 中注册（initialize 时声明 `experimentalApi`）。模型调用时，app-server 通过主会话的 stdio 连接向 tend 发出 `item/tool/call`，参数为 `{threadId, turnId, callId, namespace, tool, arguments}`；tend 返回 `{success, contentItems}`。
- 调用方身份由连接决定：只有 tend 自己启动的 app-server 进程能发起调用，tend 根据连接和 threadId 确定是哪个 harness 实例、哪个 thread，不信任 arguments 里自报的身份。
- 应用 request_id 的推导：`harness:<harness_instance>:<threadId>:<turnId>:<callId>`。callId 当作不透明字符串使用（code mode 下是复合字符串）。
- 第二层业务去重：规划写入以 `(source_message_id, plan_commit_seq)` 唯一；run 创建以 `(action_id, generation)` 唯一（v0.1 §8.6）。
- 第一批工具：`tasks.list`、`tasks.read`、`tasks.create`、`tasks.update_intent`、`actions.create`、`decisions.open`、`decisions.resolve_from_message`、`artifacts.read`、`requests.read`。主 Agent 拿不到 `acceptance.submit`，也没有 completed 的写入路径。
- 工具执行期间 tend 崩溃或连接中断时，调用的结果如何处理还没验证，B12 做专门的故障测试。在得到结论之前，工具的副作用按“结果未知”对待，靠 request_id 和业务去重保证重放安全。

## 4. Runner 协议（细化 v0.1 §10、§12.2）

传输：runner 通过 HTTPS（tailnet）长轮询连接控制服务，凭据是 per-host token。所有请求使用 v0.1 §8.3 的信封。

| 接口 | 语义 |
| --- | --- |
| `runner.hello` | 上报 host_id、capabilities，以及**本地 journal 中仍持有的 run 列表**，用于重连对账。 |
| `runner.claim` | 长轮询领取任务。服务端在一个事务里创建或分配 run，以 `(action_id, generation)` 唯一，并返回 run_id、lease_id、lease 到期时间和不可变 spec 引用。同一 request_id 重试时返回同一个分配。 |
| `runner.heartbeat` | 续租并上报观测状态。响应中可能带期望状态（继续、停止、暂停）和是否已撤销租约。 |
| `runner.events` | 上报事实事件。event_id 由 runner 基于原生 item 身份生成，重传时不变；以 `(run_id, runner_seq)` 去重。 |
| `runner.complete` | 提交结果和 artifact 引用（实现角色）或验收结论（验收角色）。凭据是该 run 专属的、限定作用域的 token。 |

自我 fencing：runner 在本地按租约到期时间计时。超过到期时间仍没有续租成功，就先停止对工作区的写入（中断 harness turn），再停下来等待对账，不再继续自主推进。这让 v0.1 §9.3 的 fencing 由真正执行写入的一方来执行。

Runner 本地有一个 journal（SQLite），记录持有的 run、lease 和已上报的最大序号。runner 重启后先执行 `hello` 对账，然后才 `claim` 新任务。

Worker 在节点本地驱动 `traex app-server`（或测试适配器），与主 harness 共用 harness 适配包。一个任务对应一个 worktree；验收 run 使用独立 worktree，基于 artifact 引用检出。

Artifact 由 runner 生成，不由模型生成：worker 只修改工作区（sandbox 下 worktree 的 git 目录不可写），turn 完成后 runner 执行 commit，并把 `(分支, commit sha)` 作为不可变的 artifact 引用上报。runner 启动 worker 时显式配置可写根目录、网络策略和环境变量（依据 spike A7）。

## 5. 范围（细化 v0.1 §17 阶段 C）

MVP 闭环：Web 消息 → 主 Agent（traex，单前台 thread）→ 通过动态工具创建任务与 PlanAction → 调度器派给 Linux 本机 runner → worker 产出 artifact（分支加 commit sha）→ 程序自动派独立验收 run → passed 则 completed，否则进入待决。

MVP 不做：任务范围的后台推理 thread（失败统一进入待决事项）、人工接管、导入、多机器、Orca、Electron。数据模型为这些保留字段，但不实现相关流程。

## 6. 网络与身份（tsnet）

### 6.1 拓扑

- **服务端**（`tend-server`）以 tsnet 节点身份加入 tailnet，主机名默认为 `tend`。它只在 tsnet listener 上提供 Web、API 和 runner 协议，不监听宿主机的公网或局域网接口。
- **runner**（`tend-runner`）也以 tsnet 节点身份加入 tailnet，主动连接 `tend`。Windows、macOS 和容器里都不需要安装 tailscaled。
- tsnet 的 state 目录需要持久化（服务端放在数据目录下，runner 放在 journal 旁边），保证重启后节点身份不变。

### 6.2 接入凭据

- 首次加入 tailnet 使用 auth key（环境变量或一次性文件传入，用完不落盘到配置）。runner 使用带 tag 的 key（例如 `tag:tend-runner`），服务端使用 `tag:tend-server`。
- tailnet ACL 只允许：用户设备访问 `tag:tend-server` 的 443 端口；`tag:tend-runner` 访问 `tag:tend-server` 的 runner 端口。runner 之间不互通。ACL 示例放在部署文档里，不由程序修改。

### 6.3 应用层身份

- 服务端对每个请求调用 tsnet 的 WhoIs，取得调用方的节点和用户身份，用作第一层身份。
- 角色映射写在服务端配置里：哪些 tailnet 用户是 owner；哪些节点（按 node key 或 tag 加主机名）对应哪个 host_id。
- 第二层仍然使用应用凭据：runner 的 per-host token 和 run 级 token、浏览器会话。主 Agent 的工具调用走本机 stdio，不经过网络。只在 tailnet 内也不能免除角色校验（D21）。
- 浏览器会话可以基于 WhoIs 自动登录（tailnet 用户等于 owner 时签发会话 cookie），不再单独设置密码。

### 6.4 TLS

服务端通过 tsnet 获取 tailscale 签发的证书，对外提供 `https://tend.<tailnet>.ts.net`。当前 tailnet 已开启 HTTPS 证书（spike A9）。证书在首次 TLS 握手时签发，服务端启动后应主动预取一次。没开启时退回 tailnet 内的明文 HTTP（WireGuard 已经加密），但浏览器的部分能力（例如 Service Worker、通知）会受限。

## 7. Web 客户端（细化 v0.1 §14.1）

- 按移动优先设计。窄屏时，聊天、任务、待决三个区域用底部 tab 切换；宽屏展开成多栏（聊天为主栏，看板和待决为侧栏）。
- 待决事项在移动端是最高频的入口：按决策和审批分别展示，选项可以直接点按，提交时携带 decision_id 和版本。
- 任务详情按需展开：目标与约束、状态、下一动作、产物、验收结论。日志用流式查看，默认折叠。
- 弱网是常态：本地发件箱（IndexedDB）保存待确认的请求及其 request_id；SSE 断开后按事件游标续传；回到前台时先拉快照再接增量。
- 后续可以做成 PWA（添加到主屏、推送通知）。推送依赖 §6.4 的 HTTPS，放在 MVP 之后。
- 技术选型建议：Vite + React + TypeScript；样式方案在阶段 C 开始时决定。

## 8. 开发环境

- 仓库内提供 `.devcontainer/`：基础镜像包含 Go 工具链、Node 24 与 pnpm、sqlite3 和常用调试工具。
- traex 是静态链接的二进制，宿主机的 traex 安装目录以只读方式挂载到 `/opt/traex`。tend 使用专用的 traex home（`~/.local/share/tend/traex-home`），和工作目录一样挂载到与宿主机相同的绝对路径，并设置 `TRAE_HOME`。traex 通过 SSH 鉴权：`~/.ssh` 只读挂载，启动时复制一份可写副本。
- 基础镜像从 Docker Hub 拉取；开发机无法直连时，可以在一台能访问外网的主机上 pull，再用 `docker save | ssh | docker load` 传回。具体主机等机器相关信息记在被 ignore 的 `.local/` 中。
- 容器里的服务端用 tsnet 的 ephemeral auth key 加入 tailnet，主机名加 `-dev` 后缀（例如 `tend-dev`），这样手机可以直接访问开发实例。tsnet 是用户态网络，容器不需要 TUN 设备或特权模式。
- 测试默认不联网：tsnet 在测试中替换为本地 listener，traex 替换为 fake harness。真实 traex 和 tailnet 的集成测试单独标记，需要时显式开启。

## 9. 多客户端（D27）

- **客户端标识**：每个客户端首次启动时生成 client_id 并持久保存（浏览器存在 IndexedDB 里）。它不是凭据：会话 cookie 和 client_id 绑定，鉴权仍然依据 tailnet 身份和会话。owner 的 Principal 带有 client_id，用于审计、事件游标、本地发件箱的归属，以及以后的推送订阅。
- **去重**：owner 的所有客户端共用一个去重域。request_id 是随机生成的，不同客户端之间不会冲突。任何客户端都可以通过 `requests.read` 查看其他客户端发起的请求的状态。
- **并发写入**：不同客户端对同一对象的写入，靠 `expected_control_revision` 或 decision 的版本号解决，先提交的生效。后到的请求收到 `stale_revision` 和当前状态，客户端刷新后由用户重新决定，不会静默覆盖。
- **会话**：所有客户端的消息进入同一个前台串行队列，按服务端分配的会话序号排序。每条消息带上发送方客户端的 `last_seen_conversation_seq` 和 `reply_to`，消息与任务的关联依据该客户端当时实际看到的内容（v0.1 §6.2）。
- **事件同步**：服务端把事件广播给所有在线的客户端，每个客户端用自己的游标续传。一个客户端的操作结果通过事件流同步到其他客户端，而不是只返回给发起方。
- **待决事项**：在一个客户端回答之后，其他客户端立即看到它已解决。用同一版本、同一答案重复回答，返回已有的结果；版本已经变化的回答，返回 `stale_revision`。
