# 独立自维护

实现依据为 D201；实施进展见[计划](plans/standalone-self-maintenance.md)。
服务和定时器模板仅供配置，本批不安装、不启动生产维护器。

`tend-maintainer` 使用独立 SQLite 和进程锁。事件唤醒后程序检查，最多每十五秒
合并一次事件补查，每五分钟再补查一次。独立系统定时器每小时执行 `audit`，
提交单次 Agent 检查；正在维修时排进当前会话。服务停止时，audit 可以取得同一
状态锁提交检查；已有进程持锁时不会绕过它创建另一个控制器。

默认检测将恢复 exhausted/blocked/unknown、执行 unknown、明确的启动失败，
以及两次连续采集失败作为诊断候选，
不修改业务状态。未覆盖的故障由小时审计发现并补检测。单次采集失败保留错误，
不能等同于 Issue 阻塞。没有候选或审计请求时不启动模型。

## 小时审计与同链介入

`last_audit` 记录入队时间。系统定时器和 `maintenance audit --scheduled` 对相同 UTC 小时只入队一次，显式手动审计可单独发起。每次审计收到上次已完成后的 Issue 变化（同一 Issue 合并到最新观测，包括 owner Task、状态、修订与任务数）以及待复核的 case ID，各最多 32 项。`more` 标示还有其他项，下次审计继续。只有原生 turn 返回完成才更新 `last_audit_completed` 和 `audited_through`。失败、中断、未知回执与控制器重启都保留原变化。清单带上次成功采集时间和采集错误；采集缺失或服务离线时，空清单不证明业务无变化。离开默认列表只表示超出探针当前可见范围。

定时审计成功后，`history` 保留定时请求 ID、输入 ID、原生 turn ID、冻结的游标和终态。`status.idle_audit_session` 显示可供下一轮复用的线程。下一轮用同一线程开启独立 turn，前提是后端、工作区和维护指令一致，且原生线程可用、空闲并支持分页读取当前 turn 回执。旧后端仍可完成单次审计。分页读取对照原 turn 或输入，避免历史累积阻止回执核对。已完成的空闲线程明确失效后，下轮在新线程读取已持久化的审计游标。已发送的输入仍沿原线程对账。清单为空时定时 Agent 仍独立选择证据并查找遗漏。轮询间隔由部署的定时器决定。

维护 Agent 或本机操作者可按原对象登记已核实的同链故障。`subject.json` 包含 `issue_id`、`task_id`、`run_id`、适用时的 `wait_id`、证据定位 `evidence` 和应验证的业务事实 `expected`。诊断 code 与这些原对象身份确定唯一 case。写入回执带回 `case_id`，重复观测或更换会话继续读取该案。

```sh
tend maintenance report --id "$DIAGNOSTIC_CODE" --reason "$DIAGNOSIS" --file subject.json
tend maintenance intervene --id "$CASE_ID" --reason "$ORIGINAL_ACTION_RECEIPT"
tend maintenance resolve --id "$CASE_ID" --reason "$DIAGNOSIS" --verification "$OWNER_READBACK"
```

`intervene` 记录已使用的正常入口及其回执，状态为 `awaiting_verification`。对于自动识别的 CI 接续，原 owner 已随等待保存同一候选的终态、事件和来源消息。程序复用原探针取得的 owner Task，每个补查周期最多轮转 8 个点查。自动发现定位各 Task 最近一次因 checks 终态而接续的等待，已登记 case 按原 Run 继续复查。更早的其他 CI 等待可由原 Run 定向核实。队列、运行中的 owner、有效提醒、原生回执待对账、父级或本级准入暂停、未完成的依赖和明确延后都保持静默。已结束的交付处置按原 head/base/target 核对。历史等待缺少来源、候选或基线变化、无法确认处置时继续保留未知。

```sh
tend task ci-continuation "$OWNER_TASK" "$ORIGINAL_RUN" --json
tend task resume-wait "$OWNER_TASK" --revision "$TASK_REVISION" --data '{"wait_id":"<original wait>","reason":"Restore the original CI source","restore_ci_notice":true}'
```

只在原 case 和业务授权仍有效、诊断读回 `missing` 时调用恢复命令。服务端在同一事务内重新验证条件并将原消息与原事件交回同一 owner。恢复事实与原等待在同一事务保存。写入回执丢失时使用原身份点查；修复后的提醒再次不可见时保留待核，继续复用原介入记录。原 owner 收到来源时显示 `notified`；owner 实际读取来源并正式回复或发布明确引用该来源的通知，在此 turn 完成后显示 `processed` 并自动核销。这个范围止于 owner 处理 CI 结果；合并、部署和其他外部操作仍沿各自授权与回执核验。

