# 受控升级操作

实现依据 D82–D85、D252。自动安装目前支持 Linux；其他平台可以构建，但不会执行进程替换。发布由固定 Go 更新器执行，模型不参与停止、替换或恢复进程。

## 信任边界与故障模型

本流程用于同一 owner 管理的可信 server、runner 和 updater。服务用户能够修改程序、配置和本地状态；恶意同 UID 进程、root 或恶意 runner 不属于此流程提供的隔离边界。已有节点鉴权和本地私有 socket 用于选择正确的服务入口。

恢复逻辑处理进程崩溃或重启、网络断开、请求重复或迟到、发布产物不完整，以及领取任务和升级之间的并发。批次、进程和程序哈希用于识别旧回执与误接的进程；健康与持久提交分开，是为了避免回退覆盖已接收的新任务。收到停止或启动结果不明时，只暂停依赖该结果的切换步骤，继续保留现有服务和任务事实。

本地状态写入失败时，updater 保持待处理，不发布未持久化的终态证明，也不继续外部切换。修复磁盘等原因后，重新启动 updater 即可从已保存状态对账；这不要求停止仍运行的受管服务。正常的心跳确认间隔和已恢复接单后的活动任务不会被视作升级故障。

## 单独更新 server

从已支持 D258 的控制端执行 `tend upgrade start --server-only`，照常选择完整发布包，也可用 `--commit` 或 `--release` 固定来源。目标 server 也须声明此能力。默认的 `upgrade start` 仍更新整套服务。首次从较早控制端迁入使用其已有升级流程。

server 单独更新时，各在线 runner 通过当前协议确认重启窗口，已领取的 Run 继续执行。server updater 校验并提交新进程后，批次可以在原 Run 结束前完成。用 `tend upgrade status --json` 查看 `server_only`、各 host 的 `retained` 阶段及实际协议。原先处于维护模式的 host 继续维护。若现存 runner 超出目标的 N/N−1 窗口，先安排它升级或退役，再发起兼容的批次；结果未知时仍按原请求对账。

## 首次接入

协议 5 首次采用整套冷切换，不提供旧协议滚动互通：先停止旧接单与真实执行，
归档数据库、原件、runner journal 和 updater 状态，保留配置及节点身份；安装完整
发布集并初始化新的业务数据，不自动重放旧任务。归档不等于删除原件，也不等于
旧 unknown 已解决。已有协议 5 集群后续普通更新保留业务数据并执行必要迁移。

server 配置增加 `upgrades`，包含 `instance`（逻辑服务名，默认 `server`）、`control_socket`、`updater_socket`、`release_dir`；可配置 `drain_timeout`（默认 `6m`）、`restart_budget`（默认 `120s`）、`grant_duration`（默认 `300s`）。新批次另持久保存二十分钟整体截止；阶段与 grant 均不能超过整体剩余预算，retry 不续期，到期后只对账或取消再另开批次。历史批次没有整体字段时仍按原分段期限对账。这些是本机私有路径，配置不提交仓库。

远端执行者需要的正式操作入口放在 `upgrades.execution_entry`：`transport` 为 `local`、`ssh` 或 `tailscale_ssh`；远端通道显式填写 `target` 和 `user`，并填写指向当前受控 CLI 的绝对 `cli_path`。`control_socket` 来自前述实际升级配置，不能另指一个 socket。Issue 和 worker 的部署准备投影会提供这些字段；未配置时不把逻辑 instance 或 runner ID 当作 SSH 别名，也不猜测 PATH 中的 CLI。该投影不包含凭据、不表示网络连通证明，且不授予安装权限。更换受控 CLI 入口时同步配置稳定入口或实际当前路径。

Issue 的 `pr_policy` 默认只控制合入。已有明确范围内部署授权时，由真实 owner 来源记录 `deployment: {server_instance, host_ids}`，或使用 `tend issue pr-policy set ... --mode auto --deploy-server-instance <instance> --deploy-hosts <hosts>`；空 host 列表表示仅 server。候选和基线变化继续复核真实审阅、检查与安装身份，不因 SHA 改变重复索要授权；实际目标或授权范围扩大仍由负责人确认并请求新的权限。连接入口与这份授权分别记录。

runner 配置同时增加 `updater_socket`（自己的 authority socket）、`updater_control_socket`（对应 updater 的 control socket）、`updater_bundle_dir`（私有下载目录）。逻辑 updater instance 必须与 host ID 一致。保留现有 worker、gateway、槽位和数据目录配置。

每个受管进程配置一个 updater：

- `role` 为 `server` 或 `runner`，`instance_id` 为对应逻辑服务名。
- `state_dir`、`initial_binary`、`args`、`log_path` 固定指向已核实的程序和配置；`env_files` 指向原凭据文件，状态和日志不复制凭据。
- `data_files` 只列出该服务的数据库或 journal；SQLite sidecars 一并处理。不包含配置、凭据和其他服务数据。
- `authority_socket` 指向被管服务的私有 authority socket；`authorize_url`、`health_url` 分别为 `http://localhost/updater/authorize`、`http://localhost/updater/health`，请求通过 Unix socket 发出。
- server updater 的 `coordinator_socket` 指向 server `control_socket`。runner 使用自身已有 tsnet 连接向 server 转发授权，不复制节点身份。
- 首次 `protocol`、`storage_version` 填当前实际版本。后续成功安装会持久化新版本，回退恢复匹配版本，不持续依赖首次配置。

