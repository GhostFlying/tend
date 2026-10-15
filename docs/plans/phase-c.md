# 阶段 C 计划：owner API、事件流与 Web（C1–C10）

**日期：2026-10-15**
**依据：`docs/design-v0.1.md` §6.2、§8、§9.2、§14；`docs/design.md` §5–§9；约定见 `AGENTS.md`。**
**前置：阶段 B 已合并（`docs/plans/phase-b-core.md`、`docs/plans/phase-b-runtime.md`）。**

阶段 B 的闭环只能在测试里驱动：没有给人用的入口。本阶段补上 owner 的 HTTP API、SSE 事件流和移动优先的 Web，让 owner 在手机或电脑的浏览器里完成 v0.1 §17 阶段 C 的闭环：Web 发消息 → 主 Agent 关联任务 → worker 实现 → 独立验收 → completed 或待决事项。阶段 B 的真机验收合并到本阶段末尾一起做（§10）。

## 0. 范围与完成标准

- 交付物：`internal/api`（新增）、`web/`（新增）、`internal/protocol` 的 JSON Schema 导出；`app`、`store`、`mainthread`、`scheduler`、`runner/gitws`、`cmd/tend-server` 的相应修改。
- 完成标准：
  - `make check` 通过，CI 增加 Web 的类型检查、lint、测试、构建，以及生成类型的一致性检查；
  - §11 列出的 `TestFault_*` 全部通过；
  - 一个进程内端到端测试只经过 owner API（真实 HTTP、SSE、fake harness、fake runner）从消息跑到 completed；
  - 真机验收（§10）：服务端和 runner 部署在宿主机上并加入 tailnet，owner 用手机浏览器跑通一个真实任务。
- 不在本阶段：
  - template-assembler 的专门支持（原计划的 prepare 选择、验收预热、MR/CQ 交付）。按 D44，仓库特定的流程交给 worker agent。
  - 人工接管、多机器调度、断线 UX 的完善（阶段 D）。
  - PWA 和推送、请求撤销（`cancel`）、owner 直接编辑 spec 或直接建任务。这些都通过和主 Agent 对话完成（D01）。

## 1. 需要写入 design.md 的决策（提议）

| 编号 | 决策 | 落地 |
| --- | --- | --- |
| D40 | Web 工程：Rsbuild + React + TypeScript（strict），样式用 CSS Modules 加 CSS 变量，不引入 UI 组件库和路由库。构建产物用 `embed` 打进 `tend-server`，单个二进制同时提供 Web、owner API 和 runner 协议。`go build` 不依赖 Node：没构建 Web 时内嵌一个占位页。 | C7 |
| D41 | 验收在任务自己的工作区里进行（修订 D33 和计划 B 的 §6.2）。验收的独立性来自独立的 run 和 agent，不来自独立的工作区。同一任务的实现 run 和验收 run 互斥地使用工作区。验收 agent 只负责判断任务是否完成：可以构建、运行测试，但不修改代码、不清理、不提交，这一点只通过给验收 agent 的说明约束。tend 不在验收之后做任何清理，也不对验收后的工作区状态做承诺。 | C2 |
| D42 | owner 身份：服务端配置列出 owner 的 tailnet 登录名。每个请求都用 WhoIs 取得登录名并检查。浏览器首次访问时领取会话 cookie，cookie 用服务端密钥签名并绑定 client_id。写请求必须带和 cookie 一致的 `X-Tend-Client` 头，防止跨站请求。本地开发 listener 使用配置里指定的固定 owner。 | C4 |
| D43 | owner API：写入统一走 `POST /api/v1/ops`，请求体是 v0.1 §8.3 的信封，响应是 `Operation`，和 runner 协议同一套去重和错误码。读取用 GET：快照、任务详情、消息分页、run 日志、请求状态。事件流是 `GET /api/v1/events`（SSE），游标是 `events.event_seq`，快照和游标在同一个读事务里取得。 | C5、C6 |
| D44 | 交付由 worker agent 完成：push、MR、CQ 跟进、提交信息规范等仓库特定的流程，tend 不内置，也不提供交付 profile。`deliver` 动作类型保留，具体机制（例如验收通过后的交付 run）在需要时设计。 | 本计划 |

D44 在本计划的 commit 里直接写入 design.md；其余决策在落地的 commit 里写入。

## 2. C1 事件与通知补齐

SSE 只转发 `events` 表。阶段 B 有几处状态变化没有写事件，或者写了事件但不唤醒等待方，先补齐。