## 配置与观察

复制[配置示例](examples/tend-maintainer.json)到私有配置目录，填写真实绝对路径、
owner token 文件和共享原生控制 socket。状态目录须为 0700。配置中的 backend
仅用于首次初始化，之后通过 CLI 原子采用新的配置，重启不会覆盖已采用的后端。
通过 `network_access` 显式允许维修命令访问 API、Git 和 CI；需要额外落盘的位置
用绝对路径 `writable_roots` 配置，或把 CLI 请求记录与产物暂存放在维护工作区内。
填写[维护范围模板](examples/maintenance-instructions.md)，明确实际 CLI 配置、
仓库和受管服务。范围文件只在新建原生会话时读取，不包含凭据。

Codex 控制 socket 和 `app-server proxy` 承载 WebSocket。维护器直接连接同一个
socket，不启动或停止共享 daemon；原生 session ID 与 rollout 路径保留在状态中。
可在 Codex App 通过该会话索引观察，CLI 使用原生 remote resume。TraeX 使用
自己的 TUI；需要让其共享 app-server 提供受保护的 `ws/wss` remote 地址，可在
backend 中记录 `tui_url`。TraeX CLI 的 remote 参数不支持 Unix URL。

```sh
export TEND_MAINTENANCE_SOCKET="$MAINTENANCE_CONTROL_SOCKET"
tend maintenance status --json
tend maintenance cases --after 0 --limit 50 --json
tend maintenance show --id "$CASE_ID" --json
tend maintenance history --after 0 --limit 50 --json
tend maintenance session --json
tend maintenance overview --json
codex resume "$SESSION_ID" --include-non-interactive --remote "unix://$NATIVE_SOCKET"
traex resume "$SESSION_ID" --include-non-interactive --remote "$NATIVE_TUI_URL"
```

server 可选配置 `maintenance_socket`，仅为已认证 owner 提供只读概览。诊断页面显示
检测/审计时间、候选数量、原生会话索引和维护器更新阶段。报告失败不影响维护器
运行；首版不接 Tend 用量面板，不创建额外 Run。

## 外部切换与恢复

`config` 不带输入时读取当前配置，带完整 backend JSON 时采用目标配置。`when-idle`
等当前原生 turn 终止；`interrupt` 发出一次中断并观察实际终态后切换。结果未知时
先对账，不重复发中断或输入。只有中止交接附上前一个 Agent 和 session ID；
普通退出不向新 Agent 传前驱，也不注册任何历史读取工具。

```sh
tend maintenance config --file "$BACKEND_JSON" --mode when-idle --dry-run
tend maintenance config --file "$BACKEND_JSON" --mode interrupt --json
tend maintenance check --json
tend maintenance audit --json
tend maintenance defer --id "$CASE_ID" --until "$UNIX_SECONDS" --reason "$DIAGNOSIS"
tend maintenance resolve --id "$CASE_ID" --reason "$VERIFICATION"
tend maintenance pause
tend maintenance resume
tend maintenance review --base main --head "$CANDIDATE_COMMIT" --json
```

`review` 排队到维修 turn 安全结束后，通过原生 `review/start` 的 inline 模式运行
独立 review，再让同一 thread 的维修 Agent 读取结果并推进。review 完成只代表
执行结束，不自动推导审核通过。检查配置应明确模型与 reasoning effort；仓库的
额外审核要求仍需完成，不绕过候选变化后的重新验证。

写命令输出 request ID 和预期修订。完整原请求可用 `--dry-run` 保存，重试必须复用
原 ID、修订和输入。`request --id` 查询持久收据。未知创建保留首次可记录的创建错误和收到的线程标识。查到可核实的原线程时，用 `reconcile --session-id` 认领。经本机操作者核实原生日志与原创建的关联、该线程已退出以及从未执行 turn 后，可以沿同一入口结清。

```sh
tend maintenance reconcile --revision "$CURRENT_REVISION" --request-id "$RECONCILE_REQUEST_ID" \
  --creation-id "$ORIGINAL_CREATION_ID" --session-id "$ORIGINAL_THREAD_ID" \
  --reason "$ALLOCATION_CORRELATION" --exit-evidence "$NATIVE_EXIT_EVIDENCE" \
  --verification "$NO_TURN_EVIDENCE"
```

