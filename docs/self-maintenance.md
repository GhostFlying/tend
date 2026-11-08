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
原 ID、修订和输入；`request --id` 查询持久收据。未知创建只能在核实原生记录后用
`reconcile --session-id` 绑定原会话，不因超时创建替代执行。暂停只停止新模型调度，
当前原生工作仍可观察；部署层停止 timer 时保留状态和配置。

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

业务 server 使用现有 `tend upgrade` 受控入口。普通 updater 自更新使用其独立工作
提供的受支持交接接口（[PR #280](https://github.com/GhostFlying/tend/pull/280)）；
其固定角色准入还需要覆盖 `maintainer`，并联合验证保留子进程、独占锁及收据。
在该能力可用并验证之前，不用强制恢复或手动覆盖代替。
升级顺序为 updater、业务服务、维护器，分别保留不可变产物和实际进程收据。