- **事件**：
  - 领域规则开出的待决事项（run 失败、验收环境不可用）补写 `decision.opened`，和工具开出的待决事项一致。
  - 消息的处理状态写成 `conversation.message_state` 事件，body 为 `{message_id, state}`：
    - 状态由 outbox 和 `main_turns` 推出，取值 `queued`、`processing`、`replied`、`interrupted`、`failed`、`unknown`。
    - `mainthread` 改写 turn 状态、`FinishDelivery` 结束投递时，在同一个事务里写这个事件。
    - v0.1 §14.2 要求界面区分“已收下、正在处理、已投递、执行状态未知”，这里就是它的数据来源。
- **通知**：
  - `App` 的变更通知从单个 `Notifier` 改为可以多方订阅的广播：runnerapi 的长轮询和 SSE 都订阅它。
  - `runnerapi.Broadcast` 移到 `app`，导出等待方法。
  - 所有写事件的路径都在提交后通知，包括 `ExpireLeases`、`FinishDelivery`、`RecordRunEvents`（不限于观测状态变化）和 `PutMainTurn`。
- **run 日志通知**：`run_logs` 不进事件表，写入后单独通知，供日志接口的长轮询使用（§5.3）。

## 3. C2 验收复用任务工作区（D41）

- **调度**：
  - `scheduler.Candidate.ImplementOccupied` 改为 `WorkspaceOccupied`：同一任务任何角色的 run 还可能在写工作区时（starting、running、waiting、interrupted、unknown），实现和验收都不派发。
  - 现有的 host 亲和性规则不变。
- **gitws**：
  - accept 的工作区根目录就是任务的工作区，不再建 `workspaces/.accept/<run_id>`。
  - **准备**：
    - 检查各 mount 的 HEAD 是否等于 artifact commit、是否仍在 `tend/<name>` 分支上。
    - 不一致按完整性检查失败处理，验收 run 失败，任务以 `acceptance_env_unavailable` 阻塞。
    - 一致时按 `rerun_if_changed` 执行 prepare profile（通常不需要重跑）。
  - 验收之后不恢复、不清理工作区（D41）。
  - accept 的 scratch 目录仍然按 run 分开，结束后删除。
- **runner 生成的 `AGENTS.md`（D37）**：
  - 验收角色的说明改为：在任务工作区里只判断任务是否完成；可以构建、运行测试，但不修改代码、不清理工作区、不提交或改动 git 历史。
  - 这是对验收 agent 的约束，runner 不做检查或强制。
- **测试**：
  - `TestFault_WorkspaceExclusive`：验收进行中不派发实现，反之亦然。
  - 验收 run 在任务工作区里检出的是 artifact commit；HEAD 或分支不一致时验收失败。
  - 修改阶段 B 中假设验收工作区独立的测试。

## 4. C3 JSON Schema 导出与 TS 类型（D22）

- `internal/protocol/schema`：一个小的反射生成器，不引入第三方库。
  - 支持 struct（json tag、omitempty）、指针、切片、map、`json.RawMessage`（任意 JSON）和 `time` 字段。
  - 枚举通过显式注册取值列表。
  - 输出 `api/v1` 用到的类型：信封、`Operation`、`Error`、各操作的 payload 和 result、读接口的视图、SSE 事件。
- 读视图类型从 `internal/tools/views.go` 中提出来，放到 `internal/protocol` 的视图类型里：主 Agent 工具和 owner API 用同一套，避免两套视图漂移（v0.1 §14.3）。
- **生成命令**：`go run ./internal/protocol/schemagen` 写出 `web/src/generated/api.schema.json`，再由 Web 的 `pnpm gen` 用 `json-schema-to-typescript` 生成 `web/src/generated/api.ts`。
  - 两个文件都提交，`web/src/generated/README.md` 写明生成命令和来源（约束 11）。
  - `make gen` 依次执行这两步。
  - CI 重新生成并检查没有 diff。

## 5. C4–C5 owner 身份与 API（`internal/api`）

### 5.1 身份与会话（D42）

- **服务端配置**：
  - 新增 `owners`：tailnet 登录名列表。
  - 新增 `local_owner`：只在 `listen` 模式（不加入 tailnet）下生效，作为本地 listener 的固定 owner 登录名。
- **会话**：
  - `POST /api/v1/session`，body 是 `{client_id}`。服务端先用 WhoIs 取得登录名，确认在 `owners` 里，再签发 cookie `tend_session`：HttpOnly、SameSite=Strict，TLS 下加 Secure。
  - cookie 的内容是 `{login, client_id, issued_at}` 加 HMAC。密钥由 `TEND_SERVER_SECRET` 派生，和 run 凭据（D29）用不同的派生标签。
  - 有效期 30 天，过期后客户端重新领取。