先运行 `tend-updater serve --config "$UPDATER_CONFIG"`，再以 `bootstrap --config "$UPDATER_CONFIG" --pid "$SERVICE_PID"` 收养已确认空闲的进程。更新器验证实际程序、进程启动身份和参数，并保存初始版本；每个配置只能由一个 updater 持有。

普通 `restart` 仍要求协调停止。旧升级实现自身失效时，使用下述新发布中的外部恢复入口；正常自动升级不自行获得强制终止权限。

## 发布与执行

普通升级必须使用 format 2 完整发布集，不提供旧包升级入口。完整目录包含 `release-set.json`、`manifest.json` 及全部平台组件。service manifest 固定 `format: 1`、`release_id`、`protocol: {min,max}`、`storage.server_schema`、`storage.runner_journal` 以及 server/runner `artifacts`；每项产物有 `role`、`os`、`arch`、相对 `path`、`size` 和小写 `sha256`。两个 format 字段属于不同清单。

main 的完整 CI 通过后，Actions 以 `main-<完整 commit SHA>` 发布不可变 prerelease。
Linux amd64、arm64 各有完整压缩包及发布集清单，并提供 `SHA256SUMS`。
另提供 `linux-fleet` 完整包，包含两个架构的全部组件；混合架构的 server/runner
升级使用 fleet 包，其 service manifest 可供既有协调器按每台主机平台选择产物。
完整包同时包含 `tend-server`、`tend-runner`、`tend-updater`、`tend` 和
`release-set.json`；server 嵌入同一提交实际构建的 Web。发布集绑定全部组件、
源码 commit 与 service manifest 摘要；它不扩展旧 manifest 的角色。
存在但损坏、不完整或与 service manifest 冲突的发布集必须拒绝，不能退回旧包。
发布重跑只补齐尚未公开的 draft，相同资产核验后复用，不覆盖不同内容。
发布不会连接或部署任何主机；生产安装仍经过独立授权与升级流程。

本地复现发布构建时，先在干净的已提交源码运行 `make web`，再执行：

```sh
go run ./internal/release/packagecmd --arch amd64 --commit "$(git rev-parse HEAD)" --output .local/release
```

另用 `--arch arm64` 构建第二个平台。构建器拒绝源码漂移、缺少实际 Web 和
二进制 Go VCS 身份不符；压缩包使用提交时间和固定文件属性，避免重跑改变资产。
组合包由这两个已验证平台包生成，要求相同源码及协议/存储兼容范围，并再次验证
所有二进制身份；不能把不同提交的包拼接成一个发布。
线上不再存在须兼容的历史部署。service-only 与 format 1 发布集不能用于新建升级；
账本及运行中的原请求仍可读取和对账，不提供绕过格式门槛的开关。新构建使用
format 2 发布集，明确要求 updater 先交接，不能静默退回只更新服务。开始前
校验本机 updater 和所有 runner 的协商能力；缺能力时先按下面的独立 installer
流程 bootstrap。server updater → server 完成后，逐台执行 runner updater → runner。
批次状态的 server_updater 和各 host.updater 分别记录实际进程及独立交接回执。
只有实际运行哈希和受管服务身份一致才能跳过替换；固定路径上的文件相同不足以证明。

`protocol.max` 是发布当前协议，`min` 必须为上一代（首次为 0）；存储范围的 `max` 是该二进制当前迁移版本，`min` 是能升级读取的最旧版本。已提交迁移不可修改。发布目录不得包含符号链接或路径逃逸。实际服务凭据、数据和工作目录不放入 bundle。

先校验全部文件：

```sh
tend-updater verify --release "$RELEASE_DIR"
```

默认从 server 本机选择官方最新已发布 main fleet 包（包含 prerelease，排除 draft），
校验完整包后启动单个批次：

```sh
tend upgrade start --socket "$SERVER_CONTROL_SOCKET" --json
```

已授权交付绑定明确提交时使用完整源码 SHA：

```sh
tend upgrade start --socket "$SERVER_CONTROL_SOCKET" --commit "$SOURCE_COMMIT" --json
```

离线或本地构建使用完整目录，同样强制 format 2：

```sh
tend upgrade start --socket "$SERVER_CONTROL_SOCKET" \
  --release "$RELEASE_DIR" --upgrade-id "$UPGRADE_ID" --json
```

