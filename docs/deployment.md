# 部署说明

服务端和 runner 直接运行在各自的机器上（D39）；owner 通过 Web 与托管主 Agent 对话，也可以使用 `tend` CLI 管理账本（D46、D114）。示例配置见 `docs/examples/`，格式是 HuJSON（D32）。

## tend-server

```sh
make web                           # 构建 Web 客户端，嵌入 tend-server；需要 Node 24 和 pnpm
go build -o tend-server ./cmd/tend-server
TEND_SERVER_SECRET=... TS_AUTHKEY=... ./tend-server -config tend-server.hujson
```

- `TEND_SERVER_SECRET`：至少 32 字节，hex 编码，用来派生 run 凭据（D29）和 owner token（D49）。更换它会让正在执行的 run 失去凭据，所有 token 失效，需要重新 `tend login`。
- `TS_AUTHKEY`：只在第一次加入 tailnet 时需要；之后状态保存在 `data_dir/tsnet`。正式部署使用带 `tag:tend-server` 的 key。
- 监听：加入 tailnet 后，同一组接口同时在 443（https，证书由 tailnet 签发）和 80（http）上提供。启动后会预取一次证书；tailnet 没有开启 HTTPS 证书时日志里有一条警告，这时客户端使用 `http://<hostname>`，WireGuard 仍然加密流量（design.md §6.4）。
- `owners`：可以使用 owner API 的 tailnet 登录名，也就是可以运行 `tend login` 的人（D49）。不加入 tailnet、只用 `listen` 时，用 `local_owner` 指定本地开发的 owner。owner 在 tagged 机器上工作时（tagged 节点没有登录名），用 `owner_nodes` 把这台机器的名字映射到 `owners` 里的登录名（D56）。
- `projects`：可选。project 是 tend 对任务的分组（D52、D58），不等于仓库：`repos` 列出它涉及的仓库，`weight` 是它的任务在排队时的权重（默认 1），`template` 是没有给出工作区的任务使用的工作区模板。不配置时所有任务属于隐含的 `default` project。从配置中删掉的 project 变为 retired：已有任务照常进行，不能再建新任务。修改后重启服务端生效。
- `host_lost_after`：可选，默认 `24h`。任务的工作区只在一台 host 上；这台 host 退役，或超过这段时间没有消息（hello、心跳或领取）时，有待执行动作、或者在那台 host 上有状态未知的 run 的任务以 `workspace_unavailable` 阻塞，并请 owner 选择等待、换一台 host 从头再来或取消（D62）。host 回来后一分钟内自动解除。设为 `0s` 时只对退役的 host 生效；不设为 `0s` 时至少 `10m`，因为空闲 runner 只在领取时被记下，而且每分钟最多记一次。
- 文件原件保存到 `<database-path>.files/`，SQLite 保存清单和关联（D87）。聊天附件和任务交付文件不设应用层文件大小、累计大小或图片分辨率门槛；旧 `deliverables.file_limit_bytes` / `deliverables.result_limit_bytes` 配置仍可读取但不再限制上传。上传与下载流式处理，实际磁盘/IO/网络错误会报告；文件传输不沿用短 RPC 的总时限。
- 升级到存储版本 31 时，旧图片/交付 BLOB 逐个文件导出，在文件完整持久化和 hash 校验成功后才清除对应数据库原文，重启可以继续；SQLite 的旧 BLOB 读取可能加载一个完整历史文件，部署前核对最大旧 BLOB 与可用内存；逐文件迁移避免同时加载所有历史内容，新文件链路没有此限制。迁移期间需为数据库旧页、WAL 和导出文件保留磁盘空间。清空 BLOB 不意味着 SQLite 文件立即缩小，迁移不自动 VACUUM。
- 备份必须包含数据库、`<database-path>.files/` 和原生 agent 历史。在线备份先获得 SQLite 一致快照，再复制快照引用的不可变原件；原件在本轮没有自动 GC。恢复必须将数据库和对应内容目录一起恢复，不得只恢复数据库。原生 session/rollout 继续使用 harness 自身存储，附件不是原生 session 的替代品。
- 上传临时文件在正常失败/取消时清理；硬中断可能留下 `.upload-*`，未产生有效账本引用。清理硬中断遗留项应在确认没有在途上传的维护窗口执行，不按年龄删除可能仍在写入的文件。
- `hosts[].worker`：可选，默认 `appserver`。这台 host 用哪种 harness 运行 worker（D63、D64）：`appserver` 是 traex app-server 加 runner 自建的工作区；`orca` 在 orca 的 worktree 里运行 agent TUI，只支持单仓库工作区。有活动 host 而它们都不支持多仓库时，tend 拒绝建多个 mount 的任务；已有的这类任务在 `tend status` 里显示没有 host 能领取的原因。runner 配置的 `worker.kind` 必须与它一致，否则 runner 的 hello 和领取被拒绝。
- `queue.wait_per_hour`：可选，默认 0.1。排队的动作按分数领取（D60）：`p0` 最先；其余的分数是 project 权重 × 评级分（`p1` 3、`p2` 2、`p3` 1，未评级按 `p2`）加上排队小时数 × 这个系数。默认值下，权重为 1 时等 10 小时相当于升一级；设为 0 则不考虑等待时间。
- `main_agent`：配置后由服务端运行唯一的主协调 Agent，Web 客户端与它对话；工作项后台 Agent 随之启用。未配置时仅提供 owner 管理接口和 runner 调度，不支持外挂 Agent 代替主协调（D114）。
  - 它是服务端启动的 traex app-server，默认使用运行服务端的用户自己的 traex home：用这台机器上 owner 的 traex 鉴权、配置和 MCP server，主会话也出现在 owner 的 traex 历史里。`main_agent.traex_home` 可以改用一个单独的 home，这时需要在那个 home 下完成一次 traex 鉴权。
  - 启动时保留 `TEND_ADDON=off`，仅用于仍指向旧版二进制的遗留配置；它不能让新 CLI 的已退役命令成功。替换 CLI 和启动 Agent 前必须清理已确认的 Tend 外挂 hook/MCP。
  - 权限：sandbox 是 `workspace-write`，可以读整台机器（例如仓库），只能写自己的工作目录（`main_agent.cwd`，默认 `data_dir/main-agent`，必须在任何仓库之外）；需要更多权限的操作由 traex 的 `auto_review` 自动审查，不等人确认。编辑文件、浏览器、插件、应用、skill 和 memory 是关掉的。
  - 模型默认用 traex 的默认模型，`main_agent.model` 可以指定。
  - `main_agent.summary_idle_minutes` 设置预备摘要的空闲分钟，省略默认 50，0 关闭自动准备，范围 0..10080。Web 的独立 owner 设置持久化覆盖该默认值，不依赖模型目录。交接维护仍启用；工具 schema 升级会使用 fresh native thread，Web 对话历史与原 native 历史保留（D81）。
  - `tend conversation handoff --prepare-only` 只准备摘要，`tend conversation handoff` 排队执行交接，`tend conversation handoff --status [--json]` 读取共享设置与摘要状态。网络重试沿用原请求 ID；结果未知时先核对原请求，不重复发起交接。