- **每个请求的校验**：
  1. WhoIs 得到的登录名在 `owners` 里，并且等于 cookie 里的登录名；
  2. 写请求的 `X-Tend-Client` 头等于 cookie 里的 client_id；
  3. 得到 Principal `{kind: owner, subject: login, client: client_id}`。

  tailnet 身份和 cookie 缺一不可：cookie 被拿到另一台不是 owner 的设备上无效。

### 5.2 写入（`POST /api/v1/ops`）

- 请求体是信封，响应是 `{operation}` 或 `{error}`，HTTP 状态码的映射和 runnerapi 相同。
- 只放行以下操作（白名单）：

  | 操作 | 用途 | 状态 |
  | --- | --- | --- |
  | `conversation.submit_message` | 发消息 | 已有 |
  | `decisions.respond` | 回答待决事项 | 已有 |
  | `actions.create` | 待决之后重试实现或验收（D31） | 已有 |
  | `control.set_mode` | 暂停、恢复任务 | 已有 |
  | `tasks.cancel` | 取消任务 | 已有 |
  | `control.interrupt_reply` | 停止主 Agent 当前的回复 | 新增 |

- **`control.interrupt_reply`**：
  - 目标是一条消息。命令确认这条消息有处于 `started` 的 turn，然后写一条 outbox 消息到新目的地 `main_control`。
  - 它和 `main_thread` 的队列分开（per-destination worker），所以不会排在正在进行的 turn 后面（v0.1 §6.2）。
  - handler 调用 `mainthread.Manager.Interrupt(messageID)`：当前 turn 处理的正是这条消息时，调用 `turn/interrupt`，否则什么都不做。
  - 中断是幂等的，对账时可以安全重试。
  - 结果状态是 `accepted`。turn 结束后，消息状态变成 `interrupted`，客户端从事件流看到这个变化。

### 5.3 读取

| 接口 | 内容 |
| --- | --- |
| `GET /api/v1/snapshot` | 未结束的任务摘要、待决事项、最近 50 条消息（带处理状态和关联对象）、hosts 概况，以及同一读事务里的 `cursor`（最新的 `event_seq`） |
| `GET /api/v1/tasks/{id}` | 任务详情：spec、动作、run、artifact、验收结论、待决事项、工作区的解析结果 |
| `GET /api/v1/messages?before=<seq>&limit=` | 更早的消息，分页 |
| `GET /api/v1/runs/{id}/logs?after=<seq>&wait=<s>` | run 日志分页；带 `wait` 时没有新日志就长轮询，最多 25 秒 |
| `GET /api/v1/requests/{id}` | owner 去重域里某个请求的状态（`requests.read`），任何客户端都能查其他客户端发起的请求 |

- 读请求可以带 `X-Tend-Request` 头，只用于日志关联，不落库（v0.1 §8.5）。
- 已完成的任务在任务列表里分页读取：`GET /api/v1/tasks?finished=1&before=`。

## 6. C6 事件流（`GET /api/v1/events`）

- 游标来自 `?after=<seq>` 或 `Last-Event-ID` 头。先按页（500 条）补发游标之后的事件，然后在变更广播上等待新事件。
- 每 15 秒发一次注释行作心跳。
- **SSE 消息格式**：
  - `id` 是 `event_seq`；
  - `data` 是 `{seq, kind, object_type, object_id, object_version, body, created_at}`；
  - `body` 原样转发事件表的 `body_json`，事件里本来就不放凭据。
- 客户端的处理：
  - `conversation.message` 带消息正文，客户端直接追加；
  - `conversation.message_state` 更新气泡状态；
  - 任务、run、待决事项相关的事件只用来标记对应对象已过期，客户端合并请求后重新读摘要或详情。

  这样服务端不用为每个事件推送完整视图，客户端也不需要按事件重放领域规则。
- 断线重连从最后收到的 `id` 继续。页面回到前台时先拉快照，再从快照的 `cursor` 接增量（design.md §7）。
- 事件表不清理，所以游标不会过期。

## 7. C7 Web 工程（`web/`，D40）

- 依赖：`react`、`react-dom`、`@rsbuild/core`、`@rsbuild/plugin-react`、`typescript`、`eslint`、`prettier`、`@rstest/core`（Rsbuild 的测试工具）、`json-schema-to-typescript`。
  - 不用路由库：三个区域加一个详情视图，用状态和 URL hash 就够了。
  - IndexedDB 自己写一层很薄的封装。
