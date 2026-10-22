# 部署说明

服务端和 runner 直接运行在各自的机器上（D39）；owner 在自己的设备上安装 `tend` CLI，把自己使用的 agent 接入 tend（D45、D46）。示例配置见 `docs/examples/`，格式是 HuJSON（D32）。

## tend-server

```sh
go build -o tend-server ./cmd/tend-server
TEND_SERVER_SECRET=... TS_AUTHKEY=... ./tend-server -config tend-server.hujson
```

- `TEND_SERVER_SECRET`：至少 32 字节，hex 编码，用来派生 run 凭据（D29）和 owner、agent 的 token（D49）。更换它会让正在执行的 run 失去凭据，所有 token 失效，需要重新 `tend login`。
- `TS_AUTHKEY`：只在第一次加入 tailnet 时需要；之后状态保存在 `data_dir/tsnet`。正式部署使用带 `tag:tend-server` 的 key。
- 监听：加入 tailnet 后，同一组接口同时在 443（https，证书由 tailnet 签发）和 80（http）上提供。启动后会预取一次证书；tailnet 没有开启 HTTPS 证书时日志里有一条警告，这时客户端使用 `http://<hostname>`，WireGuard 仍然加密流量（design.md §6.4）。
- `owners`：可以使用 owner API 的 tailnet 登录名，也就是可以运行 `tend login` 的人（D49）。不加入 tailnet、只用 `listen` 时，用 `local_owner` 指定本地开发的 owner。owner 在 tagged 机器上工作时（tagged 节点没有登录名），用 `owner_nodes` 把这台机器的名字映射到 `owners` 里的登录名（D56）。
- `projects`：可选。project 是 tend 对任务的分组（D52、D58），不等于仓库：`repos` 列出它涉及的仓库，`weight` 是它的任务在排队时的权重（默认 1），`template` 是没有给出工作区的任务使用的工作区模板。不配置时所有任务属于隐含的 `default` project。从配置中删掉的 project 变为 retired：已有任务照常进行，不能再建新任务。修改后重启服务端生效。
- `host_lost_after`：可选，默认 `24h`。任务的工作区只在一台 host 上；这台 host 退役，或超过这段时间没有消息（hello、心跳或领取）时，有待执行动作、或者在那台 host 上有状态未知的 run 的任务以 `workspace_unavailable` 阻塞，并请 owner 选择等待、换一台 host 从头再来或取消（D62）。host 回来后一分钟内自动解除。设为 `0s` 时只对退役的 host 生效；不设为 `0s` 时至少 `10m`，因为空闲 runner 只在领取时被记下，而且每分钟最多记一次。
- `hosts[].worker`：可选，默认 `appserver`。这台 host 用哪种 harness 运行 worker（D63、D64）：`appserver` 是 traex app-server 加 runner 自建的工作区；`orca` 在 orca 的 worktree 里运行 agent TUI，只支持单仓库工作区。有活动 host 而它们都不支持多仓库时，tend 拒绝建多个 mount 的任务；已有的这类任务在 `tend status` 里显示没有 host 能领取的原因。runner 配置的 `worker.kind` 必须与它一致，否则 runner 的 hello 和领取被拒绝。
- `queue.wait_per_hour`：可选，默认 0.1。排队的动作按分数领取（D60）：`p0` 最先；其余的分数是 project 权重 × 评级分（`p1` 3、`p2` 2、`p3` 1，未评级按 `p2`）加上排队小时数 × 这个系数。默认值下，权重为 1 时等 10 小时相当于升一级；设为 0 则不考虑等待时间。
- `main_agent`：可选，默认不配置。主 Agent 是 owner 自己使用的 agent（见下文 `tend` CLI）；配置了 `main_agent`（可以是空对象 `{}`）时，服务端另外运行一个托管的主 Agent，Web 客户端和它对话，与外挂的主 Agent 并存（D68）。
  - 它是服务端启动的 traex app-server，默认使用运行服务端的用户自己的 traex home：用这台机器上 owner 的 traex 鉴权、配置和 MCP server，主会话也出现在 owner 的 traex 历史里。`main_agent.traex_home` 可以改用一个单独的 home，这时需要在那个 home 下完成一次 traex 鉴权。
  - owner 的 traex 里接入了 tend（`tend setup`）时，托管会话不会把自己当作外挂会话：服务端为它设置 `TEND_ADDON=off`，不需要另外配置。
  - 权限：sandbox 是 `workspace-write`，可以读整台机器（例如仓库），只能写自己的工作目录（`main_agent.cwd`，默认 `data_dir/main-agent`，必须在任何仓库之外）；需要更多权限的操作由 traex 的 `auto_review` 自动审查，不等人确认。编辑文件、浏览器、插件、应用、skill 和 memory 是关掉的。
  - 模型默认用 traex 的默认模型，`main_agent.model` 可以指定。

