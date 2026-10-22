# 阶段 F 计划：托管主 Agent 与 Web（F1–F8）

**日期：2026-10-22，owner 已确认（包括 §9）**
**依据：`docs/design.md` D16、D17、D20、D24、D27、D45、D47、D49、D50、D53、D55；`docs/design-v0.1.md` §6、§14；约定见 `AGENTS.md`。**
**前置：阶段 E 已合并，真机验收完成（`docs/findings/phase-e-acceptance.md`）。**

本阶段的工作项编号为 F1–F8。

D45 把托管主 Agent 往后放，先做外挂；D50 把 Web 延后。现在把两者一起恢复：tend 在服务端运行一个托管的主会话，owner 在手机和桌面的浏览器里（可以装成 PWA）和它对话，同时看任务、回答待决事项。外挂主 Agent（Claude Code、Codex、traex 里接入 tend 的会话）照常可用，两种形态并存。

设计稿：`docs/plans/phase-f/index.html`（桌面与手机并排，数据是虚构的示例），视觉语言参考 dsh 的 Web UI。

## 0. 范围与完成标准

- **交付物**：
  - 托管主会话按新的运行方式启用：traex app-server、共享 traex home、自动审批（F1）；
  - 会话可以读取：消息、主 Agent 的工具调用、消息状态（F2）；
  - owner 的事件流：会话与账本的变化，断线后按游标续传（F3）；
  - 停止回复（F4）；
  - 待决事项在 Web 里回答，带效果的直接执行，不带效果的交给主 Agent（F5）；
  - Web 客户端：工程、登录、PWA、发送队列，由 tend-server 提供（F6）；
  - Web 的对话、任务与待决界面，手机与桌面（F7）；
  - 真机验收（F8）。
- **完成标准**：
  - `make check` 通过（包含 Web 的类型检查、lint 和单元测试）；§8 的测试全部通过；
  - 集成测试（build tag `integration`）用真实的 traex 跑通：Web 的 API 发消息、主 Agent 建任务、事件流收到任务完成；
  - 真机验收（§7）。
- **不在本阶段**：
  - 通知（Web Push 等），继续按 D50 延后；
  - `tend chat` 这类 CLI 对话入口；
  - 主 Agent 接手自动排序（D52）；
  - 单一控制权（同一时间只允许一个主 Agent 写账本）：owner 提出过，讨论后不做，理由见 §2；
  - 离线使用：只缓存页面外壳和待发送的消息，离线时不能查看账本。

## 1. 写入 design.md 的决策

| 编号 | 决策 | 落地 |
| --- | --- | --- |
| D68 | 恢复托管主 Agent（修订 D45 的优先级、D20 的只读运行、D39 的主 Agent 部分）：服务端运行托管主会话；traex app-server 使用机器上 owner 的 traex home，进程设置 `TEND_ADDON=off`；sandbox `workspace-write`，`approvalPolicy = on-request`，`approvalsReviewer = auto_review`；与外挂主 Agent 并存，不设单一控制权（§2）。 | F1 |
| D69 | 会话的读取与事件流（细化 D27，落实 v0.1 §14.2）：会话记录包括 owner 的消息、主 Agent 的回复和它的工具调用，共用一个顺序；owner API 按顺序分页读取；事件流用 HTTP 流式响应推送账本事件，按 `event_seq` 续传；停止回复是一条 owner 控制命令（§3、§4）。 | F2、F3、F4 |
| D70 | Web 客户端（修订 D50）：手机优先，同时适配桌面，可以装成 PWA；一个对话加任务概览和任务详情；由 tend-server 提供，与 owner API 同源；登录沿用 D49 的 owner token（§5、§6）。 | F6、F7 |
| D71 | 在 Web 里回答待决事项（细化 D53）：选中带效果的选项时直接 `decisions.respond`，tend 执行效果；选中不带效果的选项时发一条关联这条待决事项的消息，由主 Agent 处理（§4）。 | F5 |
| D72 | 回复里的任务引用：主 Agent 提到任务时带上任务 ID，Web 把识别出的 ID 显示为带当前标题和状态的标签；账本里查不到的原样显示（§6）。 | F1、F7 |

## 2. 托管主会话（F1）