- **构建产物**：`pnpm build` 输出到 `internal/api/webui/dist/`。
  - 这个目录被 ignore，只提交一个占位的 `index.html`，保证没有 Node 时也能 `go build`。
  - `make web` 构建 Web，`make build` 不依赖它。
- **CI**：新增 web job：`pnpm install --frozen-lockfile`、`pnpm typecheck`、`pnpm lint`、`pnpm test`、`pnpm build`、生成类型一致性检查。
- 开发时：`pnpm dev` 起 Rsbuild 的 dev server，API 请求代理到本地的 `tend-server`（`listen` 模式）。

## 8. C8–C9 Web 功能

C8 是布局、聊天和发件箱（§8.1、§8.2、§8.5），C9 是任务和待决事项（§8.3、§8.4）。

### 8.1 布局（D24、design.md §7）

- 窄屏用底部三个 tab：聊天、任务、待决。待决的 tab 带未处理数量。
- 宽屏展开成三栏：聊天是主栏，任务和待决是侧栏。
- 任务详情窄屏是全屏视图，宽屏是右侧面板。

### 8.2 聊天

- 消息列表向上滚动时分页加载。
- 自己发的消息显示处理状态：已收下、正在处理、已回复、已中断、失败、未知。
  - 状态为“未知”时提示“这条消息是否已处理未知”，由用户决定是否重发；重发是新的请求（v0.1 §9.2）。
- 主 Agent 回复的消息显示关联的任务和待决事项，点开进入详情。
- **回复引用**：长按（宽屏是悬停菜单）一条消息可以回复它。发送时自动带上 `reply_to` 和本客户端已看到的最大序号 `last_seen_seq`（v0.1 §6.2）。
- 正在处理的消息上有“停止”按钮，对应 `control.interrupt_reply`。

### 8.3 任务

- **列表**：未结束的任务按优先级排列，显示生命周期、阻塞原因、控制模式和当前 run 的观测状态。期望状态和观测状态分开显示，例如“已请求暂停，worker 仍在运行”（v0.1 §5.1）。已结束的任务折叠，分页加载。
- **详情**：目标与验收标准、工作区（mount、基线的解析结果、host）、动作和代次、run、artifact（每个 mount 的分支和 commit）、验收结论（问题列表和证据）、待决事项。
- **操作**：暂停或恢复、取消（二次确认），都带 `expected_control_revision`。
- **run 日志**：默认折叠，展开后分页读取，run 进行中时用长轮询追加。
- 收到 `stale_revision` 时刷新详情，并提示状态已经变化，由用户重新操作，不自动重试（design.md §9）。

### 8.4 待决事项

- 列出所有未回答的待决事项。选项可以直接点按，也可以补充文字。提交时带上 `decision_id` 和版本。
- 回答之后，按 D31 显示后续操作。例如实现失败的待决事项回答“重试”后，提供“开始下一代实现”按钮（`actions.create`）。回答本身不触发后续动作。
- 其他客户端回答后，事件流把这一项标为已解决。

### 8.5 弱网与多客户端（D27、design.md §9）

- 首次启动时生成 `client_id`，存进 IndexedDB，并领取会话。
- **本地发件箱**：每个写请求先生成 request_id，连同信封存进 IndexedDB，再发送。
  - 网络失败时保留，联网后用同一个 request_id 重试。
  - 返回 `accepted` 或 `succeeded` 后，从发件箱删除。
  - 结果不确定时，用 `GET /api/v1/requests/{id}` 查询，不重新执行。
  - 发件箱里的消息在聊天里显示为“待发送”。
- 事件游标按客户端保存，断线后续传；回到前台时先拉快照（§6）。

## 9. C10 组装

- **`tend-server`**：
  - **tailnet 模式**：
    - 同一个 mux 同时提供 `/runner/v1/*`、`/api/v1/*` 和 Web 静态文件，在 tailnet 的 `:443`（`ListenTLS`）和 `:80` 上提供服务。
    - 用户设备访问 443，runner 访问 80，由 tailnet ACL 限制（design.md §6.2）。两类接口各自鉴权，不依赖端口来区分。
    - 启动后用 `LocalClient().CertPair` 预取一次证书。tailnet 没开 HTTPS 时，记录警告，只用 80。
  - **`listen` 模式**：一个明文 listener 提供全部接口，owner 是 `local_owner`。
- 更新 `docs/deployment.md`：owner 配置、ACL 的端口、浏览器访问地址、本地开发方式。
- 端到端测试 `TestEndToEnd_OwnerAPI`：
  1. 用 HTTP 领取会话、通过 `/api/v1/ops` 发消息；
  2. 在 SSE 上看到消息状态变化和任务事件；
  3. fake harness 建任务，fake runner 实现、验收；
  4. 最后通过快照确认任务 completed。

