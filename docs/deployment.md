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
- `owners`：可以使用 owner API 的 tailnet 登录名，也就是可以运行 `tend login` 的人（D49）。不加入 tailnet、只用 `listen` 时，用 `local_owner` 指定本地开发的 owner。
- `main_agent`：可选，默认不配置。主 Agent 是 owner 自己使用的 agent（见下文 `tend` CLI）；配置了 `main_agent` 时服务端另外运行一个托管的主 Agent（D45，目前不再开发）。它使用专用的 traex home（`main_agent.traex_home`），需要在那个 home 下完成一次 traex 鉴权。

## tend-runner

```sh
go build -o tend-runner ./cmd/tend-runner
TEND_HOST_TOKEN=... TS_AUTHKEY=... ./tend-runner -config tend-runner.hujson
```

- `TEND_HOST_TOKEN`：任意足够长的随机串；服务端配置里该 host 的 `token_hash` 是它的 SHA-256。
- worker 使用这台机器上已有的 traex 和用户配置（D38），需要能直接运行 `traex`；git 提交使用这台机器的 git 身份。
- 这台机器上即使也按下文接入了 tend，worker 也不会把自己当作主 Agent：runner 为 worker 设置 `TEND_ADDON=off`，tend 的 hook 和 MCP 看到它时什么都不做（D45）。
- runner 的 tailnet 节点必须匹配服务端为该 host 配置的 `nodes`（tag 或机器名）。

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
  - Codex、traex：在各自配置目录的 `hooks.json` 里加上两个 hook，在 `config.toml` / `traecli.toml` 里追加 `[mcp_servers.tend]`。**第一次启动 TUI 时要信任这两个 hook**，否则它们不会运行。
- hook 命令按路径写入：`tend` 在 PATH 上时写 `tend`，否则写绝对路径。Codex 和 traex 按命令记录信任，换了路径要重新信任，所以升级时放回同一个位置。
- hook 等 tend 最多 3 秒（`TEND_HOOK_TIMEOUT` 可调），超时或 tend 不可达时只注入一行提示，不会挡住输入。
- owner 命令：`tend status`、`tend task <id>`、`tend logs <run_id> [-f]`、`tend decide`、`tend retry`、`tend pause|resume|cancel`，都支持 `--json`。

## tailnet ACL

只允许 owner 的设备访问 `tag:tend-server` 的 443 和 80 端口，只允许 `tag:tend-runner` 访问 `tag:tend-server` 的 80 端口；runner 之间不互通（design.md §6.2）。使用 https 需要在 tailnet 中开启 MagicDNS 和 HTTPS 证书。ACL 由管理员在 tailnet 中配置，程序不修改。