`--commit` 与 `--release` 互斥。CLI 先输出 request ID；server 在私有发布目录按该 ID 先固定原始输入和公开标签，再读取官方 SHA256SUMS、查询 fleet 大小并固定摘要，然后才下载。重试使用原 request ID、upgrade ID 及全部原参数，不能因 main 前进选择新包；改变选包、配置或交付参数会被拒绝。原子暂存完成前不会创建业务批次。下载最多四分钟，初始本机 HTTP 往返最多五分钟，不改变升级准入后的任何维护、停止或健康期限。默认发现读取官方公开 release feed；无有效 main 发布时明确失败，可用 `--commit` 精确选择。发现与校验使用公开 feed 和校验文件，不依赖 GitHub 匿名 API 配额或新增主机凭据。

也可用 server updater 的 `upgrade --config "$UPDATER_CONFIG" --release "$RELEASE_DIR" --upgrade-id "$UPGRADE_ID"`，它同样进入受 format 2 约束的 server 批次协调接口。CLI 会先输出 request ID；不确定回执先读状态，不要换 ID 重发。相同请求重试必须保留批次、发布和 request ID。`--dry-run` 只展示请求，缺省选包仍显示空 release，既不访问 GitHub，也不启动下载或升级。

准入拒绝未结束批次、离线或不兼容主机、无 updater 能力、无法读取的存储以及仍依赖退役协议的 run。server 先缓存全部产物，各目标 runner 经现有认证链路下载并校验；原本 normal 的主机在 preparing 期间继续接单。
已通过 `host maintenance` 停止接单的主机可直接使用同一 `upgrade start` 入口交接，
无需先恢复 normal：必须在线、新鲜确认当前 control revision，且没有其他升级目标或
固定安装授权；目标 server 必须经实际候选能力探测确认支持交接。缺少支持的旧发布
仍可从 normal 启动，但不能接管临时维护；拒绝不改变原维护。新批次同事务接管维护责任并提升 control revision，准备包时继续保留
维护，既有 Run 可继续执行；PrepareOnly 不授予安装权限。server 提交后这些主机也
保持维护，直到自身 runner 提交或同哈希安全跳过并收到 normal ACK。准入失败保留
原临时维护；接管后的失败、超时或取消沿既有恢复路径释放已确认未替换的进程，
未知候选保留原批次维护等待真实回执。全部准备完成后，server 等当前主会话结束；期间 owner 消息继续持久入队。随后固定本轮现有 run 的维护授权并等待 runner 确认，确认剩余覆盖足够才允许停 server。普通心跳不延长这次固定授权。runner 用完整请求往返耗时和本地保守折扣确认覆盖期限；停止授权的截止时间再扣除整个 server 重启预算。获取授权后暂停过久的 updater 不得停止服务。

新 server 健康后恢复领取并按空闲优先逐台维护 runner。维护只等待当前 run 与交付/资源操作，不等待整项任务：后续验收仍在原工作目录排队。单 runner 也可更新。维护期间只使用已校验的缓存，不再联网下载。候选以维护状态启动，确认实际进程、程序哈希、配置、journal、provider 和服务端握手；首次真实就绪即可由本机 updater 持久提交。server 与 runner 都在确认对应提交证明后恢复接单；相同组件哈希跳过替换。模型会话、摘要和业务历史对账不阻塞安装提交。

```sh
tend upgrade status --socket "$SERVER_CONTROL_SOCKET" --json
tend-updater status --config "$UPDATER_CONFIG"
```

Web 的 Chat 和看板显示批次入口；主机在线、维护与 slot 分别显示。开始、单台恢复、完成、异常和取消写持久 system 消息，不自动调用模型。下载和细粒度状态放在发布详情。

## 暂停、恢复与数据边界

排空超时会暂停批次，恢复尚未授权替换的旧进程接单；已有候选、失联或未知安装阶段保持维护，等待对账。其他主机离线、校验失败和健康未知也不会触发强杀或猜测回滚。

server 替换超过心跳窗口时，已提交的新 server 对尚未确认恢复控制的原目标显示
`reconnecting`（等待主机重连与控制确认），保留 queued，不将旧维护观察当作新的
在线或安装就绪证明。此等待仅占用原阶段/整体剩余时间；任一期限到达仍暂停。
同一目标的新控制 ACK 到达后继续原滚动流程，不等待全部 fleet 同时在线。

部署到本修复生效前，若旧版本已因“等待升级的主机离线”提前 attention，先读原
`upgrade status` / `upgrade steps`、原 updater committed receipt、目标进程/哈希、
控制 revision 和实际 normal ACK；维护交接目标按原 maintenance 控制确认。不要
另起请求重装已提交的 server。没有未结候选且整体期限未到时，可按下述正式 retry
恢复原批次；retry 会按既有规则重算 drain 阶段并受原整体期限限制，不能把它当作
原阶段等待未续期的证据。若要求保留原阶段截止而该阶段已到，保持暂停并对账，
不通过修改 deadline 继续。未知安装、取消回执或身份不符先沿原身份对账；期限已到
只能完成证据对账或合法取消，不手改数据库、不 force、不缩目标。正常部署使用
包含本修复的目标 server 后，重连等待由新 server 在持久提交后执行。