## 10. 真机验收（阶段 B 与 C 一起）

本阶段最后一个 PR 合并后，和 owner 一起做，结论写进 `docs/findings/phase-c-acceptance.md`，机器相关的信息留在 `.local/`。

1. **部署**：在宿主机上构建 `tend-server`（带 Web）和 `tend-runner`；服务端用 `tag:tend-server` 的 key 加入 tailnet，runner 用 `tag:tend-runner`，配置一个真实仓库。
2. **基本闭环**：owner 用手机浏览器打开 `https://<tend 主机名>.<tailnet>.ts.net`，发一条需要改代码的消息，看到：
   - 消息状态的变化；
   - 主 Agent 的回复和建出的任务；
   - run 的进展和日志；
   - 验收结论；
   - 任务 completed，artifact 的分支和 commit 在 runner 上可以查到。
3. **故障演练**：
   - 发消息时断网，确认联网后发件箱自动重发且只处理一次；
   - 任务运行中重启服务端，确认 run 和消息状态正确恢复；
   - 在电脑上回答一个待决事项，手机上同步显示为已解决；
   - 停止主 Agent 的一次回复；
   - 暂停再恢复任务。

## 11. 必须通过的测试（本阶段子集）

| 测试 | 场景 | 项 |
| --- | --- | --- |
| `TestFault_WorkspaceExclusive` | 同一任务的实现和验收不会同时使用工作区；unknown 的 run 也算占用 | C2 |
| `TestFault_DomainDecisionVisible` | run 失败开出的待决事项出现在事件流和快照里 | C1 |
| `TestFault_SSEResumeNoGap` | 快照之后、订阅之前发生的事件不丢；断线后从 `Last-Event-ID` 续传，不重复也不遗漏 | C6 |
| `TestFault_ClientRetrySameRequest` | 发件箱用同一 request_id 重发消息，只产生一条消息，返回原结果 | C5 |
| `TestFault_ConcurrentDecisionAnswers` | 两个客户端同时回答同一待决事项：先到的生效，后到的得到 `stale_revision`；同一答案重复提交返回原结果 | C5 |
| `TestFault_StaleControlFromUI` | 用旧的 control_revision 暂停任务，得到 `stale_revision` 和当前状态 | C5 |
| `TestFault_InterruptReply` | 停止当前回复：turn 被中断，消息状态变为 `interrupted`；对已结束的回复不产生作用 | C5 |
| `TestFault_CookieWithoutOwnerNode` | 合法 cookie 来自不是 owner 的 tailnet 用户：拒绝 | C4 |
| `TestFault_CrossSiteWrite` | 写请求缺少或带错 `X-Tend-Client`：拒绝，不产生副作用 | C4 |

Web 侧用 Rstest 测发件箱、事件游标和状态合并的逻辑；界面不做截图测试。

## 12. 实施顺序与提交拆分

| 步骤 | commit | 内容 |
| --- | --- | --- |
| 1 | `docs: add the phase c plan` | 本计划，D44 写入 design.md |
| 2 | `feat: complete domain events and change broadcast` | C1 |
| 3 | `feat: run acceptance in the task workspace` | C2，D41 |
| 4 | `refactor: move read views into protocol` | 为 C3 准备，行为不变 |
| 5 | `feat: export api json schema` | C3 的 Go 部分 |
| 6 | `feat: authenticate owners with tailnet identity and sessions` | C4，D42 |
| 7 | `feat: impl owner api and reply interrupt` | C5，D43 |
| 8 | `feat: impl owner event stream` | C6 |
| 9 | `build: scaffold the web client with rsbuild` | C7，D40，CI 的 web job，生成 TS 类型 |
| 10 | `feat: impl web chat` | C8 |
| 11 | `feat: impl web tasks and decisions` | C9 |
| 12 | `feat: serve the web client and api from tend-server` | C10 |
| 13 | `docs: record phase c acceptance` | §10 的结论 |

每一步一个 PR，按 AGENTS.md §5 经过 codex review。

## 13. 之后的阶段要跟进的事项

- 交付：验收通过后的交付 run（D44），由 worker agent 按仓库自己的流程 push、建 MR、跟进 CQ，tend 记录结果。
- 大仓库的工作区成本：template-assembler 这类仓库每个工作区约 11GB 依赖，工作区的保留和清理策略按实际使用情况再定。
- PWA、推送通知（依赖 HTTPS）、请求撤销。
- 阶段 D：人工接管、`paused` 的恢复、多机器调度、断线 UX。