- **运行方式**：
  - 沿用 `internal/mainthread`：一个前台 thread，逐条消息一轮，注入上下文，工具是 app-server 的动态工具（D17、D20）。
  - traex home 默认用服务端所在机器上 owner 自己的 home，不再要求专用 home；`main_agent.traex_home` 仍可指定。主会话因此出现在 owner 的 traex 历史里。
  - 进程设置 `TEND_ADDON=off`：owner 的 traex 配置里装了 tend 的 hook 和 MCP（D46），不关掉的话，托管会话也会把自己当作外挂会话，上下文注入两次。
  - 权限：sandbox `workspace-write`，cwd 是 tend 数据目录下主会话专用的目录；`approvalPolicy = on-request`，需要提权的操作由 `approvalsReviewer = auto_review` 自动审查，不等人确认。现有 `isolation.go` 关掉的 shell、浏览等功能重新评估：主 Agent 需要能读仓库时，靠 sandbox 限制写入，而不是关掉工具。
  - 模型用 traex 的默认模型，`main_agent.model` 可以覆盖。
- **说明**（instructions）：
  - 提到任务时带上任务 ID（D72）；
  - 待决事项的回答以消息的形式到达时，用 `decisions_resolve` 记录并接着处理（§4）；
  - 其余沿用现有说明。
- **工具**：托管会话只拿托管形态的工具；`session_project` 这类只对外挂会话有意义的工具不给（当前 `tools.Specs()` 一并给出，需要拆开）。
- **与外挂主 Agent 并存**：
  - 托管会话看所有 project（现状），不按仓库绑定（D59 只对外挂会话）。
  - 外挂会话的写入照常出现在托管会话的上下文增量里，反过来也一样；各自的写入按 D47 不在自己的增量里重复。
  - 写入冲突由 `expected_control_revision` 解决。
  - **不设单一控制权**：托管主 Agent 只在 owner 发消息时工作，没有后台的轮次，不会和外挂会话同时对同一件事做出反应；写入已经有版本校验。单一控制权要新增取得、释放、找回失联会话的控制权等机制，省下的并不比新增的多。实际使用中出现问题再考虑。
- **配置**：`main_agent` 段去掉“目前不再开发”的说明；`docs/deployment.md` 写明启用方式、共享 home 的含义、`TEND_ADDON=off` 由服务端自己设置。

## 3. 会话的读取与事件流（F2、F3）

- **会话记录**（F2）：
  - 现有 `conversation_messages` 记 owner 的消息和主 Agent 的回复。新增主 Agent 的工具调用项：工具名、参数摘要、结果摘要、状态（进行中、完成、失败）、所属的消息和轮次。和消息共用一个顺序，Web 按这个顺序渲染（新迁移）。
  - 参数和结果有大小上限，超出的截断并注明，和 run 日志一样（`boundedBody`）。
  - 每条 owner 消息的状态：已收下、正在处理、已投递、执行状态未知、被停止（v0.1 §14.2；现有 `conversation.message_state` 事件），读取时随消息一起返回。
  - owner API：`GET /api/v1/conversation?before=<seq>&limit=<n>` 向前翻页，`?after=<seq>` 取新的。
- **事件流**（F3）：
  - `GET /api/v1/events?after=<event_seq>`，HTTP 流式响应，每个账本事件一行（`text/event-stream` 格式），带 `event_seq`；没有事件时定期发心跳。
  - 浏览器用 `fetch` 读流而不是 `EventSource`：`EventSource` 不能带 `Authorization` 头，而 owner API 用 bearer token（D49）。
  - 断线后按最后收到的 `event_seq` 续传；游标太旧（事件已清理）时返回明确的错误，客户端整体重新加载。
  - 事件只是“有变化”的通知：Web 收到任务、待决事项、host 的事件后重新读对应的视图，不在客户端拼状态；会话的事件直接带上新的消息或工具调用项。
  - 现有进程内的 `Broadcast` 用来唤醒等待中的流，不轮询数据库。
- **主 Agent 的回复不做逐字流式**：现有实现只记录完整的 `agentMessage`；回复在生成过程中，Web 显示“正在处理”和已经发生的工具调用。逐字流式以后需要时再做。

## 4. 停止回复与待决事项（F4、F5）

- **停止回复**（F4）：
  - owner 控制命令 `conversation.interrupt_reply`，参数是消息 ID：这条消息的轮次正在进行时，经 `harness.Thread.Interrupt` 中断，消息状态记为被停止；已经结束的轮次不受影响，返回当前状态。
  - 带 request_id 去重（约束 1）。停止请求先持久化完整 envelope 和 request_id。HTTP 回执丢失时先查询原请求；无记录时只允许沿用同一 request_id 恢复原 HTTP 投递（D11）。已有 accepted、succeeded 或业务 unknown 记录时，不创建新中断、不重放外部副作用，只查询既有请求并观察消息最终状态（约束 4、D69）。
  - 阶段 C 未合入的 #31（`control.interrupt_reply`）作为参考，重新实现。