读取当前 revision 后控制批次：

```sh
tend upgrade retry --socket "$SERVER_CONTROL_SOCKET" \
  --upgrade-id "$UPGRADE_ID" --revision "$REVISION"
tend upgrade cancel --socket "$SERVER_CONTROL_SOCKET" \
  --upgrade-id "$UPGRADE_ID" --revision "$REVISION"
```

已经确认回退的批次会恢复旧版本服务并保留失败候选事实；先取消本批次，再用新批次重新安装。只有绑定当前进程、原候选和旧哈希的 updater 终态回执能解除该维护门禁；仅观察到旧哈希不足以证明回退成功。

取消后会对账尚未安装的本地 updater 操作；下一批升级要等各主机心跳确认取消，避免遗留 staged 操作。已经完成的主机不因历史授权过期阻止后续主机重试。旧进程已停止且持久阶段确认候选尚未启动时，授权过期会恢复旧进程；候选启动结果未知时仍须对账。

候选在阶段超时后恢复健康，仍会按原进程身份、程序哈希和实际就绪证明进行对账。确认后的 server 恢复主会话和 runner 领取，确认后的 runner 恢复接单；批次继续保持暂停，不自动安装下一台。读取更新后的 revision，再选择 retry 或 cancel。此过程不续签停止授权、不重做安装。恢复旧版后的自动同版本重启会保留回退证明，并将其绑定到已确认的新进程。

提交证明同样沿已确认的同版本重启保留原候选身份，避免恢复后的新进程被误认为无关安装。选择回退后，updater 必须持久保存绑定 control revision 的 admission fence，停止仍存活候选前再次核对；无法确认则保持待处理。已持久提交的版本不可恢复旧数据，取消也不能绕过提交或回退证明。

过期授权不能靠 retry 或重复心跳续期。取消不反向降级已经完成的主机，且不能在候选状态未知时把主机直接恢复接单。运行中的新 server 一旦确认响应，就不能恢复旧数据库；新 runner 一旦恢复领取，就不能恢复旧 journal。日志、任务、交付物及历史会话继续保留，无法确认的外部操作按 unknown 对账。

停止授权已提交但回执丢失时，updater 可能仍停在 staged。批次暂停后，协调器通过 staged 比较交换取消；完整持久 cancelled 证明与当前旧进程匹配后，恢复未执行替换的服务。本批次保留暂停，取消后以新批次重新升级。该证明表示替换未开始，不表示执行过数据库回退。已进入 stopping 或后续阶段的操作不会通过此路径解除维护，继续原有进程对账。

演练需要核对实际 PID、程序哈希、批次和 run 身份，以及各实例停止、启动、就绪和提交的实际边界。每次测试只清理本次创建的 Orca workspace 与临时目录，先确认 run 释放，保留任务账本和交付证据。

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
server 授权后，updater 停旧进程、保存原配置、原子应用配置并启动相同程序。相同存储版本不复制整库；有存储迁移才备份受影响数据。新进程报告
实际解析字节的哈希，server 确认 required provider 和实际就绪，updater 再
持久提交。服务端恢复 normal 并收到确认后批次完成。验收须独立读回配置、PID/实例、
server 侧 online/normal/provider，真实对象访问需另有明确目标与授权。

已知未启动或有 server admission fence 的失败恢复原配置及允许恢复的数据，并核对
旧配置加载哈希后才解除维护。停止/启动结果未知、配置被外部改动、备份损坏或健康
渠道不可用时保持维护并报告，不以“应已回滚”代替证据。提交后不恢复旧 journal；
后续撤销配置须以当前哈希为基线启动新的维护批次。原始配置和备份不上传为任务附件。

## Issue 交付的绑定与并发

D152 的后续交付执行使用 `tend upgrade start --delivery-task "$DELIVERY_TASK_ID"`，同时保留原 `--upgrade-id` 和 `--request-id`。控制服务从固定目录的已校验产物读取真实 Go VCS 信息，应用层复核当前执行租约、Issue 代次、具体 owner 答复、目标范围、上一批次 ID/revision 和当前 provider 事实；修改过、未知或旧目标构建不能安装。已有管理员维护入口继续保留原权限，不能将其当作未批准的 Issue 交付旁路。

同一目标同时只领取一个部署执行。另一 Issue 的 pending action 不占 run/slot/执行环境；第一个执行启动批次后提交包含唯一 `upgrade:<id>` 的持久报告并释放自身 run。批次完成之前其验收也不领取目标主机；unknown、失败和取消沿原批次对账，不能靠新建批次换掉身份。目标升级完成或取消后，等待 Issue 必须刷新 provider/baseline 并重新绑定当前候选，不能安装先前旧 main。

首次安装 D152 前，先确认既有批次实际结清、协调工具已发布、runner 已协商 D150 的 `issue-question-handoffs-v1`。旧版本交付任务只启动一个实际批次，以原 ID 提交技术等待问题并由 runner 确认退出；协调者读取实际完成状态后答原问题，原非终态任务接续核验并交付，再走独立验收。未协商的旧运行不得强制套用新合同。D152 上线后的 reconciliation 只核验原已验收报告及真实完成批次，不重放合入、安装或事后改记旧任务授权；缺来源、安装事实或原验收时保留阻塞。