## tend-runner

```sh
go build -o tend-runner ./cmd/tend-runner
TEND_HOST_TOKEN=... TS_AUTHKEY=... ./tend-runner -config tend-runner.hujson
```

- `TEND_HOST_TOKEN`：任意足够长的随机串；服务端配置里该 host 的 `token_hash` 是它的 SHA-256。
- `worker.kind`：可选，默认 `appserver`，必须与服务端为这台 host 配置的 `worker` 相同。
- worker 使用这台机器上已有的 traex 和用户配置（D38），需要能直接运行 `traex`；git 提交使用这台机器的 git 身份。
- 这台机器上即使也按下文接入了 tend，worker 也不会把自己当作主 Agent：runner 为 worker 设置 `TEND_ADDON=off`，tend 的 hook 和 MCP 看到它时什么都不做（D45）。
- runner 的 tailnet 节点必须匹配服务端为该 host 配置的 `nodes`（tag 或机器名）。

### orca worker（`worker.kind: orca`）

worker 是 orca 里的 agent TUI（D63、D66）：runner 在 orca 里建 worktree、启动 agent，owner 可以在 orca（桌面或手机）里看着它工作，也可以直接接手（D67）。部署前 owner 在这台机器上准备好：

- **orca**：已安装并运行，`orca` 在 PATH 上（或用 `worker.orca_bin` 指定）。
- **仓库登记在 orca 里**：tend 不登记、不删除仓库，只在里面为任务建 worktree（D65），之后也不删除它们。runner 配置里每个仓库用 `repos.<name>.orca` 指明它在 orca 里的登记，例如 `"name:tend"`、`"path:/abs/path"`、`"id:<repoId>"`。tend 建的 worktree 和分支用 orca 的命名，产物记录实际的分支。
- **为 worker 用的 agent 关闭 orca 的 Chat UI**：orca 在 Chat UI 打开时以 chat 启动 agent，tend 只能驱动终端里的 TUI；否则 run 以明确的错误失败。
- **agent 的 orca hook 生效**：orca 从 agent 的 hook 得知它的状态（在做、等待、一轮结束），tend 据此判断一轮是否结束。agent 的配置关掉了 hook 时（例如 Codex 的 `[features]` 里 `hooks = false`），或 orca 的 hook 没有被信任时，orca 看不到 agent，worker 在启动后约两分钟以明确的错误失败。
- **agent 信任这个仓库**：Codex 和 traecli 只对受信任的项目加载项目级配置，worktree 的信任跟随仓库根目录。tend 通过项目级配置给 worker 接上 `submit_verdict` 等工具，并设置无人值守（`approval_policy = "never"`、`sandbox_mode = "danger-full-access"`、tend 工具免批准），不受信任时验收拿不到结论。
- **runner 配置**：`worker.kind: "orca"`，`worker.agent` 为 `codex` 或 `traex`；`model`、`effort` 可选（orca 只在给了 model 时接受 effort）。

tend 在仓库里留下的东西：

