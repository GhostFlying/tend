# 受控升级操作

实现依据 D82–D85。自动安装目前支持 Linux；其他平台可以构建，但不会执行进程替换。发布由固定 Go 更新器执行，模型不参与停止、替换或恢复进程。

## 信任边界与故障模型

本流程用于同一 owner 管理的可信 server、runner 和 updater。服务用户能够修改程序、配置和本地状态；恶意同 UID 进程、root 或恶意 runner 不属于此流程提供的隔离边界。已有节点鉴权和本地私有 socket 用于选择正确的服务入口。

恢复逻辑处理进程崩溃或重启、网络断开、请求重复或迟到、发布产物不完整，以及领取任务和升级之间的并发。批次、进程和程序哈希用于识别旧回执与误接的进程；健康与持久提交分开，是为了避免回退覆盖已接收的新任务。收到停止或启动结果不明时，只暂停依赖该结果的切换步骤，继续保留现有服务和任务事实。

本地状态写入失败时，updater 保持待处理，不发布未持久化的终态证明，也不继续外部切换。修复磁盘等原因后，重新启动 updater 即可从已保存状态对账；这不要求停止仍运行的受管服务。正常的心跳确认间隔和已恢复接单后的活动任务不会被视作升级故障。

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

新 server 健康后恢复领取并按空闲优先逐台维护 runner。维护只等待当前 run 与交付/资源操作，不等待整项任务：后续验收仍在原工作目录排队。替换期间保留另一台可接单主机。候选以维护状态启动，确认配置、journal、provider 和服务端握手。两次不同的健康心跳只允许本机 updater 提交；server 收到持久提交证明后才恢复领取；相同 runner 哈希跳过替换。

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

候选在阶段超时后恢复健康，仍会按原进程身份、程序哈希和两次健康观察进行对账。确认后的 server 恢复主会话和 runner 领取，确认后的 runner 恢复接单；批次继续保持暂停，不自动安装下一台。读取更新后的 revision，再选择 retry 或 cancel。此过程不续签停止授权、不重做安装。恢复旧版后的自动同版本重启会保留回退证明，并将其绑定到已确认的新进程。

提交证明同样沿已确认的同版本重启保留原候选身份，避免恢复后的新进程被误认为无关安装。选择回退后，updater 必须持久保存绑定 control revision 的 admission fence，停止仍存活候选前再次核对；无法确认则保持待处理。已持久提交的版本不可恢复旧数据，取消也不能绕过提交或回退证明。

过期授权不能靠 retry 或重复心跳续期。取消不反向降级已经完成的主机，且不能在候选状态未知时把主机直接恢复接单。运行中的新 server 一旦确认响应，就不能恢复旧数据库；新 runner 一旦恢复领取，就不能恢复旧 journal。日志、任务、交付物及历史会话继续保留，无法确认的外部操作按 unknown 对账。

停止授权已提交但回执丢失时，updater 可能仍停在 staged。批次暂停后，协调器通过 staged 比较交换取消；完整持久 cancelled 证明与当前旧进程匹配后，恢复未执行替换的服务。本批次保留暂停，取消后以新批次重新升级。该证明表示替换未开始，不表示执行过数据库回退。已进入 stopping 或后续阶段的操作不会通过此路径解除维护，继续原有进程对账。

演练需要核对实际 PID、程序哈希、批次和 run 身份，以及两台主机之间没有重叠维护。每次测试只清理本次创建的 Orca workspace 与临时目录，先确认 run 释放，保留任务账本和交付证据。

## Runner 进程代次文件

runner 在 journal 旁维护 `<journal>.incarnation` companion SQLite 文件，分配
跨进程严格递增的 Generation。该文件不含业务数据或凭据；必须跨候选回滚保留，
不得加入 updater 的业务 `data_files` 恢复集合，也不要为重试启动而删除它。

文件丢失或损坏时，先停止该服务的 runner/updater 启动重试并保留现场。优先
恢复较新的 companion 备份；无备份时，管理员确认旧服务进程均已停止后读取
server 当前 Runtime.Generation，离线恢复 companion 计数至不小于该高水位，
下一次启动会分配更高代次。不得通过自动追赶 server 代次重试旧进程，否则
迟到的旧进程也可重新抢占。具体实现和回归边界见
[进程代次计划](../plans/runner-incarnation-ordering.md)。

## 终态资源隔离不等于 runner 未排空