## 受阻交付的恢复

扩大产品修复范围前，先读取原操作、批次和实际安装事实。按已知结果分流，不从超时、空日志或过期投影推断未安装：

| 原操作的实际状态 | 恢复方式 |
| --- | --- |
| 已接纳、批次未结束，或外部结果 unknown | 沿原请求、批次、固定输入和期限读取状态并对账。不得换 ID、入口或版本重复安装。 |
| 明确拒绝且未创建批次 | 保留拒绝，核对当前授权内是否已有正式恢复入口；不把拒绝改写为后续维护成功。 |
| 已有完成批次和实际安装 | 核对原目标、安装来源及原 Task 的正常结算能力；先复用真实证据，不为了登记再部署。 |
| 现有接口无法表达真实恢复 | 明确缺失的转换和证据，决定同故障链修复；不以新 Task、补造批准或状态修改绕开。 |

Issue 已有的自动部署授权与管理员维护权限分别按其真实范围判断。不能为了满足误用手动答复的入口而补造批准，也不能仅凭自动授权就推定另一个维护入口已获授权。已有授权确实覆盖的动作无需重复索取许可；一次明确授权的维护例外不成为以后的默认旁路。

若授权允许独立管理员维护，可使用上文正式本机 `tend upgrade start` 入口，不传 `--delivery-task`，并保留真实发起责任。先核对原操作没有冲突的 accepted/unknown 批次、当前目标和基线、精确受审源码及完整 format 2 发布集；正常排空、固定期限、健康和提交回执要求全部保留。它是新的独立维护动作，不是重发原被拒绝的业务 Start。不要用 force、缩小主机集合或更改 deadline 代替这些检查。

开始前同时确认原 Task 的正常结清路径。现有精确源码结算中，实际安装 commit 等于原 PR 的真实 merge，且原目标、成果和所需质量条件均满足时，可由原 owner Task 登记真实 `upgrade:<id>`、正常 complete、最终发布和关闭；维护批次身份不改成原业务发起。如果实际安装的是包含原 PR 的后代提交，不能把“包含”当作“commit 相等”，也不能用当前 main 的祖先关系替代实际安装版本的证明。先确认正常接口支持该恢复；缺能力时保留具体缺口，不删除 source 校验、用 end 冒充 complete，或安装旧包来满足登记。

completed 批次标签不能替代实际进程、配置、健康、committed receipt 和正常 ACK。安装证明、原 owner 成果及质量、交付义务结清、最终消息正文与附件回执、整个 Issue closure 分别核对。该 Issue 的专用观察器只在整体完成或用户暂停时停止，保留检查点、策略和实际最终发布证据。长期产品修复单独记录责任，不自动成为这次已有正式恢复路径的前置条件。

## 执行窗口与诊断（D234）

完整发布集的构建、下载暂存、实际基线核对、备份和所需操作输入应尽量在发起批次前准备。已接纳批次沿原整体/阶段期限推进，不把人工准备耗时补成新的 deadline；恢复前先核对剩余时间是否足以完成尚未执行的步骤。固定期限不是每步重新获得的完整窗口。

同哈希 server 也记录准确进程和实际就绪证明；独立维护批次后续供正式交付引用时不能省略这些证明。同哈希 runner 完成既有 updater 核实后，把恢复正常与清除本轮元数据放在一次控制更新中，仍等待真实正常 ACK 才推进下一主机。真实替换或配置变化继续走完整排空、安装、健康和恢复路径。串行主机、下载、排空和心跳等待要分别计时，不能把一个已部分安装的批次当成全新部署的耗时基准。

server、runner 和 updater 的进程入口统一接受 `TEND_LOG_LEVEL=debug|info|warn|error`，空值为 `info`，`verbose` 是 `debug` 的别名；非法值明确报配置错误并停止启动，不静默降级。稳定性调查期可在原服务环境文件中设置 `TEND_LOG_LEVEL=verbose`，下一次正常受控启动生效；不为调高日志重复未知升级。该设置只改变诊断级别，沿用 stderr、现有监督器分段与轮转，不改变凭据、业务身份、授权、期限或状态。普通 owner CLI 的业务 JSON 输出不受影响。

`app state transition` 在关键 Task／Run／Issue／upgrade 事件实际提交后记录对象、版本和 request／trace；`app command rejected` 保留错误码与对象，避免序列化用户输入或内部错误正文。一般观察放在 debug。`upgrade host availability decision` 在真正暂停时以 warn 记录判断时的逐主机在线、心跳时间／年龄／阈值、slots、运行与控制模式、当前／已应用控制版本、upgrade 身份和具体失败谓词；短暂正常 ACK 间隙仅为 debug，不改变原等待行为。短路未读取的字段保持缺失，不能解释为观测成功。

