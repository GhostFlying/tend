# 有界维护 CLI

`tend updater bounded` 使用独立、按 SHA-256 固定的 installer。正常维护仍保留原有排空规则；强制接管通过独立授权明确承接本次 predecessor 的未知工作。server、每个 runner target 的预算、物理退出和业务恢复分别记录，不能把 CLI 成功退出当成它们全部完成。

## 计划与授权

先准备完整 source plan 和 `BoundedMaintenanceAuthorization` 草案。所有输入文件和 installer/config 路径必须是绝对路径。草案至少包含稳定 `request_id`、`role`、`reason`、`expires_at_ms`；`plan` 由真实 installer 检查环境并填写 source、target、当前 generation/phase 和 canonical `plan_sha256`。CLI 固定原文件字节，不丢弃 source plan 中其它字段。

```sh
tend updater bounded plan \
  --installer "$INSTALLER" --installer-sha256 "$INSTALLER_SHA256" \
  --config "$UPDATER_CONFIG" --plan "$SOURCE_PLAN" \
  --authorization "$AUTHORIZATION_DRAFT" --json
```

保存计划输出中的完整 plan/authorization。`apply`、`force`、`extend`、`cancel`、`takeover` 均要求完整的独立授权；`request_id`、`expected_generation`、`expected_phase` 和 `plan_sha256` 缺失时拒绝执行。命令行重复提供 request/CAS 时必须与授权一致，不能借重复参数修改授权。

旧 updater 的实际 generation 可以为零。授权中的显式 `expected_generation: 0` 是合法的原 CAS；缺失该字段或写成 `null` 不会被默认为零，也不会自动换成当前版本。

```sh
tend updater bounded force \
  --installer "$INSTALLER" --installer-sha256 "$INSTALLER_SHA256" \
  --config "$UPDATER_CONFIG" --plan "$SOURCE_PLAN" \
  --authorization "$EXPLICIT_FORCE_AUTHORIZATION" --json
```

`force_on_drain_timeout: true` 是授权文件中的预授权字段，限定原 component、target、candidate、阶段和有效期。它不是等待超时后由 CLI 临时创造的权限。`extend` 使用独立 request、fresh CAS 和新授权窗口；读取 status、心跳和原请求重放都不延长预算。

授权中的 `timeouts` 使用毫秒，默认如下：

| 字段 | 默认值 |
| --- | ---: |
| `overall_ms` | 360000 |
| `handoff_ms` | 60000 |
| `fence_ms` | 30000 |
| `drain_ms` | 60000 |
| `stop_ms` | 30000 |
| `kill_ms` | 10000 |
| `backup_ms` | 30000 |
| `start_health_ms` | 90000 |
| `complete_ms` | 30000 |

分项合计 340 秒，component/host 总预算独立为 360 秒；不能通过阶段切换、轮询或重复 apply 续期。

## 超时与对账

`status` 和 `reconcile` 直接调用独立 installer，不依赖旧 updater 的 control socket，默认命令等待 10 秒。其它命令的外层等待默认 370 秒，可用 `--timeout` 指定一个有限正值。命令等待结束不证明受管服务或 native worker 已退出。

```sh
tend updater bounded reconcile \
  --installer "$INSTALLER" --installer-sha256 "$INSTALLER_SHA256" \
  --config "$UPDATER_CONFIG" --request-id "$ORIGINAL_REQUEST_ID" \
  --timeout 10s --json
```

变更命令失去回执时输出 `outcome: unknown`、原 request/CAS、已收到的 installer 输出和下一条 reconcile 命令。不自动重试，不换 request，不推断 stop/start 没有发生。保留原 plan、授权与 installer hash，先对账原请求，再决定是否逐字重放。

`takeover` 使用同一独立 installer 的接管入口，仍需完整的新授权及 fresh CAS。installer 必须核对旧 updater 和旧服务的精确身份；尚未证明旧 writer 退出时不能启动第二个 writer。强制替换 runner 不证明其旧 native worker 退出，不释放 held slot，也不清除 journal unknown。

takeover 授权额外提供 `updater_source`（PID、start_ticks、launch_id、sha256）和 `updater_lock`（device/inode）。`data_identity` 则独立绑定原业务库或 journal；不能用 updater lock 的空闲代替业务 writer 已退出的证明。server 的 `plan` 从固定配置的唯一 ledger 文件取得实际 path/device/inode，变更授权必须保留并重新核验该身份。相同路径换成另一个 inode、缺失身份或错误路径会在停止、备份及启动前被拒绝；schema 迁移不要求文件的旧版本号保持不变。

`--dry-run` 只输出 installer 参数和完整 typed input，不执行 installer，也不偷偷转成一次 plan 调用。它用于审阅原请求，不构成物理状态或安装成功的证据。

## Runner owner 授权

独立 runner 授权通过正式 owner operation `hosts.authorize_bounded_maintenance` 提交。完整 `RunnerBoundedMaintenancePlan` 绑定 host、配置中的 `node_selector`、原 runner instance/generation、旧 process、journal identity、目标 release、原 host control CAS，以及独立授权。配置 selector 不代表 server 已核验物理 node 身份；installer 仍须核验本机 authority、固定 instance、原 process 和 journal。嵌套授权的 source/target/data identity 必须与外层字段完全相同，plan digest 按共享协议计算。

plan 中的 `release_dir` 必须是候选 bundle 的绝对路径；授权中的 target 不能代替 installer 对该目录内容和 hash 的实际核验。

```sh
tend host authorize-bounded-maintenance "$HOST_ID" \
  --file "$RUNNER_BOUNDED_PLAN" --dry-run
```

保存输出的原 Envelope 后，移除 `--dry-run` 提交。CLI 直接使用 plan 内的 request/CAS，不自动刷新。owner grant 不代表 server 核验了 kernel start ticks 或 journal inode；installer 仍须在本机独立验证这些事实。

owner 提交结果 unknown 时，先读实际维护授权、升级批次及原 Run 的对账状态。保留原业务身份和输入，不能从缺少传输回执推断未执行。只有确认原动作未发生且仍有授权时，才可用 `tend operation submit --file original-envelope.json` 单次提交；其自身不对账、不刷新 CAS，也不自动重放外部动作。授权、停止、安装、health、ledger link、恢复 ACK 和原业务恢复分别核实。

替换后的旧 Run 保留恢复隔离。通过 `tend run maintenance-reconciliation <original-run> --json` 取得原身份、全部 protection 和 `recovery_authorization_id`，独立核实后把这些原字段放入 `tend run continue-retained` 的完整 payload，并保留原 Task control revision。普通 host mode 恢复不释放这份隔离；缺少原授权、证据或尚有未知原生请求时不能假称旧工作已恢复。

successor 已独立对账后，owner 可单独释放全局新工作准入：

```sh
tend host release-bounded-admission "$HOST_ID" \
  --file "$EXACT_SUCCESSOR_ADMISSION_RELEASE" --dry-run
```

完整 `BoundedRunnerAdmissionRelease` 包含新的 `request_id`、原 `recovery_authorization_id`、host、新观察到的完整 successor binding、runner generation、journal identity、fresh host `expected_control_revision` 和 reason。该操作仍以 payload 中的原 request/CAS 组成 Envelope，不自动刷新；unknown 先查原请求再逐字重放。

释放全局准入只允许新工作经过既有条件调度，保留所有旧 Run 隔离、unknown、held slot 和 workspace。它不把旧工作标为完成，也不把未观察的 successor 宣称为 Ready。
