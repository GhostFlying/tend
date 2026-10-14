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

## A5 主 Agent 隔离

复现：`go run ./spikes/appserver -scenario isolate`。主 thread 的 cwd 放在仓库外的专用目录，使用只读 sandbox，并通过 thread 级 `config`（点分路径，与 `-c` 写法相同）关闭不需要的功能，完整列表见 `spikes/appserver/isolate.go`。

| 项 | 结果 |
| --- | --- |
| AGENTS.md | `instructionSources` 为空，不再加载任何项目说明 |
| 基础上下文 | 第一个请求的输入从约 12.9k 降到约 8.7k token；剩下的系统内容是 tend 的 developerInstructions、sandbox 权限说明、多 agent 团队说明和 auto-compact 提示 |
| shell、插件、skills、浏览器、web search、`request_user_input` | 已经从工具列表中移除 |
| 写文件 | 模型尝试用 `apply_patch` 写文件，被只读 sandbox 拒绝，文件没有创建 |
| 动态工具 | 正常可用 |

关不掉的部分：
- 这些工具由模型目录（model catalog）决定，配置覆盖不了：code mode（`exec`/`wait`）、`apply_patch`、三个 MCP resource 工具。GPT-5.6 和 GPT-6 系列的目录项强制使用 code mode。
- `multi_tool_use.parallel` 由 Responses API 那一侧添加，无法关闭。
- `cron_*` 是 traex 自己加的工具，上游没有，暂时没找到开关。
- 这几项在只读 sandbox 下都写不了工作区，所以不影响隔离；代价只是一些上下文 token。
- 多 agent 协作工具**有意保留**：主 Agent 有时需要用 subagent。注意 traex 的 `agents` 配置是“角色名 → 角色配置”的映射，和上游的 `agents.enabled` 不兼容。subagent 是否继承只读 sandbox，留到 B12 确认。

**重复调用**：一次请求里，模型连续两次调用了同一个工具，参数相同、callId 不同，间隔约 3.6 秒。这直接证明只靠 request_id 去重不够，有副作用的工具必须做业务层去重（v0.1 §8.6，design.md §3 的第二层去重）。

模型列出的工具清单只作参考，证据以工具列表的实际变化和 token 数为准。

## A6 重启与恢复

- 关闭 app-server 进程，重新启动并握手之后，`thread/resume {threadId}` 成功，历史完整（包括压缩后的状态），模型答对了暗号。
- **影响**：app-server 重启本身不会让基线失效，只要 resume 成功、并且期间没有发生 compaction。tend 重启后仍然要按“无法确认就重建”的原则：如果水位记录和 thread 的状态对不上，就注入一份完整快照。

## A7 Worker 形态

复现：`go run ./spikes/appserver -scenario worker`。用一个临时 git 仓库，在 `task/sub` 分支的 worktree 里启动 workspace-write、`approvalPolicy=never` 的 thread，让 worker 实现一个小改动并跑测试；turn 结束后由 spike 扮演 runner，执行 commit。

| 项 | 结果 |
| --- | --- |
| Docker 中的 sandbox | 默认 seccomp 下 bubblewrap 建不了 user namespace，所有命令都失败。加上 `--security-opt seccomp=unconfined` 后正常（宿主机允许非特权 user namespace，不需要特权模式） |
| 实现与测试 | worker 修改了 `calc.go`，新增 `calc_test.go`，`go test` 通过 |
| `.git` 写入 | worktree 的 git 目录在 cwd 之外，sandbox 不允许写，模型无法 commit 或改分支 |
| 产物 | runner 在 turn 结束后执行 `git add -A` 和 commit，得到确定的 commit sha 作为 artifact |
| 网络 | workspace-write 下网络是通的（curl 成功） |
| 构建缓存 | `GOCACHE` 在可写根目录之外，worker 只能自己绕到 `/tmp` |

**影响**：
- Artifact 由 runner 生成：turn 完成后，runner 检查工作区、commit，并把 `(分支, commit sha)` 作为不可变的 artifact 引用上报。模型只改工作区，不碰 git 历史。
- runner 启动 worker 时显式设置：可写根目录（构建缓存、临时目录）、网络策略（`sandbox_workspace_write.network_access`），以及 worker 需要的环境变量。
- 在容器中运行的 runner 需要放开 user namespace。开发容器用 `seccomp=unconfined`；生产环境的容器化 runner 改用只放开相关系统调用的自定义 seccomp profile。Windows 和 macOS 上的 runner 使用各自原生的 sandbox，需要在 A9 之后实机验证。

## A8 协议绑定

`generate-json-schema --experimental` 的输出约 5.4MB，v2 下有 345 个定义，其中大量是带标签的联合类型，生成 Go 代码的收益很低。

**决定**：
- `internal/harness/traex` 只手写 tend 用到的消息子集：握手、thread 和 turn 的启动与恢复、inject_items、compact、动态工具调用、tool 回调，以及几类 item 和通知。
- 用脚本从 schema 中抽出这些定义，连同 traex 版本号一起提交为测试数据。
- 写一个测试，校验手写结构体的 JSON 字段与 schema 的 properties 和 required 一致；升级 traex 时重新抽取，漂移会让测试失败。
- 不引入代码生成器和 JSON Schema 校验库。具体实现放在 B12。

## A9 tsnet

复现：`go run ./spikes/tsnet`（见 `spikes/tsnet/README.md`）。在 dev container 中以 ephemeral 节点 `tend-dev` 加入 tailnet，再从 tailnet 内的另一台设备访问它。

| 项 | 结果 |
| --- | --- |
| 加入 tailnet | 在容器内经由 HTTP 代理（tsnet 会读取 `HTTPS_PROXY`）连接控制服务器，大约 3 秒上线；不需要 tailscaled、TUN 设备或特权模式 |
| HTTP + WhoIs | 通过短主机名访问成功；WhoIs 返回访问者的登录用户、节点名和 node key |
| HTTPS | tailnet 已启用证书。证书在第一次 TLS 握手时才申请，第一次握手超时；签发后，之后的握手耗时数十毫秒，证书校验通过 |
| 交叉编译 | 同一程序能为 `windows/amd64` 和 `darwin/arm64` 构建成功；实机入网留到部署第一台非 Linux runner 时验证 |
| tag | 本次使用的 auth key 不带 tag，WhoIs 返回的 `tags` 为空 |

**影响**：
- D23 成立：服务端和 runner 都可以内置 tsnet。
- 服务端启动后主动预取一次证书（例如对自己的 FQDN 发起一次 TLS 握手，或调用 LocalClient 获取证书），避免第一个用户等待证书签发。
- 角色映射优先依据 tag（`tag:tend-server`、`tag:tend-runner`），按用户映射的只有 owner。正式部署需要换成带 tag 的 auth key，并在 tailnet ACL 中定义这些 tag。
- tsnet 的 state 目录必须持久化（开发环境放在 `tend-data` 卷里），否则每次重启都会注册成一个新节点。

## 未完成项

| 项 | 状态 |
| --- | --- |
| A3 hook | 推迟：注入不依赖 hook |
| A9 非 Linux 实机 | 部署第一台 Windows 或 macOS runner 时验证 |