`server process log provenance` 给出当前 server 的实际 PID／父 PID、可执行文件，以及支持时的 stdout／stderr 文件描述符目标；用此定位本轮日志，不把历史固定日志文件当作正在运行进程的输出。

server 日志中的 `upgrade control step` 关联原 `upgrade_id`、`request_id`、步骤、耗时、阶段/整体期限及剩余时间；`upgrade phase changed` 与 `upgrade host phase changed` 记录转换和原因。updater 的 `updater operation changed` 记录实际持久阶段、进程身份、健康计数与首次或变化的错误。重复健康轮询不重复写阶段日志。

`tend-updater status --config "$UPDATER_CONFIG"` 返回操作的 `phase_timings`、`last_health_at` 和 `last_health`，保留原批次的阶段转换及最近一次检查。诊断分别呈现进程存活与身份、权威绑定、server 确认、服务 ready、主会话排空、组件失败原因及检查阶段耗时；未观察到的事实省略，不冒充 false。服务 ready 不代表已经排空，恢复业务后 `main_drained=false` 可以与 ready 同时成立。`healthy` 仍使用原有安装门槛，不因诊断字段而放宽。健康 authority 只在调用方显式请求 `diagnostics=1` 时追加诊断，旧严格客户端继续收到原响应；新 updater 同时接受旧 authority 缺少诊断的响应。历史超时不据此改写为某个唯一已知原因。

普通自身交接记录 `self-update installer dispatch recorded`、`self-update dispatch command returned`、`self-update installer entered`、阶段持久化和 `self-update installer returned`。用其中的 unit 名读系统日志，例如 `journalctl --user --unit "$INSTALLER_UNIT"`；自动 installer 的 stdout/stderr 明确交给 journal。派发命令退出码只证明该命令的结果，installer 返回也不等于新监督器完成接管，最终仍读原请求的状态、准确进程和提交回执。进程被终止且缺退出证据时继续记为 unknown，不能重建一个成功退出码。

定位当前服务日志时先读取真实运行进程及其监督器配置，不以某个旧日志文件的 mtime 推断服务没有运行。受控启动的分段日志位于配置 `state_dir` 下的 `diagnostic-logs`，以真实 launch ID 关联原件；固定 `log_path` 在旧文件仍存在时未必指向本轮分段。保留原日志、journal 单元信息与状态/回执，再解释缺口，不通过轮转、清空或重发安装消除故障现场。分享日志前移除凭据。

## 普通 updater 自身交接与旧节点 bootstrap（D200）

正常更新中的 updater 自身交接沿原服务和监督锁进行，尽量保留正在运行的服务。
旧 updater 或其安装状态无法工作时，使用独立外部恢复，不要求旧 RPC 或旧状态可解析。

普通升级自动把 installer 固定为独立 systemd user transient unit。旧 updater 退出时
保持服务 PID，安装器取得同一监督锁，原子替换固定 executable 并启动新 updater。
新 updater 先取得原锁、加载原 journal、验证保留服务并写持久回执，再允许服务升级。
共享文件的安装串行化；各实例各自交接锁、原服务和回执。回执或启动结果未知时仅观察
原 attempt，不换 request ID、重复启动或借用维护/force 授权。配置、维护期限和健康规则不续期。

显式单主机配置维护保留原目标范围：在 format 2 下先确认本机监督器，再对目标 runner 的监督器和配置执行原维护流程；没有选中的主机保持 skipped，不要求或派发本批次的新安装证明。普通完整版本升级仍覆盖全部活跃目标主机。已完成普通交接的历史记录不会替代后来独立维护的启动授权，新维护由其原有 adopter 验证。

监督器准备明确失败且 installer 尚未派发时，按原请求持久保存 rejection 证明。`self-update status --plan <original-plan>` 可在响应丢失或重启后恢复该证明；业务批次取得证明后可取消并另开批次。无法确认派发结果仍是 unknown，不由超时或缺少 journal 推导拒绝。

旧 bootstrap 工具只用于已有可核验退出约定的监督器。故障恢复的默认路径见下节，
不用给失效旧实现补一份退出能力声明才能安装修复。


### 安装窗口与停机诊断

普通新批次以 `grant_duration` 为安装额度，runner重启覆盖另包含 `restart_budget` 的预留，并受既有维护上限与原整体截止约束。实际server授权还会按runner报告的保守截止缩短；读取 `remaining_ms` 和原截止，不把配置额度当作保证剩余时间。已发批次或授权不会因再次观察而续期。

协调器和runner在实际状态变化时及时观察/ACK，周期心跳仍兜底；提速不跳过必要停止、实际就绪和持久安装回执。耗时对比须使用同样的替换范围，不能将已有部分组件安装的批次当作全量性能基线。

停机日志包含 `server shutdown phase`、`HTTP shutdown requests`、`HTTP shutdown active request` 及限量的 `HTTP request drained`。用规范化route、method、trace_id/request_id、request_kind、elapsed_ms、cancellation_source与各阶段时间区分只读等待、在途写入和后台关闭；原文件、URL参数及凭据不记录。空闲claim、消息/问题/日志长等待会返回当前事实，真正写入和文件传输仍继续排空。

