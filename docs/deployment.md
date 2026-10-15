# 部署说明（阶段 B）

服务端和 runner 直接运行在各自的机器上（D39）。示例配置见 `docs/examples/`，格式是 HuJSON（D32）。

## tend-server

```sh
go build -o tend-server ./cmd/tend-server
TEND_SERVER_SECRET=... TS_AUTHKEY=... ./tend-server -config tend-server.hujson
```

- `TEND_SERVER_SECRET`：至少 32 字节，hex 编码，用来派生 run 凭据（D29）。更换它会让正在执行的 run 失去凭据。
- `TS_AUTHKEY`：只在第一次加入 tailnet 时需要；之后状态保存在 `data_dir/tsnet`。正式部署使用带 `tag:tend-server` 的 key。
- `owners`：可以使用 Web 和 owner API 的 tailnet 登录名（D42）。不加入 tailnet、只用 `listen` 时，用 `local_owner` 指定本地开发的 owner。
- 主 Agent 使用专用的 traex home（`main_agent.traex_home`），需要在那个 home 下完成一次 traex 鉴权。

## tend-runner

```sh
go build -o tend-runner ./cmd/tend-runner
TEND_HOST_TOKEN=... TS_AUTHKEY=... ./tend-runner -config tend-runner.hujson
```

- `TEND_HOST_TOKEN`：任意足够长的随机串；服务端配置里该 host 的 `token_hash` 是它的 SHA-256。
- worker 使用这台机器上已有的 traex 和用户配置（D38），需要能直接运行 `traex`；git 提交使用这台机器的 git 身份。
- runner 的 tailnet 节点必须匹配服务端为该 host 配置的 `nodes`（tag 或机器名）。

## tailnet ACL

只允许用户设备访问 `tag:tend-server`，只允许 `tag:tend-runner` 访问 `tag:tend-server` 的 80 端口；runner 之间不互通（design.md §6.2）。ACL 由管理员在 tailnet 中配置，程序不修改。

阶段 B 还没有面向用户的 HTTP API 和 Web；它们在阶段 C 加入。