- 每个 run 期间，worktree 里有 `.codex/config.toml` 或 `.trae/traecli.toml`（无人值守设置、关闭启动时的更新检查、tend 的 MCP server），worktree 的 git 目录里有 `tend-worker.json`；run 结束时删除。仓库里已有同名而不是 tend 写的文件时，run 失败，不会覆盖。
- 仓库共用的 `info/exclude`（`git rev-parse --git-path info/exclude`）里加了 `/.codex/config.toml`、`/.trae/traecli.toml` 两行，worker 的提交和产物不会带上它们。linked worktree 与主 checkout 共用这个文件，所以 owner 主 checkout 里同名的未跟踪文件也会被 `git status` 忽略。
- 任务的 worktree 和 worker 的终端在 run 结束后保留，owner 可以在 orca 里查看；取消或放弃的 run 的终端会被关闭。

orca 里启动的 agent 继承 orca 的环境而不是 runner 的，runner 无法给它设置 `TEND_ADDON=off`；tend 的 hook 和 MCP 看到当前 worktree 的 git 目录里有 `tend-worker.json` 时同样什么都不做（D45）。

owner 在 orca 里给 worker 的 agent 发一句话或按 Esc，就是接管（D67）：tend 察觉后任务转为 `human_control`，不再判断、打断或推进这个 agent；`tend takeover <task>` 可以显式接管。处理完后 `tend resume <task>` 交还，worker 先检查工作区再完成任务。

## tend CLI 与 agent 接入

在 owner 的设备上：

```sh
go build -o tend ./cmd/tend        # 放到 PATH 上
tend login --server https://<hostname>.<tailnet>.ts.net
tend setup claude                  # 只显示要做的修改
tend setup claude --write          # 写入；codex、traex 同理
```

- 配置和凭据在 `~/.config/tend/`（可用 `TEND_CONFIG_DIR` 指定），凭据文件只有本人可读。token 有效期 30 天，过期后重新 `tend login`。
- `tend setup` 每次都先显示 diff，重复执行不会重复添加：
  - Claude Code：在 `settings.json` 里加上 SessionStart 和 UserPromptSubmit 两个 hook，放行 `mcp__tend__*` 工具；`--write` 时用 `claude mcp add --scope user` 注册 MCP server。
  - Codex：在配置目录的 `hooks.json` 里加上两个 hook，在 `config.toml` 里追加 `[mcp_servers.tend]`。
  - traex：在 `traecli.toml` 里追加两个 `[[hooks.<Event>]]` 表和 `[mcp_servers.tend]`（traex 不读 `hooks.json`）。如果已经用 `SessionStart = [...]` 这类写法定义了同名事件，`tend setup` 不改动，只打印需要手动合并的片段。**第一次启动 TUI 时要信任这两个 hook**，否则它们不会运行。
- hook 命令按路径写入：`tend` 在 PATH 上时写 `tend`，否则写绝对路径。Codex 和 traex 按命令记录信任，换了路径要重新信任，所以升级时放回同一个位置。
- 只有用过 tend 的会话才会收到 tend 的上下文（D54）：会话第一次调用 tend 的工具之后，hook 才开始注入快照和增量。在一个会话里让 agent 使用 tend 时，它会先调用 `tend_context`。agent 的后台会话和与 tend 无关的会话不受影响。
- hook 等 tend 最多 3 秒（`TEND_HOOK_TIMEOUT` 可调），超时或 tend 不可达时只注入一行提示，不会挡住输入。
- owner 命令：`tend status [--project <p>]`、`tend task <id>`、`tend logs <run_id> [-f]`、`tend decide`、`tend retry`、`tend rate <id> <p0..p3>`、`tend pause|resume|cancel`，都支持 `--json`。`tend status` 按 project 分组，列出每个 project 的权重、运行中和排队的数量、未结束的任务，以及排队的动作在整个队列里的位置、评级、等待时间和分数（D60）。

## tailnet ACL

只允许 owner 的设备访问 `tag:tend-server` 的 443 和 80 端口，只允许 `tag:tend-runner` 访问 `tag:tend-server` 的 80 端口；runner 之间不互通（design.md §6.2）。使用 https 需要在 tailnet 中开启 MagicDNS 和 HTTPS 证书。ACL 由管理员在 tailnet 中配置，程序不修改。
