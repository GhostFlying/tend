# 集中 TraeX loop 与 Orca 原生终端（D140）

## 目标与边界

按 Agent 显式选择本地或集中运行。集中模式在服务端用户现有 TraeX 配置、认证与 session 目录运行 app-server；runner 保留真实工作区、exec-server、Git/工具凭据及 Orca 原生 TUI。工作区仍沿已有 git/Orca backend 准备，保持任务账本、冻结执行配置、独立验收与 run 租约。无需容器，不新增 Web Agent 界面，不实施生产迁移。

## 接口与实现顺序

1. TraeX 适配器支持连接现有 JSON-RPC 流与显式 Environment；创建和恢复必须验证 environment/cwd，集中模式禁止回退本机环境。已有 stdio 默认不变。
2. 中心配置显式启用 native worker broker。新传输端点沿 runner/run bearer 与节点身份鉴权，仅接受有效实现/验收 run；runner 主动建立执行通道，中心每 run 启动原生 app-server，并通过内部回环代理连接执行通道。控制客户端断开不销毁仍被 runner 持有的会话；执行通道断开撤销原生进程。原生 session 文件留在中心用户 home，进程退出不删除历史。传输是原生执行连接，不另建业务调度队列。
3. runner 为集中 run 创建本机 exec-server 与受控 TUI 入口；执行通道、进程组与 run 生命周期绑定，fencing/取消/退出停止本机执行。通信不明时保留 journal 与 unknown，不重发业务动作。冷恢复固定原 run、workspace 与中心连接身份，拒绝改绑。
4. Agent 配置增加显式集中 worker 类型，保留本地类型长期可用。接入既有冻结快照、可用性探测和后端选择；不借 Orca 本地 Agent 工厂启动第二个 loop。新能力可选启用，不改变旧 runner 请求含义；不提升现有 2/1 通信代次，旧端不支持时显式不可用。
5. 中心注入不调用模型的准备记录，生成可恢复历史后，runner 在同一 Orca workspace 打开 `native-tui` 客户端。TUI 经限定 thread 的入口连接中心；Tend 动态工具请求只由控制连接回答。原生状态由中心事件提供，不使用 runner 上不存在的中心 transcript 路径。终端创建先记录稳定请求与身份，未知结果不得重开。
6. 自动执行期间允许查看；人工首次写入先调用现有 takeover 并暂停自动驱动，成功后才转发输入。held 保留会话、工具处理与终端；明确 resume 后核清原生 turn 再继续。关闭终端或断网不自动交还；暂停、取消、租约失效与人工持有分别处理。完成和 release 必须停止原 run 执行资源，不把 WebSocket 关闭当作停止证明。

## 验证与交付

- 默认本地行为回归；新配置验证、冻结恢复与不支持端显式拒绝。
- 实际 TraeX/exec-server，确定性模型替身：命令与文件落点、session 落点、动态工具回调单一回答、TUI 同 thread、持续查看、打断、交还、关闭 TUI 后继续。
- 鉴权、越 run/thread、空会话恢复、exec 断线/重启、冷恢复不改绑、held 不自动完成、取消与 fencing 停止、未知启动不重复。
- 运行相关 race 测试、make check 与独立 review；保留可重复的 integration 测试，生产启用另行处理。

## 已验证事实

原生 TUI 可跨机器 resume 同一 app-server thread，并持续观察控制客户端发起的 turn。直接连接时 TUI 会抢答动态工具，须过滤。尚无首个 turn 的空 thread 可能不能 resume；须等原生历史可恢复。真实 Orca PTY 已验证；其 Agent preset、进程识别与原生历史读取是不同能力，不能以终端可输入代替完整状态验收。

## 第一版配置与恢复合同

服务端显式增加 `native_workers: {"traex_bin":"traex"}`；可选 `traex_home` 指向中心用户现有配置，省略时沿用该用户默认 home。默认进程 cwd 在服务端数据目录的 `native-workers` 下，避开服务端仓库。此设置不启动第二个协调 Agent，不修改已有 main/Issue owner 的配置。

