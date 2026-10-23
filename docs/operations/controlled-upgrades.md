# 受控升级操作

实现依据 D82–D85。自动安装目前支持 Linux；其他平台可以构建，但不会执行进程替换。发布由固定 Go 更新器执行，模型不参与停止、替换或恢复进程。

## 首次接入

无版本旧 runner 不具备控制接口，首次接入不能伪装为自动升级。先确认主会话没有活动 turn、每台 runner 没有活跃或未知 run、未交付结果、未确认领取或工作目录操作；保存原始程序、配置和匹配数据备份。先安装支持协议 1 的 server，再逐台安装支持协议 1 的 runner。新 server 仍接收 legacy0；新 runner 不向旧 server 静默回退。

server 配置增加 `upgrades`，包含 `instance`（逻辑服务名，默认 `server`）、`control_socket`、`updater_socket`、`release_dir`；可配置 `drain_timeout`（默认 `30m`）、`restart_budget`（默认 `120s`）、`grant_duration`（默认 `300s`）。这些是本机私有路径，配置不提交仓库。

runner 配置同时增加 `updater_socket`（自己的 authority socket）、`updater_control_socket`（对应 updater 的 control socket）、`updater_bundle_dir`（私有下载目录）。逻辑 updater instance 必须与 host ID 一致。保留现有 worker、gateway、槽位和数据目录配置。

每个受管进程配置一个 updater：

- `role` 为 `server` 或 `runner`，`instance_id` 为对应逻辑服务名。
- `state_dir`、`initial_binary`、`args`、`log_path` 固定指向已核实的程序和配置；`env_files` 指向原凭据文件，状态和日志不复制凭据。
- `data_files` 只列出该服务的数据库或 journal；SQLite sidecars 一并处理。不包含配置、凭据和其他服务数据。
- `authority_socket` 指向被管服务的私有 authority socket；`authorize_url`、`health_url` 分别为 `http://localhost/updater/authorize`、`http://localhost/updater/health`，请求通过 Unix socket 发出。
- server updater 的 `coordinator_socket` 指向 server `control_socket`。runner 使用自身已有 tsnet 连接向 server 转发授权，不复制节点身份。
- 首次 `protocol`、`storage_version` 填当前实际版本。后续成功安装会持久化新版本，回退恢复匹配版本，不持续依赖首次配置。

先运行 `tend-updater serve --config "$UPDATER_CONFIG"`，再以 `bootstrap --config "$UPDATER_CONFIG" --pid "$SERVICE_PID"` 收养已确认空闲的进程。更新器验证实际程序、进程启动身份和参数，并保存初始版本；每个配置只能由一个 updater 持有。

本期不提供绕过排空的人工 `restart`。该命令会明确拒绝；意外退出后的同版本恢复只重启已证实退出的进程，不向运行中服务发送停止信号。

## 发布与执行

发布目录包含 `manifest.json` 和其中列出的 server/runner 二进制。manifest 固定 `format: 1`、`release_id`、`protocol: {min,max}`、`storage.server_schema`、`storage.runner_journal` 以及 `artifacts`；每项产物有 `role`、`os`、`arch`、相对 `path`、`size` 和小写 `sha256`。

`protocol.max` 是发布当前协议，`min` 必须为上一代（首次为 0）；存储范围的 `max` 是该二进制当前迁移版本，`min` 是能升级读取的最旧版本。已提交迁移不可修改。发布目录不得包含符号链接或路径逃逸。实际服务凭据、数据和工作目录不放入 bundle。

先校验全部文件：

```sh
tend-updater verify --release "$RELEASE_DIR"
```

再从 server 本机启动单个批次：

```sh
tend upgrade start --socket "$SERVER_CONTROL_SOCKET" \
  --release "$RELEASE_DIR" --upgrade-id "$UPGRADE_ID" --json
```

也可用 server updater 的 `upgrade --config "$UPDATER_CONFIG" --release "$RELEASE_DIR" --upgrade-id "$UPGRADE_ID"`，它同样进入 server 的批次协调接口。CLI 会先输出 request ID；不确定回执先读状态，不要换 ID 重发。相同请求重试必须保留批次、发布和 request ID。

准入拒绝未结束批次、离线或不兼容主机、无 updater 能力、无法读取的存储以及仍依赖退役协议的 run。server 等当前主会话结束；期间 owner 消息继续持久入队。随后固定本轮现有 run 的维护授权并等待 runner 确认，确认剩余覆盖足够才允许停 server。普通心跳不延长这次固定授权。runner 用完整请求往返耗时和本地保守折扣确认覆盖期限；停止授权的截止时间再扣除整个 server 重启预算。获取授权后暂停过久的 updater 不得停止服务。

新 server 健康后恢复领取并按空闲优先逐台维护 runner。维护只等待当前 run 与交付/资源操作，不等待整项任务：后续验收仍在原工作目录排队。替换期间保留另一台可接单主机。候选以维护状态启动，确认配置、journal、provider 和服务端握手，并取得两次不同的健康心跳后恢复领取；相同 runner 哈希跳过替换。

```sh
tend upgrade status --socket "$SERVER_CONTROL_SOCKET" --json
tend-updater status --config "$UPDATER_CONFIG"
```

Web 的 Chat 和看板显示批次入口；主机在线、维护与 slot 分别显示。开始、单台恢复、完成、异常和取消写持久 system 消息，不自动调用模型。下载和细粒度状态放在发布详情。

## 暂停、恢复与数据边界

排空超时会暂停批次，恢复尚未授权替换的旧进程接单；已有候选、失联或未知安装阶段保持维护，等待对账。其他主机离线、校验失败和健康未知也不会触发强杀或猜测回滚。

读取当前 revision 后控制批次：

```sh
tend upgrade retry --socket "$SERVER_CONTROL_SOCKET" \
  --upgrade-id "$UPGRADE_ID" --revision "$REVISION"
tend upgrade cancel --socket "$SERVER_CONTROL_SOCKET" \
  --upgrade-id "$UPGRADE_ID" --revision "$REVISION"
```

已经确认回退的批次会恢复旧版本服务并保留失败候选事实；先取消本批次，再用新批次重新安装。只有绑定当前进程、原候选和旧哈希的 updater 终态回执能解除该维护门禁；仅观察到旧哈希不足以证明回退成功。

取消后会对账尚未安装的本地 updater 操作；下一批升级要等各主机心跳确认取消，避免遗留 staged 操作。已经完成的主机不因历史授权过期阻止后续主机重试。旧进程已停止且持久阶段确认候选尚未启动时，授权过期会恢复旧进程；候选启动结果未知时仍须对账。

过期授权不能靠 retry 或重复心跳续期。取消不反向降级已经完成的主机，且不能在候选状态未知时把主机直接恢复接单。运行中的新 server 一旦确认响应，就不能恢复旧数据库；新 runner 一旦恢复领取，就不能恢复旧 journal。日志、任务、交付物及历史会话继续保留，无法确认的外部操作按 unknown 对账。

演练需要核对实际 PID、程序哈希、批次和 run 身份，以及两台主机之间没有重叠维护。每次测试只清理本次创建的 Orca workspace 与临时目录，先确认 run 释放，保留任务账本和交付证据。