## 在线添加仓库（D110）

仓库可以通过 owner CLI 注册，无需重启 server。首次在线登记会接管同名启动配置条目；此后登记信息以账本为准，旧配置及配置中的遗漏不会覆盖或退役它。`repo show` 返回当前控制修订；`register --retired` 停止新挂载，但不打断已有任务。已有仓库不能在线替换为不同的 remote 身份，需要另一个逻辑名。

```sh
tend repo register lynx --remote "$REPO_REMOTE" --base main
tend repo show lynx
```

在要承载任务的 runner 主机上，将同一逻辑名绑定到已有 Orca 仓库：

```sh
tend-runner repo bind --config "$RUNNER_CONFIG" \
  --repo lynx --remote "$REPO_REMOTE" --orca "id:$ORCA_REPO_ID"
tend-runner repo status --config "$RUNNER_CONFIG"
```

本机命令经 runner 的私有 Unix socket 执行，支持 Linux/macOS；绑定记录位于 runner home；较长 home 路径通过私有定位文件使用短路径 socket。命令校验实际 origin 并将选择器固定为 provider ID，持久保存成功后在线发布。appserver 模式省略 `--orca`，需要时才创建 runner 自有 bare clone。绑定只允许新增或相同身份登记，不更换已有绑定、工作区或脏文件；已有配置的 prepare profile 保留。