任务终态、所有关联 run 本地 done 且身份匹配，没有 held/fenced、活动 executor、
待确认领取/交付/释放或其他资源操作时，已知旧 app-server UUID 缺成功最终报告的
静态证明不足只保留为资源隔离告警，不阻止 runner 自身替换。该判断不要求 thread
或 worker 字段为空，也不
将旧失败 run 的缺失证明当作仍在执行。服务端有开放或 unknown run、任务未终态、
缺少 journal、身份冲突、实际 harness 调用失败或当前存在待确认操作时，维护仍被
阻塞。原生 thread 内保存的未确认 launch/send 同样是待确认操作，不能仅因
journal requests 表为空而豁免。

资源正常对账不变：能取得真实停止证明的资源继续释放；无法确认的资源仍由原
任务占用，不能复用或清理。告警不表示原生进程已经停止，不会伪造 StoppedConfirmed
或为失败对账创建释放意图。journal 读写错误仍是维护故障，不因资源隔离而忽略。
升级 runner 不会恢复已 done 的历史 run，也不能作为关闭原生终端的授权。
具体边界及回归见[实施计划](../plans/terminal-resource-maintenance.md)。

## 单主机 provider 配置维护（D106）

首次使用前以普通发布安装支持 `runner-config-maintenance-v1` 的 runner 和 server，
并显式升级固定 updater。停止 updater 不应停止受管 runner；先核对服务管理器的
进程边界、持久状态和实际进程身份，保留旧 updater，再换更新器。不得用停止 runner
来安装这项能力。目标 updater 增加 `service_config`，必须与已有 `args` 的 `-config`
路径完全相同；不改变 runner 配置本身。

worker 只准备私有候选和证据，外部 operator 负责启动批次及等待完成。执行任务若
运行在目标 runner 上，应先安排它结束，或按 owner 的维护授权暂停并正式收敛 run。
worker 等待提问仍是活动 run，不满足排空条件。配置维护冻结整个目标主机的新 claim，
不只冻结该任务；验收只能在维护完成恢复领取后启动。

本期只允许 `delivery_observer` 的差异，provider 可执行文件必须为显式绝对路径。
固定 runner 的 `check-config OLD CANDIDATE` 只解析配置并解析可执行文件路径，不登录
或访问平台，也不证明真实账号认证。updater 暂存前强制调用这个模式。Codebase 观察
调用设置 `BYTEDCLI_NO_AUTO_UPGRADE=1`，避免只读查询触发 CLI 自更新。

准备包含**当前实际 server 和所有 runner 原字节**的新 manifest，存储和协议版本不变；
唯一的新 `release_id` 与配置批次关联，不通过修改 build ID 制造一次程序升级。配置
内容不放进 bundle。元数据文件仅包含目标主机及原/新文件哈希和要求的 provider，例如：

```json
{"runner-a":{"previous_sha256":"<old-sha256>","sha256":"<candidate-sha256>","providers":["github","codebase"]}}
```

在目标主机使用新 updater CLI 暂存，沿用同一批次 ID 重试：

```sh
tend-updater stage-config --config "$UPDATER_CONFIG" \
  --upgrade-id "$BATCH_ID" --manifest-sha256 "$MANIFEST_SHA256" \
  --candidate "$PRIVATE_CANDIDATE" --previous-sha256 "$OLD_SHA256" \
  --sha256 "$CANDIDATE_SHA256" --providers github,codebase
```

原配置和候选必须是权限不超过 0600、至多 1 MiB 的普通文件。updater 固定目标路径，
保存不可变候选和备份；这里只做准备，不修改运行配置。随后在 server 本机执行：

```sh
tend upgrade start --release "$RELEASE_DIR" --upgrade-id "$BATCH_ID" \
  --runner-configurations "$METADATA_FILE" --json
```

配置批次跳过 server 重启，在创建批次的事务内冻结指定主机 claim。其余主机保持正常，
目标等待所有 run、未确认领取及资源/交付操作完成。旧进程报告实际加载的原配置哈希；
server 授权后，updater 停旧进程、备份数据、原子应用配置并启动相同程序。新进程报告
实际解析字节的哈希，server 确认 required provider、两次不同健康心跳，updater 再
持久提交。服务端恢复 normal 并收到确认后批次完成。验收须独立读回配置、PID/实例、
server 侧 online/normal/provider，真实对象访问需另有明确目标与授权。

已知未启动或有 server admission fence 的失败恢复原配置及允许恢复的数据，并核对
旧配置加载哈希后才解除维护。停止/启动结果未知、配置被外部改动、备份损坏或健康
渠道不可用时保持维护并报告，不以“应已回滚”代替证据。提交后不恢复旧 journal；
后续撤销配置须以当前哈希为基线启动新的维护批次。原始配置和备份不上传为任务附件。