runner 的 `agents.execution.workspace_backend` 选择 `orca`，沿用现有的 Orca 安装、仓库及目录工作区登记。Agent source 格式 2 可新增 runtime：

```json
{
  "runtime_id": "central-traex",
  "revision": 1,
  "kind": "central-traex-worker",
  "cli": "traex",
  "executable_ref": "traex",
  "declared_purposes": ["worker_run"],
  "enabled": true
}
```

具体 Agent 仍引用 runtime，并明确 model、effort、类别和 revision。`executable_ref` 是 runner 上执行工具和显示 TUI 的原生 CLI；中心的 CLI 由服务端配置选择。安装 pin、source reload、inventory 注册和独立 reviewer 沿既有流程，不复制模型登录文件。相同 runner 可继续登记本地 app-server 或 Orca Agent。第一版集中 worker 只声明 `worker_run`，不伪造 Issue owner 协调能力；已有 owner runtime 保持显式配置。终端启动使用 POSIX shell，Windows runner 暂不报告此适配器可用；桌面客户端的平台不受此限制。

端点 `/runner/v1/native/status` 对已认证 host 报告是否启用；`exec`、`control`、`tui` 是已冻结中央 runtime run 的 WebSocket 传输，不增加业务动作入口。run 必须有当前 active lease，且原冻结 inventory 的 runtime kind 与 revision 匹配；校验沿原节点和 bearer，不把历史 session ID 当成授权。

启动先以原生 user 消息记录完整的冻结任务 brief，不调用模型；独立记录准备消息的 pending/confirmed 状态，发送不明时阻断重放。等待原生 TUI 确认同一 thread 后才启动任务首 turn，因此首 turn 前的暂停恢复不会丢失任务要求。TUI 只使用无需模型凭据的前端启动配置；它的 provider、Environment 和权限默认值不能覆盖中心会话。实际模型认证、请求和历史始终由中心处理。

人工持有保留原连接。暂停只有在原生 turn 已确认空闲后才保留执行通道；interrupt 被拒绝、超时或无法确认时先 fencing，保存停止证明及待对账标记，不以暂停原因冒充停止确认。交还先等待当前原生 turn 空闲，再发新的继续输入；不打断仍在完成的人工 turn。关闭 TUI 不交还；重连只重建视图，不重发输入。中心在原生进程 cwd 的 `run-bindings` 下保存 run/cwd/thread 传输绑定，创建原生 thread 前持久化 pending，成功回执先持久化绑定再交付 runner；不接受 runner 指定未绑定的既有中心历史。该目录随中心数据保留，丢失、变更或 pending 未消解时阻断恢复。它不保存业务控制状态。

runner 已确认执行进程停止后，将停止事实与原 thread、工作区和 provider 绑定保存在 journal，允许新连接恢复原中心历史；held run 冷恢复后的取消也使用该停止证明完成释放。崩溃后没有停止证明、原终端创建结果不明、绑定变化或原历史缺失都进入既有对账，不新建替代 thread。已确认关闭的终端可以在显式 resume 时重新创建同一 thread 的视图；这不重发原人工输入，也不启动第二个 loop。已确认的验收结论先保存到原 run 的 StateDir，再返回工具成功，跨 attempt 恢复时保留该记录。执行资源释放撤销前端 capability，终端本身可保留退出后的文本，不把后续 owner shell 当成可自动关闭的 run 进程。

真实集成测试默认使用模型替身和实际 TraeX/exec-server；设置 `TEND_NATIVE_ORCA_BIN`、`TEND_NATIVE_RUNNER_BIN` 可进一步使用独立 Orca runtime 和新编译的 runner。测试 runtime 必须独立于日常 workspace。测试不导出或复制现有模型凭据，模型响应由替身提供。

原生空 thread、明确拒绝及请求计量在冷恢复后的限制见 [验证记录](../findings/central-traex.md)。