绑定的 remote 地址必须与 server 登记及本地 origin 一致；不推断不同传输地址、`.git` 后缀或 SSH 相对/绝对路径等价。runner 与 server 在 hello 中协商能力，随后在心跳中只报告仓库名与精确 remote 地址的 SHA-256 指纹，不暴露地址、凭据或本机路径。server 确认身份匹配后，`repos_list` 和调度视图才显示这台 host 可以执行该仓库。注册成功、本机绑定成功和 server 已确认可调度是三个可区分的状态；通常在下一次心跳后收敛。旧 runner 继续使用原静态配置；连接旧 server 时新 runner 不发送未协商字段。

每次写入都会输出 request ID 和原修订。网络结果不确定时保留两者；owner CLI 优先查询已有请求结果，不以后来变化的默认值重建已记录操作。尚无记录时以相同参数及 `--request-id`、`--revision` 对账，不能换新身份盲目重做。绑定持久记录包含请求回执，重启后仍可恢复原结果。程序升级仍使用受控发布；在线增加仓库不属于程序升级或 D106 的配置替换维护。

## Web 客户端

owner 在浏览器里打开 tend-server 的地址（`https://<hostname>.<tailnet>.ts.net`）就是 Web 客户端（D70），手机上可以装成 PWA。

- tend-server 在 `/` 提供 Web，`/api/` 仍是 owner API，两者同源，不需要 CORS。Web 是 `web/` 构建出来的静态文件，用 `go:embed` 嵌进 tend-server 的二进制（`internal/webui`），不需要另外部署。
- 先 `make web` 再构建 tend-server。没有构建 Web 时 tend-server 照常构建和运行，页面只显示一条“Web 未构建”的说明；Go 的构建和测试不需要 Node。
- 登录沿用 owner token（D49）：页面打开时按这台设备的 tailnet 身份调用 `POST /api/v1/session` 领取 token，存在浏览器本地，失效后自动重新领取，不需要密码。不是 owner 的设备看到明确的说明。
- 装成 PWA 和离线打开页面外壳需要 HTTPS（service worker 只在安全上下文里可用），也就是 tailnet 开启 HTTPS 证书后的 `https://<hostname>.<tailnet>.ts.net`。只有 http 时 Web 仍可用，只是不能安装。service worker 只缓存页面外壳，API 请求一律走网络。
- 发出的消息先存进浏览器的 IndexedDB 再发送，断网或关掉页面后重新打开时用原来的 request_id 重发，不会重复也不会丢。
- 开发时在 `web/` 里运行 `pnpm dev`，它把 `/api` 代理到本地以 `listen` 和 `local_owner` 运行的 tend-server（默认 `http://127.0.0.1:8640`，可用 `TEND_DEV_SERVER` 指定）。

## tend-runner

```sh
go build -o tend-runner ./cmd/tend-runner
TEND_HOST_TOKEN=... TS_AUTHKEY=... ./tend-runner -config tend-runner.hujson
```

- `TEND_HOST_TOKEN`：任意足够长的随机串；服务端配置里该 host 的 `token_hash` 是它的 SHA-256。
- `worker.kind`：可选，默认 `appserver`，必须与服务端为这台 host 配置的 `worker` 相同。
- worker 使用这台机器上已有的 traex 和用户配置（D38），需要能直接运行 `traex`；git 提交使用这台机器的 git 身份。
- worker 仅通过 run 身份使用执行工具。升级前清理用户级 Tend 外挂配置；runner 仍向子进程提供 `TEND_ADDON=off`，只用于抑制遗留旧版外挂二进制。
- runner 的 tailnet 节点必须匹配服务端为该 host 配置的 `nodes`（tag 或机器名）。

### orca worker（`worker.kind: orca`）

worker 是 orca 里的 agent TUI（D63、D66）：runner 在 orca 里建 worktree、启动 agent，owner 可以在 orca（桌面或手机）里看着它工作，也可以直接接手（D67）。部署前 owner 在这台机器上准备好：