监督器终态日志的 `historical_error` 保留曾发生的错误，`error` 表示本次记录的新错误；以真实phase和回执判断终局。自更新明确拒绝的原身份status保留首次诊断原因，旧记录未保存时仍为unknown；不能用后来成功填补旧退出或拒绝原因。

### 部署前阶段结果

已绑定部署的同一 Task 在检查或必要新候选准备阶段，可用 `submit_result` 的报告字段 `predeployment` 保存真实阶段结果：

```json
{"report":{"body":"候选与检查结果；尚未安装。","predeployment":{"delivery_id":"registered-pr","head_sha":"<完整候选提交>","base_sha":"<完整候选基线>"}}}
```

提交仍包含实际仓库状态和正常原件。不要附加虚构 `upgrade:` 引用；这个阶段只接受候选独立验收，不完成部署责任。实际 Issue owner 自执行使用原 `issue update-delivery --data` 的 `action: submit_result`，`result.report` 支持同一字段，必须是实际执行 owner 的 Run。阶段验收完成后用原 `task continue`；已确定 merge 且未升级的旧候选，通过原 `issue update-delivery` 的 prepare/bind 接续有自己真实产物、独立审阅和检查的新候选，保留旧历史。原 frozen candidate、unknown、租约或版本不能用阶段结果绕过。最终真实安装后仍提交带唯一实际 `upgrade:<id>` 的最终报告并独立核验，再 complete。

阶段 acceptance 完成后必须先 continue，再 prepare/bind；不能因 Task 的阶段已 completed 而丢弃尚未履行的部署绑定。已验收阶段在 main 前进后可启动准备新候选的 Run，这项调度准入不授予旧候选安装新 main 的权限；正式安装仍核对当前来源、授权、完整主机集合及真实升级。相同完整主机集合的顺序不改变范围。

仅报告阶段或真实安装结果、没有新源码变化时，保留实际 `Changed=false`，不要增加空提交或伪造变化。runner 的无变化采集会对明确阶段或唯一 upgrade 报告读取真实仓库状态；服务端仍完整验证 typed stage。最终无变化的 commit 结果只能精确复用同 Task 已独立验收阶段的全部已提交仓库身份，并匹配当前冻结候选/授权与真实唯一升级；普通 commit 结果、未验收阶段、不同提交或快照继续拒绝。阶段 artifact 本身仍不完成安装结算。

阶段验收要求修改时，沿原 Task 自动生成的修正 action 继续，保留其真实负面反馈；不要把 Task 的空 current artifact 指针当作阶段丢失，也不要将该负面验收视作安装通过。main 已前进时，只要原授权/责任、已知 merge 关系和无升级/unknown 条件仍成立，可以准备修正；实际安装另须通过当前 whole-PR 审阅、精确候选 CI、阶段验收与全部原安装检查。

最终安装报告的验收若要求补充说明或证据，可以在同一 Task/真实唯一升级下修正报告并保留 Changed=false。服务端依据冻结候选和当前 spec 找到最近阶段的真实已验收仓库来源，不依赖已被生命周期清空的 current artifact，不接受其他候选、未经通过的新阶段、不同 base/commit 或未提交快照。不得重新声明已安装结果为 predeployment 或制造源码变化；最终独立验收与 complete 的实际安装证据要求保持。

owner 在阶段之后继续登记普通进度，不会替代该阶段的独立验收。安装读预检和实际写入口都读取当前 Task/spec 的最近持久阶段及其真实质量约定，不能靠覆盖 current artifact 指针放行未通过阶段；后来的未通过阶段也不能借旧阶段通过。相同显式空 host 集合的 server-only 候选可正常接续，仍须保持原 server/park/完整 scope；候选入口不接受 nil，其他交付的固定完整主机授权保持。

普通 owner 进度也不会影响 prepare 和独立质量绑定：审阅报告明确指向原阶段 artifact，prepare 继续使用该原件及真实独立结论，保存的 duty 来源不能变成后续普通报告。无需为找回 current 指针而重复提交或重复审阅阶段。

同一 Task 的旧交付已正式 complete/report/ended 并归档后，其阶段只属于旧责任，不再阻挡下一份合法责任；未履行的候选接续不能据此排除阶段。真正 complete 后仍先 task continue，保留原执行和结算原件；已结束历史的原件缺失应先对账。当前责任的新阶段仍须通过原独立要求，不能用普通报告遮蔽。本规则不授权结束尚待履行的现场交付来解锁安装。


旧运行版本已 self-bind、但没有任何真实 upgrade 或 typed predeployment stage 的交付，若原执行已停止且租约/外部操作回执已收敛，可在当前 Issue revision/epoch 下用 `issue update-delivery` 的 `action: recover_preparation`，携带原 `task_id`、当前 `expected_task_control_revision` 和明确 `reason`。它归档旧绑定，恢复相同 Task 的准备责任，保留候选与全部历史；不完成交付、不授予新安装。随后通过原 Task 的真实结果、独立验收、继续与 prepare/bind 进入当前候选。保留工作区所有权不代表活动 writer。已有真实升级、未结外部动作、变化的责任/授权或 typed stage 必须走原对账/候选续行路径，不能用此入口重放升级或清理 unknown。


