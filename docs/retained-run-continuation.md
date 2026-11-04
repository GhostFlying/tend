# 保留普通 Run 的严格附着

Refs: D159、D142。该入口只处理命名普通 Orca worker；manager 与 app-server 使用各自合同。
维护完成和保护 linked 不证明原业务已完成，也不会自动允许原运行继续。

先读取原 Run 的账本对账结果：

```sh
tend run maintenance-reconciliation RUN --json
```

读取不访问 provider。输出包含原 Run/task/action/generation/spec/host/lease、完整不可变执行
pin、Task revision、完整 protections 与 protection_set_sha256、原冻结请求和当前未结
native/业务回执、blockers 及已有决议。`original_requests_json` 保留原 `null` 或 `[]`
编码。`can_continue` 只说明账本准入，不是原生 session/turn 存活或业务恢复证明。

将返回的 `identity` 字段放到 payload 顶层，并完整复制 `protections` 和
`protection_set_sha256`，添加独立 `reason`。不修改原 Task/action/spec/lease/pin，
不通过手写空请求集合声明已经结算。显式引用来源时同时提供 `source_message_id` 与
`expected_source_revision`；主会话必须使用当前真实准入的来源，Issue owner 仅能处理
自己当前责任中具有原始归属的 Task，不能控制祖先、后代或兄弟 Issue 的运行。

用读取到的原 Task control revision 与独立新 request ID 冻结完整请求：

```sh
tend run continue-retained RUN --revision REVISION --request-id REQUEST \
  --file proof.json --dry-run > original-envelope.json
tend request replay --file original-envelope.json --json
```

也支持 `--data` 和 `--file -`。缺少显式 CAS 在发起 HTTP 前即拒绝；命令不会刷新 revision
后重发。未知结果时保留原 ID、输入、revision 与 Envelope，先查询再按原字节显式重试：

```sh
tend request show REQUEST --json
tend request replay --file original-envelope.json --json
```

不要换 ID 或改 payload 重试未知动作。同 ID 不同输入冲突。新维护 capture、控制变化或
新未决业务使旧决议失效；这不改变旧请求已有回执，也不代表可以将未知请求替换成新决议。

动作提交只记录独立附着许可；同 Run、同 lease 的原 journal Hello 才能续活原 lapsed
租约。新 runner 必须报告 strict-retained-resume 能力，并可解析原不可变 pin；旧 runner
继续 retain。缺失/held journal、坏 Thread、原 terminal/session/runtime/launch/turn
不一致或原生观测不明时保持待对账，不创建新 Run、session 或 prompt。

严格模式只附着同一原 turn：当前 running 可以继续观察，completed 且 operations settled
可以继续读取原结果。cancelled、failed、unknown 或未结操作不获得新发送权限；若需要
新的同 session continuation，应先对账原 pending send 并另行使用有持久去重身份的明确
业务合同。新模式不会隐式发送 continuation 或 shutdown interrupt。原生身份与结果仍须
通过现场证据验收，不能将 lease active 当作原任务已经恢复。

首次附着许可只接回 started journal。只有此前保存了同一附着请求、且已经记录真实
WorkerDoneAt 和 WorkerResult 的 worker_done/submitting/releasing journal，才允许恢复
原结果收尾；旧 give-up/release 不能借此解除结算保护。strict 最终请求只有业务成功回执
才清理工作区和 journal；accepted/pending_reconciliation、失败或未知回执均保留原请求
身份继续对账。附着 setup、原生观察或本地事件保存错误同样保持 pending，不作为普通
worker 失败关闭原 terminal。明确 Stop、paused、held 仍遵循既有独立控制授权。
