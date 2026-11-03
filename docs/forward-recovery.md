# 原窗口前进恢复

本功能的终点是「原升级历史仍 unconfirmed，独立 successor 真 committed，恢复已关联账本」。设计通过、代码检查、PR 合入都不是生产恢复证明。生产激活另行冻结实际候选、产物、窗口和资源交接；本实施不执行生产安装。

## 受支持范围与权限

仅 Linux、同 service UID 的私有本机控制权限、固定 systemd 用户 simple service。要求 KillMode=process、KillSignal=15、FinalKillSignal=9、SendSIGKILL=yes、Restart=on-failure，ExecStart 是 regular updater binary 且只含 `serve --config <file>`（亦支持 `-config`）。不修改 unit、参数、环境文件或 state 目录；wrapper、symlink executable、未知策略均拒绝。执行 installer 的进程必须在被管理的 unit 外。

旧 updater 的退出行为另由 `old_updater_evidence` 私有文件固定：operator UID、实际 artifact hash、不可变源码/产物来源、独立 review 引用、`exit_policy=retains-managed-child` 与有效期限。其 SHA-256 固定到 handoff plan；没有实际旧产物的证明就拒绝停止。不能仅凭 systemd KillMode 或新源码推断旧 binary 的自身退出行为。

资源交接文件是实际 operator 的私有过期声明，必须由原责任在核对真实事实后出具。它包含原 window claim、operator UID、有效期限、已退出 writer 的 PID/start/launch 及可核验的 executable hash、runner 覆盖截止、未决操作处置、workspace 保留/释放责任和来源引用；installer 在运行时重新核对 writer 内核退出事实。历史 writer 已退出后无法补取的 executable hash 留空，明确表示 unknown；提供时必须是合法 SHA-256，不得拿其他文件代填。PID/start、launch 来源及实际退出核验仍必需，正在运行的相同 PID/start 不能凭空 hash 通过。server/updater/candidate 的完整身份与 hash 约束不变。paused/unknown 不是物理 fence；填入虚构 PID 或仅写「已批准」不是合法交接。生产材料只经授权 relay 获取，禁止查找凭据。

## 生成与审阅计划

所有路径均是 operator 的实际绝对路径；示例使用环境变量，不含默认生产资源。新 installer 与 successor 都必须来自已审候选。将现有 `tend upgrade status --socket "$SERVER_CONTROL_SOCKET" --json` 提供的完整升级视图放入 `recovery.original_ledger`（通过授权 relay 获取），不需要读取旧 DB 或私有 UpgradeRow，并保留原 start request ID、ledger revision。新 server 登记事务会独立保存原账本完整 UpgradeRow 与 SHA-256，原行不变。新的 `request_id`、`link_request_id`、recovery ID 必须固定且互不复用。

handoff 模板字段见 `updater.HandoffPlan` 与 `protocol.ForwardRecoveryPlan`。模板指定 unit、固定 updater/config 路径、新 updater candidate、原账本、successor release bundle、operator/窗口/资源证据和期限。`plan` 只读取事实，填充缺省的 state/config/unit/manager/产物摘要、MainPID 身份、lock device/inode 和独立 successor launch。已有字段不会自动刷新。

```sh
tend updater handoff plan --installer "$REVIEWED_UPDATER" \
  --installer-sha256 "$UPDATER_SHA256" --config "$UPDATER_CONFIG" \
  --plan "$HANDOFF_TEMPLATE" --json > "$PLAN_OUTPUT"
```

审阅逐项 guard 与 `.plan`，将 `.plan` 原样保存为私有 `HANDOFF_PLAN`。该文件不是外部审批证明；必须另核本次受控维护授权、候选、资源交接和全局唯一生产窗口。原 updater binary hash/来源和退出行为必须有实际证据，不能从旧源码推断。

## 受控第一跳与恢复

以下是能力的实际命令，仅在独立授权的执行窗口运行。`plan` 输出 allowed=false 时先处理具体 guard；不能降级为手工信号、改 unit、bootstrap 或换 state。

```sh
tend updater handoff apply --installer "$REVIEWED_UPDATER" \
  --installer-sha256 "$UPDATER_SHA256" --config "$UPDATER_CONFIG" \
  --plan "$HANDOFF_PLAN" --json

tend updater handoff status --installer "$REVIEWED_UPDATER" \
  --installer-sha256 "$UPDATER_SHA256" --config "$UPDATER_CONFIG" --json
```

首次 apply journal 记录 stop/install/start 意图。status 不发信号、不安装、不启动；systemd start 返回不代表已经 adopted，需读回实际状态。保留原 binary 和 journal 的证据；失败不自动回装 format 1 binary。结果不确定时不得盲目再次 stop/start。同一个 apply 重放只对账原请求，不发起新的 manager 动作。

installer 中断后可使用新的、明确的本机续接授权：`handoff plan|apply` 加 `--resume-request-id <new stable request ID> --expected-generation <journal generation>`，仍带原精确 `--plan`。先用 plan 核真实 manager 无 pending job、旧 updater/child 身份、原完整 state、同锁和实际安装 hash；apply 在 installer 锁下再次核验并持久记录续接请求。仅原目标/期限内的 updater stop/install/start 可以继续，不能重复 server start。每个续接 ID 只执行一次，同 ID 结果未知时只对账；不得删 journal、改原计划或续期旧 grant。已启动但身份/结果不明、manager 仍有 job、state 已变化或授权已过期时拒绝。