- **orca**：已安装并运行，`orca` 在 PATH 上（或用 `worker.orca_bin` 指定）。
- **Workspace CLI 能力（D78–D80）**：CLI 必须支持 `folder-workspace list/create/show/update/rm` 和 `project-group list/create/rm`。可用独立版本的 CLI，通过 `worker.orca_bin` 指定，无需替换已运行的桌面应用。runner 启动时只读检查能力，成功后才声明 workspace protocol 1；旧 runner 不领取临时目录、复用或显式组合交付任务。
- **仓库登记在 orca 里**：tend 不登记、不删除仓库，只在里面为任务建 worktree（D65），之后也不删除它们。runner 配置里每个仓库用 `repos.<name>.orca` 指明它在 orca 里的登记，例如 `"name:tend"`、`"path:/abs/path"`、`"id:<repoId>"`。tend 建的 worktree 和分支用 orca 的命名，产物记录实际的分支。
- **为 worker 用的 agent 关闭 orca 的 Chat UI**：orca 在 Chat UI 打开时以 chat 启动 agent，tend 只能驱动终端里的 TUI；否则 run 以明确的错误失败。
- **agent 的 orca hook 生效**：orca 从 agent 的 hook 得知它的状态（在做、等待、一轮结束），tend 据此判断一轮是否结束。agent 的配置关掉了 hook 时（例如 Codex 的 `[features]` 里 `hooks = false`），或 orca 的 hook 没有被信任时，orca 看不到 agent，worker 在启动后约两分钟以明确的错误失败。
- **agent 信任这个仓库**：Codex 和 traecli 只对受信任的项目加载项目级配置，worktree 的信任跟随仓库根目录。tend 通过项目级配置给 worker 接上 `submit_verdict` 等工具，并设置无人值守（`approval_policy = "never"`、`sandbox_mode = "danger-full-access"`、tend 工具免批准），不受信任时验收拿不到结论。
- **runner 配置**：`worker.kind: "orca"`，`worker.agent` 为 `codex` 或 `traex`；`model`、`effort` 可选（orca 只在给了 model 时接受 effort）。
- **启动重试**：`worker.startup_attempts` 默认 `1`，设为 `0` 关闭；独立于 `worker.recovery_attempts`，按 run 持久保存。仅对 Orca 明确报告的就绪超时、提示尚未写入且原终端确认停止的情况重新启动；投递或关闭结果未知、已有人工活动、交互阻塞和旧版 Orca 缺失诊断均不自动重试。`worker.startup_failed`、`worker.startup_retry_pending`、`worker.startup_started` 事件记录安全诊断和尝试次数，不包含终端原文或任务提示。
- **有界恢复（D96）**：`worker.recovery_attempts` 默认 `1`，设为 `0` 关闭自动恢复。次数按 run 持久记录，重启不会重置。仅在 Orca 提供可核验的根轮次失败和原会话身份、后台操作已收敛时恢复；加密历史错误使用新会话，临时连接错误或明确的 `rate_limit_exceeded` 续接原会话；不根据普通 HTTP 400 推断可恢复，不切换模型或 provider。无法确认的工具或部署结果进入待决，不自动重放。旧 Orca 缺失这些事实时不会猜测会话或依据静默时长重启终端。

tend 在仓库里留下的东西：

- 每个 run 期间，worktree 里有 `.codex/config.toml` 或 `.trae/traecli.toml`（无人值守设置、关闭启动时的更新检查、tend 的 MCP server），worktree 的 git 目录里有 `tend-worker.json`；run 结束时删除。仓库里已有同名而不是 tend 写的文件时，run 失败，不会覆盖。
- 仓库共用的 `info/exclude`（`git rev-parse --git-path info/exclude`）里加了 `/.codex/config.toml`、`/.trae/traecli.toml` 两行，worker 的提交和产物不会带上它们。linked worktree 与主 checkout 共用这个文件，所以 owner 主 checkout 里同名的未跟踪文件也会被 `git status` 忽略。
- 任务的 Git worktree 保留，owner 可以在 orca 里查看；run 结束时关闭并确认停止该 run 的 agent。人工接管或无法确认身份、停止状态时保留占用，不能把资源交给另一个任务。