## 外部强制恢复与最短停机（D252）

在目标实例的 systemd user unit 之外运行新发布中的 CLI。完整本地发布集必须包含
匹配平台的新 updater；CLI 从清单固定其哈希，installer 在停止前验证整个发布集。
这个入口直接使用本机服务管理器，不访问旧 Tend/updater 的 RPC。

```sh
"$NEW_TEND" upgrade recover --release "$RELEASE_DIR" --config "$UPDATER_CONFIG" --force --dry-run
"$NEW_TEND" upgrade recover --release "$RELEASE_DIR" --config "$UPDATER_CONFIG" --force
"$NEW_TEND" upgrade recover --release "$RELEASE_DIR" --config "$UPDATER_CONFIG" --status
```

需要指定服务时加 `--unit "$UPDATER_UNIT"`。可显式指定 `--request-id`；缺省身份由
固定实例、配置和发布摘要决定。回复丢失先查 status，重试沿同一参数和身份。status
读取持久恢复记录，不能把进程正在启动当作已提交。相同身份已经提交后不会再安装。

准备阶段在服务运行时完成产物缓存、配置和兼容性检查。切换仅含：停止固定实例的
服务 cgroup（包括子进程）、确认实际退出、取得原监督锁、必要的数据快照/迁移、
原子替换和启动、私有就绪握手及持久提交。无需等旧服务的正常 drain 超时；拒绝
停止的进程由显式 force 终止。只复制发生存储转换的数据，纯二进制更新不复制业务
数据库或不可变原件，不在停机窗口下载、构建、运行测试或调用模型。

旧 updater 状态及 self-update/handoff 启动 journal 原样归档，即使无法解析也不阻止新安装。
本地阶段完成后立即推进；只有等待实际外部就绪时才轮询，不在每个持久化阶段之间固定停顿。
存储不可读、候选不兼容、
无法确认目标进程退出或无法取得原锁时仍停止推进。配置和凭据沿用原固定配置。
新服务的实际进程/产物/协议/存储就绪由共同监督路径提交。回执关联明确被替代的
升级 ID；它只结束该旧批次并解除被替换实例自身的升级控制，旧回执不能取消后来
创建的升级。其他节点仍需其自身的正常对账或外部恢复，不能借本机 force 解锁。

业务请求、原 Run、外部动作和 unknown 都保留。session 和历史收据读取在安装之后
按原身份恢复；不通过清空业务记录或重发原动作来让更新显示成功。实际停机时间应
取服务停止至就绪提交的记录，准备和事后对账另计。源码测试结果不代表实际部署。

外部恢复的存储兼容性优先依据已安装状态，初始配置中的版本可能已经过时。旧状态
不可解析时不以该初始值阻塞恢复：先保存配置指定的数据快照，再由新候选确认实际
存储。只有已证实存储代次不变时才省略数据复制；配置检查在停止旧进程前完成。

### 同机 runner 交接

首次重叠交接使用候选完整包中的 CLI。等 fleet 批次进入该主机的 draining 阶段，在 runner 服务账号环境执行 `tend upgrade runner-handoff --release <完整包目录> --config <runner配置> --upgrade-id <原批次ID>`。该命令使用原 host 凭据，校验同一发布集，更新并保留正在工作的前驱。输出中的 staged 表示本机受理；批次状态和 updater status 分别显示是否提交及哪些进程仍在收尾。回执丢失时沿原批次和包查询。

使用内嵌 tsnet 的主机先在提供给后继的 runner 配置中设置 `"side_channel":{"kind":"tailscaled"}`。原进程继续使用已打开的节点身份。新连接的节点名或 tag 须命中 host 的 nodes。可用 socket 字段指向自选 LocalAPI Unix socket。使用直接连接的主机可沿用 server_url。完成首次交接后，由接单 runner 自动提交后续完整包；旧 journal 中的任务结束、待提交回执清空并确认进程退出后分别退役。操作命令的环境需要与服务配置使用同一 TEND_HOST_TOKEN。

同机交接时可用 `tend host list --json` 查看接单实例和各收尾实例的 Run 数、协议与心跳。首次从历史 runner 交接会等待已领取的 Run 完成工作区准备，已启动的长任务可以继续运行。每个后续实例使用独立 journal，Git 和 Orca 的共享准备操作按主机资源加锁。工作区清理检查保留的各代 journal 中的原件路径。用量控制发往持有对应 Run 的实例，本机用量对账默认连接当前接单实例。

旧进程确认退出后，当前 runner 使用旧 journal 中的请求、上传游标和本机停止证明继续处理未完的归档、用量和工作区对账。可通过当前 runner 的日志查看重试原因。