从已审 handoff plan 的 `.recovery` 原样保存私有 `RECOVERY_PLAN`。handoff adopted 后查看 generation，另执行新维护授权：

```sh
tend upgrade recover status --socket "$UPDATER_SOCKET" \
  --upgrade-id "$ORIGINAL_UPGRADE" --recovery-id "$RECOVERY_ID" --json

tend upgrade recover plan --socket "$UPDATER_SOCKET" --plan "$RECOVERY_PLAN" \
  --upgrade-id "$ORIGINAL_UPGRADE" --recovery-id "$RECOVERY_ID" \
  --request-id "$RECOVERY_REQUEST" --json

tend upgrade recover apply --socket "$UPDATER_SOCKET" --plan "$RECOVERY_PLAN" \
  --upgrade-id "$ORIGINAL_UPGRADE" --recovery-id "$RECOVERY_ID" \
  --request-id "$RECOVERY_REQUEST" --expected-generation "$GENERATION" --json
```

`apply --dry-run` 执行 plan guards。status 加 `--ledger-socket "$SERVER_CONTROL_SOCKET" --recovery-id "$RECOVERY_ID"` 可直接读取新 server 的独立 ledger、原完整快照及关联结果，并输出 receipt 一致性；未查询时明确 `ledger_observed=false`，不以 updater ACK 冒充独立 DB 观测。status 支持原升级/recovery/phase 筛选、`--after` cursor 和 `--limit 1..200`。重放必须保留完整同一 plan/request；冲突拒绝，已执行请求不会续期。generation 过期须先读 status，不能修改 plan 的原 state 摘要。

停止只发一次精确 SIGTERM，不强杀；stopping 重开只观察。启动不确定时 Find 恰一精确 launch 才收养，零或多个均 unknown。授权过期、child 不能证明退出或启动未确认时停止，保留不可逆事实和已接单数据，不恢复旧 DB。新 server 注册/fence/readiness 失败不生成 receipt。receipt 已提交但 DB link 失败只幂等追加关联，不再次替换进程。

## 独立复现与后续生产前置

普通定向回归：

```sh
go test ./internal/updater -run '^TestForwardRecovery' -count=1
go test ./internal/app -run '^TestForwardRecoveryLedger' -count=1
go test ./internal/upgradectl -run '^TestRecoveryHealthWithActualProcess' -count=1
```

真实 systemd 集成是显式 opt-in：从任务基线构建 `cmd/tend-updater` 为旧 fixture；从候选构建新 updater；将 `internal/testkit/recoveryfixture` 分别以 `-ldflags '-X main.mode=legacy'` 和 `-ldflags '-X main.mode=successor'` 构建为旧/新 service。fixture 没有模型、runner transport 或生产凭据。另从候选构建 `cmd/tend` 为实际 CLI，并设置 `TEND_TEST_TEND_CLI`。设置 `TEND_TEST_SYSTEMD_USER=1`、`TEND_TEST_OLD_UPDATER`、`TEND_TEST_NEW_UPDATER`、`TEND_TEST_OLD_SERVER`、`TEND_TEST_NEW_SERVER` 为这些产物路径，运行：

```sh
go test ./internal/updater -run '^TestHandoffSystemd' -count=1 -v
```

测试只创建唯一命名的 transient unit；验证 stop updater 保 child、同 inode 单锁、真实旧 terminal health、受控替换、两样本/atomic receipt、DB link 与原历史/已接单数据保留，finally 清理自身 unit 和精确 child。独立复核须重建 fixture 并实际运行，不能以默认 skip 当集成通过。Unix socket 测试需实际使用短、0700 临时目录。

生产仍需 fresh exact manager/updater 版本/hash/epoch、原 child PID/start/launch/hash、同锁同目录、原物理 writer/runner/窗口交接、授权期限及新候选协议/schema。成功后读回 new Current/proof/receipt、app link/fence 与原历史；后续正常 upgrade 仍走新正常授权并等待原窗口正式释放。旧 operation 的 changes_required、failed/unknown run 与 workspace 生命周期不由本功能改写。

远端 writer 的 PID 属于远端 kernel，不得填入本机 `writer_processes`。使用可选 `remote_writer_fence`，包含 `host`、绝对私有证据文件 `path`、固定 `sha256` 和 `source`。操作者必须先审阅经过身份认证的远端关闭回执、原 writer 绑定和实际退出观察；安装器仅校验证据文件及摘要，明确作为操作者见证传递，不声称在本机重新验证远端进程。本机列表可以为空，但本机退出证明和远端见证至少提供一种；若两者都提供，两者均须有效。

CLI 将安装器复制到独立私有目录，校验实际副本字节后执行同一副本；不执行校验后可能被替换的源路径。恢复期 owner operation 仍可返回已提交且重新授权的原始回执，新请求继续在事务中受恢复 fence 限制，既有归档 body admission 不变。