### Workspace 资源与升级（D78–D80）

升级顺序是具备 folder workspace 能力的 Orca CLI、兼容服务端、runner，最后使用新任务能力。迁移前停止写入，备份服务端 SQLite 数据库及每台 runner 的 journal；恢复时使用匹配数据库版本的二进制和备份，不能用旧二进制直接打开升级后的账本。

`temporary` 任务不需要登记仓库，每个资源在 `<runner.home>/temporary-workspaces/<workspace_id>/` 使用独立目录，并在 Orca 里保留可接手的 folder workspace。非 Git 任务的 MCP 凭据放在 runner 私有目录，不写入交付目录。临时资源在最后一个任务终止、所有运行确认停止后保留七天；排队复用、人工接管、待决和未知运行会阻止清理。文件和报告持久保存在服务端，目录清理不删除交付物。

`reuse` 按来源任务绑定稳定资源并固定原主机，保留分支、文件和脏状态；资源缺失时进入待决，不静默重建。Orca 显示名为当前任务标题和短任务 ID，身份匹配使用持久化的 provider ID、路径与实例信息。

历史任务迁移时保留原占用，即使账本已完成。runner 根据 journal 和提供方记录核对所有历史运行，只在能确认属于对应 run 且已停止时释放；无法确认或已有人接手的终端继续保留。升级后应检查 host 的 `workspace_protocol` 为 1、原任务和资源数量、待决原因及运行状态；不能仅凭任务完成状态人工清空占用。切换 harness 不授予新 harness 删除旧 provider 资源的权限；找不到原适配器或目录已有其他注册时暂停清理，其他资源的释放继续进行。已完成 worker 的结果会先存入 journal，恢复时不重新启动该 worker。

Orca 启动的 agent 继承 Orca 的环境。升级前必须清理已确认的 Tend 用户级外挂 hook/MCP；工作区 marker 仅保留资源身份用途，不能代替清理。保留 runner 为当前 run 安装的 `tend_worker` 桥。

owner 在 orca 里给 worker 的 agent 发一句话或按 Esc，就是接管（D67）：tend 察觉后任务转为 `human_control`，不再判断、打断或推进这个 agent；`tend takeover <task>` 可以显式接管。处理完后 `tend resume <task>` 交还，worker 先检查工作区再完成任务。

## owner CLI

在 owner 的设备上：

```sh
go build -o tend ./cmd/tend        # 放到 PATH 上
tend login --server https://<hostname>.<tailnet>.ts.net
```

- 配置和凭据在 `~/.config/tend/`（可用 `TEND_CONFIG_DIR` 指定），凭据文件只有本人可读。token 有效期 30 天，过期后重新 `tend login`。
- `login` 仅领取 owner token。旧 agent token 不再有效，旧 `setup`、`mcp`、`hook` 命令和 `/api/v1/agent/` 接口已移除；旧版客户端需升级后重新登录，不能自动转为 owner 身份重试。
- 升级时清理已确认由 Tend 安装的用户级 `mcp_servers.tend`、对应 Tend hook 和工具放行项；保留其他 MCP、hook 与个人配置。runner 生成的 `tend_worker` MCP 不属于此清理范围。
- owner 命令：`tend status [--project <p>]`、`tend task <id>`、`tend logs <run_id> [-f]`、`tend decide`、`tend retry`、`tend rate <id> <p0..p3>`、`tend pause|resume|cancel`，都支持 `--json`。`tend status` 按 project 分组，列出每个 project 的权重、运行中和排队的数量、未结束的任务，以及排队的动作在整个队列里的位置、评级、等待时间和分数（D60）。

## tailnet ACL

只允许 owner 的设备访问 `tag:tend-server` 的 443 和 80 端口，只允许 `tag:tend-runner` 访问 `tag:tend-server` 的 80 端口；runner 之间不互通（design.md §6.2）。使用 https 需要在 tailnet 中开启 MagicDNS 和 HTTPS 证书。ACL 由管理员在 tailnet 中配置，程序不修改。
