# P6b Orca 精确原生入口：最小实际报告链（独立实现候选）

P6a 是已实施的 CLI/API/补充消息/来源/咨询 caller 候选；其 native-entry 目前只有真实 workspace/provider/冻结 run、明确未知 session/controller，不代表 P6 全部完成。用户要求的 Orca session/terminal 身份是本片必要剩余。保持 P6a/P4/P8 冻结原件；新 ignored 副本实施、当前基线为 P4 r5 加已审修正；最终依赖 P4 r6 与 P5 r3 完整组合。无 UI、部署、真实服务/账本或新 provider。

## 已核实的唯一真实调用链与协议判断

- `harness/orca/worker.go` 的 `workerState` 已持久 terminal/pane/worktree/session_id、root runtime/session/turn事实；`save()` 经 `WorkerSpec.OnThread` 写原 runner journal，重启恢复原 run 的同一 JSON。launch、确认 provider session、takeover/handback/recovery 都已有真实调用点。不能仅据 workspace path 或配置猜 terminal/session。
- 采用既有 `harness.WorkerEvent` → `runner/execute.go` 的 OnStartupEvent / worker.Events → `journal.AppendEvent` → `flushEvents` → `runnerapi` 的 `runs.events` 分支 → `app.RecordRunEvents` → `run_logs`。新增一个 typed 小 body 的事实 kind `worker.native_identity`；不新增 daemon/outbox/第二 identity 表或 native 控制 API，不上传 workerState、prompt、bridge token 或私有 agent definition。
- `protocol.RunEvent` 的既有 `kind` 是字符串，`body` 是 `json.RawMessage`。当前及固定旧基线 `RecordRunEvents` 接受符合正则的未知 kind 并原样保存；只对列出的状态 kind 执行业务转换。因此本片不增加 Envelope/RunnerRuntime/EventsPayload 字段，不需为既有 opaque body 人为加 capability 框架。wire1/0默认和旧 run 请求不变；旧 server 保存此事实，新 server 对旧 runner 没事件时显示 unavailable/unknown。实施时用实际旧导出形状/handler路径验证，不以生成 DTO 的存在代替兼容证明。若发现真实旧 decoder 对此 body/kind 有额外限制，则停下报告，不收紧旧请求默认。

## 最小事实合同

仅报告实际知道的 Orca worktree/providerID、terminal handle、pane key、provider session ID，以及已观测的 Orca runtime identity（有则填，无则未知）。同一事实含原 run 身份，由既有 run principal/auth/runtime fencing、run event seq/native ID 与原 journal 序列绑定；不从当前 host 默认 worker/inventory 选择替代原 pin。主机上的安装可信，不做每层二进制证明。

launch 成功有 terminal但尚未确认 session 时允许 partial；session 后续真实确认再报告完整事实。恢复读取原 state可重新报告同一事实，经原事件 journal去重与序号排序，不启动第二 turn、不发 message/PTTY/control；terminal/session被真实观察为不一致时显式 unknown/changed，不能静默把 owner会话当原执行者。控制归属只能由既有 takeover/handback/实际观察事实表达，不从可查看身份推断。报告不带新执行权限，原 SetControlMode 与 owner takeover/resume CAS 保持。

server owner GET/CLI 从**该 run 的持久事件**读最新已上报事实，workspace 固定查 `workspace_runs`，不查当前 task 可能已 start_over 的新 binding。不得自动拼未核实的 URL：提供真实原生定位字段；若需要深链先核当前 Orca CLI/TUI 的实际入口合同。明确 known/partial/unknown；Cove仍 adapter_not_implemented，没有完整 adapter。

## 必要文件与实际行为验证

- `internal/protocol`：typed NativeIdentity body/owner NativeEntry字段，真实 APITypes生成；不改 runner信封字段。
- `internal/harness/orca`：在现 launch/session观察/恢复/takeover事实点发布最小事件；原 state持久化、停止/重恢复顺序不变。只做身份观测，不新 provider 或控制器。
- `internal/runner`/`runnerapi`：尽量不改生产代码，复用真实 journal→HTTP→server pipeline，定向测试实际连接。
- `internal/app/native_entry.go`/owner GET/CLI：原 run持久事实投影；无权限扩展/控制写。

必测：真实 fake Orca launch/poll获得 terminal/session→事件→journal close/reopen→重试上传→owner GET/CLI原字节身份；未知/缺 session 与旧 runner无事件；provider session替换/恢复不被默认为原run可控；后继 run/start_over不污染旧run视图；重复/迟到事实依原seq，只留原run；看入口零控制/outbox/PTTY动作，owner-only控制负控沿现caller。旧 server保存通用 kind/body、新 server接旧事件、wire1/0 strict形状回归；无全检/生产操作，最终 root组合与独立评审。

## 本片实际结果与剩余边界

已完成最小报告链：Orca worker 从实际持久状态发布 `worker.native_identity`，复用原事件 journal、重试上传与服务端 `run_logs`；owner 原 native-entry GET/CLI 显示同一 run 的 terminal、pane、session、runtime、launch 和最新已记录序号。worker 恢复重新报告原状态，重复与迟到上传遵循既有事件序号；没有新增 provider、控制输入或会话启动 API。已记录定位不表示取得控制权。

实际定向验证覆盖 fake Orca launch/session 确认、session 变更显式 unknown、丢失已接纳回执后重试、journal 关闭再重启原 run、owner GET/CLI、owner start_over 后旧 run 保留原资源、迟到事实与权限负控。runner/journal/HTTP/owner CLI 的组合 race 测试通过，恢复仍只有一次 native launch。首次组合测试暴露了旧 git workspace 类型提前返回的问题，已改为消费原 run 实际 provider 事实并经同一路径验证。

wire 1/0 的当前 handler 以及固定 P6a 旧 handler 均接受、原样保留并去重该 opaque kind/body，未增加信封字段、必填校验或默认限制；本片无迁移、无需额外能力框架。owner API schema 与 TypeScript 类型由正常命令生成，typecheck、相关包 vet/staticcheck 与定向 race 已通过。

已只读核对现有 Orca CLI 合同：`terminal show --terminal <handle> --json` 读取指定 terminal；`terminal focus`（别名 `switch`）是可产生桌面导航的独立动作。本片返回实际定位字段，不自动调用 focus、猜测深链或把消息当 PTY。Cove 仍明确 `adapter_not_implemented`。

这只是 ignored 候选结果；没有运行完整仓库检查、规定 CLI review、真实 Orca/模型或生产服务验证。原 P6b 冻结时尚待最终组合；本副本现已纳入已审 P5 r3 与 P4 r6，Attention/直聊/咨询与 identity 原 journal 路径的组合定向 race 已通过。未重做前件修复，正式全检与规定 CLI review 仍待执行。