- **在 Web 里回答待决事项**（F5，D71）：
  - 带效果的选项（tend 自己开出的“重新实现”“提高预算”“重新验收”“换一台从头再来”“取消任务”）：直接 `decisions.respond`，tend 在同一个事务里记录回答并执行效果（D53），不经过主 Agent。主 Agent 下一次处理消息时，从增量里知道 tend 做了什么。
  - 不带效果的选项（“修改 spec”“修好验收环境”“等待这台 host”，以及主 Agent 开出的所有选项）：Web 代 owner 发一条消息，正文是可读的回答（例如“关于『CLI 输出加版本号』：修改 spec”），消息关联这条待决事项和所选的选项（`SubmitMessagePayload` 增加可选的 `decision` 字段：ID、版本、选项）。主 Agent 的这一轮输入里带上这条关联，它用 `decisions_resolve` 记录回答后接着处理。
  - owner 也可以不点按钮，直接在对话里回答，和现在一样。
  - tend 开出的固定选项由 Web 翻译成中文显示；主 Agent 开出的选项原样显示。

## 5. Web 客户端工程（F6）

- **位置与技术栈**：
  - 仓库根目录的 `web/`（TypeScript，不是 Go 业务代码，不放进 `internal/`）；`AGENTS.md` 的仓库结构随之更新。
  - Vite + React + TypeScript（strict）；样式用 CSS Modules 加 CSS 变量的设计 token（参考 dsh 的 `ui-theme`），不引入组件库；状态管理用轻量的 store。
  - Markdown 渲染用成熟的库并做 HTML 清洗；代码块只做基本的等宽显示和复制，不做语法高亮（以后需要再加）。
  - API 类型由 `internal/protocol` 导出的 JSON Schema 生成（`AGENTS.md` §3），不手写。
  - eslint、prettier；单元测试用 vitest。
- **构建与提供**：
  - `make web` 构建到 `internal/webui/dist/app/`（不提交），`internal/webui` 用 `go:embed` 嵌入；没有构建时嵌入的是一个说明“Web 未构建”的页面，Go 的构建和测试不依赖 Node。
  - tend-server 在 `/` 提供 Web，`/api/` 仍是 owner API，同源，不需要 CORS。
  - `make check` 增加 Web 的类型检查、lint、单元测试和构建；CI 同样执行。
- **登录**：
  - 页面打开时调用 `POST /api/v1/session`，服务端按 tailnet 身份（WhoIs）认出 owner 后签发 token（D49），和 `tend login` 相同。token 存在浏览器本地，过期或失效时自动重新领取：同一台设备在 tailnet 上的身份就是凭据，不需要输入密码。
  - 不是 owner 的设备看到明确的说明。
- **PWA**：
  - manifest（名称、图标、`display: standalone`、主题色）和 service worker；service worker 只缓存页面外壳，API 请求一律走网络。
  - 安装需要 HTTPS：用服务端已有的 tailnet 证书（`https://<hostname>.<tailnet>.ts.net`）。tailnet 没有开启 HTTPS 时 Web 仍可用，只是不能安装、不能离线打开外壳。
- **发送队列**（v0.1 §14.2）：
  - 发出的消息先写进浏览器的 IndexedDB，带一个持久的 request_id，再发送；页面关闭、断网后重新打开时，用同一个 request_id 重发，服务端去重（约束 1）。
  - 收到服务端的回执前，消息显示“发送中”；结果未知的消息显示“发送结果未知”，提供查看和重发（重发使用原 request_id）。

## 6. Web 界面（F7）

按设计稿：

- **信息结构**：只有一个对话（D27：一个入口、一个会话，多个设备共享），没有会话列表。
  - 桌面三栏：左侧是工作概览（需要处理的事、按 project 分组的任务、host 状态），中间是对话，右侧是任务详情（点任务行或回复里的任务标签打开，可以关闭）。
  - 手机单栏：底部三个标签（对话、任务、待处理）；任务详情是全屏的页面。
  - 断点：宽于约 1180px 时三栏并排；760–1180px 之间任务详情以浮层盖在对话右侧，不挤压对话列；窄于 760px 是手机布局。中间的对话列最宽约 760px。