`--reason` 记录如何把线程分配关联到原创建。`--exit-evidence` 与 `--verification` 分别引用原生退出和未执行的证据。超时、断开和查无记录均不足以结清。控制器核对当前修订、原创建身份和未提交输入的持久状态，历史及收据一并写入，保留原待办。暂停只停止新模型调度，当前原生工作仍可观察；部署层停止 timer 时保留状态和配置。

旧版创建状态没有记录定时待办与请求 ID 的关联。状态显示 `legacy_audit_through` 时，先从原调度记录核对待办对应的命令收据，并用 `maintenance request --id` 核对原输入。可在创建对账时附加 `--audit-request-id "$ORIGINAL_AUDIT_REQUEST_ID"`，也可另行执行 `reconcile --audit-request-id "$ORIGINAL_AUDIT_REQUEST_ID" --reason "$AUDIT_CORRELATION"`。在原身份明确前，后续定时请求继续排队，已有维修可照常续行。

原生 turn 失败或被人工中断时，历史保存真实 outcome、原 turn/input 和触发索引，
`status.failure` 与诊断概览报告故障，待办保留。普通程序检测不再次调用模型；新的
`audit`、`resume` 或显式 backend 配置才允许新尝试，原来的人工暂停仍需 `resume`。
这类新尝试不自动附加前会话索引。持久化失败直接出现在只读状态和日志中，
维护器以失败退出，由进程监督器恢复，原生 daemon 继续运行。

## 发布与更新

Actions 在相同提交和 GitHub Release 中附加 `tend-maintainer-main-<commit>-linux-<arch>`
独立包（amd64/arm64）。`component.json` 固定源码、平台、大小、hash 和状态兼容范围。
原四组件主包和 service manifest 不变。可用
`tend-updater verify --role maintainer --release "$COMPONENT_DIR"` 验证。

配置[维护器 updater](examples/tend-maintainer-updater.json)后，通过完整计划更新：

```sh
tend maintenance upgrade-plan --release "$COMPONENT_DIR" --json
tend maintenance upgrade-status --json
tend maintenance upgrade-apply --upgrade-id "$PLANNED_UPGRADE_ID" --json
```

旧维护器先保存待办、会话和安装计划，再停止新调度与状态写入。updater 安装并启动
候选；候选保持准入关闭，核对固定进程和 artifact 后提供健康。只有读取 updater 的
不可逆提交收据后恢复调度，原生会话继续沿原身份对账。共享 daemon 不受更新影响。

若 updater 明确拒绝且其返回状态证明原安装尚未建立，计划保留为 `rejected`，
维护检测和控制恢复；排除拒绝原因后可再次对同一计划执行 `upgrade-apply`。
断线或响应无法确认则保持 `submitting`，仅查询原安装，不能据此重新提交。
候选提交后再次被进程监督器重启时，使用原终态收据、提交证明和当前进程身份
核对安装结果，不要求当前监督操作仍是原安装操作。

业务 server 使用现有 `tend upgrade` 受控入口，完整发布自动按 updater → 服务顺序
推进。维护器的固定 updater 使用同一个 D200 独立交接接口；角色准入包含
`maintainer`，保留原维护器进程和原监督锁。普通业务发布集与独立维护组件分别加载。
维护器 updater 的 seed 固定主发布目录、发布集 hash、原 request ID、有效期及
本机 unit/config，格式见[普通 updater 交接](operations/controlled-upgrades.md)。

```sh
tend updater self-update plan --installer "$NEW_UPDATER" \
  --installer-sha256 "$NEW_UPDATER_SHA256" --config "$MAINTENANCE_UPDATER_CONFIG" \
  --plan "$SELF_UPDATE_PLAN" --json
tend updater self-update apply --installer "$NEW_UPDATER" \
  --installer-sha256 "$NEW_UPDATER_SHA256" --config "$MAINTENANCE_UPDATER_CONFIG" \
  --plan "$SELF_UPDATE_PLAN" --json
tend updater self-update status --installer "$NEW_UPDATER" \
  --installer-sha256 "$NEW_UPDATER_SHA256" --config "$MAINTENANCE_UPDATER_CONFIG" \
  --plan "$SELF_UPDATE_PLAN" --json
```

只有原交接收据与实际新 updater、原服务及独占锁匹配后才继续维护组件更新。
未知结果沿原请求读取状态；既有独立交接不会暂停 Tend 全局业务。先更新各自
updater 和业务服务，维护组件最后更新；各步保留不可变产物和实际进程收据。
启用新维护服务前先停止旧外挂调度，并确认旧维护会话已结束或完成明确中止交接，
避免两套独立控制器并行；本批仍不启用生产单元。