- **对话**：
  - owner 的消息是右侧的气泡，下面是状态（§3）；主 Agent 的回复是不带气泡的整段 Markdown。
  - 主 Agent 的工具调用是单行可展开的条目：工具名和摘要，展开看参数和结果。
  - 正在回复时显示“正在处理”和耗时；发送按钮变成停止按钮（F4）。
  - 回复里的任务 ID 显示为任务标签（D72），点开任务详情。
  - 只在接近底部时自动跟随新内容，否则显示“跳到最新”；向上滚动加载更早的消息，保持阅读位置。
- **待决事项**：
  - 停靠在输入框上方的卡片：问题、上下文、选项按钮；带效果的选项标明“tend 执行”；有多个时可以翻页。
  - 手机的“待处理”标签列出全部待决事项和被接管的任务（被接管的任务提供“交还给 tend”，即 `tend resume`）。
- **任务详情**：状态、project 与评级、控制模式、工作区与分支、目标与验收条件、进展（动作、run、产物、结论）、最近的 run 日志，以及暂停、恢复、接管、取消。
- **其他**：浅色与深色主题（跟随系统，可切换）；连接状态（断线时显示正在重连）；界面文字是中文。

## 7. 真机验收（F8）

验收范围更新（2026-10-22）：owner 确认本轮只考虑普通浏览器，不再验收添加为 Web App；已有 PWA 实现与自动化验证不作为真机安装验收结论。

- 服务端启用托管主 Agent，使用 owner 的 traex home。
- 在 Mac 和 iPhone 的普通浏览器上打开同一个对话：一端发的消息和主 Agent 的回复，另一端实时出现。
- 用对话建一个真实的小任务，看着它被领取、实现、验收到 completed；任务标签和侧栏的状态随之变化。
- 待决事项：制造一次实现失败，在手机上点“重新实现”（tend 直接执行）；再制造一次，点“修改 spec”，主 Agent 接着问要怎么改。
- 停止回复：在主 Agent 回复过程中停止，消息状态为被停止，之后的消息照常处理。
- 断线：发送时断开网络，恢复后消息不重复、不丢失；服务端重启后事件流续上。
- 外挂主 Agent 并存：在 Mac 的外挂会话里修改一个任务，托管会话的下一轮知道这次修改。
- 自动审批：主 Agent 需要读仓库时，不停在等待确认上。

## 8. 测试

- **单元测试**：
  - 会话记录：工具调用项的写入与顺序、分页、截断（F2）；
  - 事件流：按游标续传、心跳、游标过旧、客户端断开后 goroutine 退出（F3，约束 7）；
  - 停止回复：进行中的轮次被中断、已结束的不受影响、去重（F4）；
  - 带待决事项关联的消息：校验关联、主 Agent 输入里带上关联（F5）；
  - Web：发送队列（持久 request_id、重发、结果未知）、事件流的续传与重连、任务 ID 的识别、固定选项的翻译（vitest）。
- **端到端测试**（`internal/e2e`，fake harness）：Web 用到的 API 走一遍：发消息、读会话、收事件、停止回复、两种待决回答。
- **故障测试**：`TestFault_*`：发消息回执丢失后用原 request_id 重发；事件流在服务端重启后续传；停止回复的回执丢失。
- **集成测试**（`integration`）：真实 traex 的托管主会话，经 owner API 发消息、建任务，事件流收到任务完成。

## 9. owner 已确认的选择

- 对话入口是 Web（手机与桌面，PWA），设计参考 dsh，先对设计稿；通知延后。
- 托管主 Agent 用 traex app-server 和共享的 traex home；权限按“auto approve”，即 sandbox `workspace-write` 加 `auto_review` 自动审查（§2）。
- 与外挂主 Agent 并存；owner 提出的单一控制权讨论后不做（§2）。
- 待决事项的按钮：带效果的直接执行，不带效果的交给主 Agent（§4）。
- Web 放在仓库根目录的 `web/`，由 tend-server 嵌入提供；Node 只在构建 Web 时需要（§5）。
- 界面只做中文（§6）。

## 10. 实施顺序

1. F1：托管主会话的运行方式和工具拆分。
2. F2、F3：会话记录与读取、事件流。
3. F4、F5：停止回复、带待决事项关联的消息。
4. F6：Web 工程、构建与嵌入、登录、PWA、发送队列。
5. F7：界面，按设计稿；每完成一块在真机浏览器上对照设计稿检查。
6. F8：真机验收，结论写入 `docs/findings/phase-f-acceptance.md`。
