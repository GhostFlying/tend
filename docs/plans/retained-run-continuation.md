# 保留普通 Run 的原身份继续

Refs: D159、D142。基线为已冻结的 writer/concurrency 修复候选；本批独立 worktree、唯一 writer，正式合入前重绑实际前置 main。

## 问题与范围

恢复维护登记保护了当时所有未结束 Run。普通执行 Run 即使没有未结原生业务请求，后续 Hello 也只会 retain；现有设计把独立业务对账入口留到后批，导致恢复机制不能自行继续修复自己的 Run。

仅支持命名执行的普通非 manager Run；原 Task/action/spec/generation/host/lease/pin 不变，业务仍 managed/wanted，且当前与冻结原生业务请求为空或明确已结算。独立继续不会关闭 Issue、完成 Task、创建 Run、换 provider、清除旧 unknown、删除维护保护或改 runner journal。manager、暂停/取消、未知请求与身份变化保持拒绝。

## 合同与实现决策

1. 增加只读 `GET /api/v1/runs/{id}/maintenance-reconciliation`，CLI `run maintenance-reconciliation <id>`，主会话与 Issue owner 提供同一受范围约束的读工具。返回原 Task/action/generation/spec/host/role/lease/full execution pin、Task 控制 revision、完整保护集合及其摘要、原请求的已知结算状态、当前 blocker 与既有继续决议；不返回凭据、payload 正文或伪造零值。
2. 增加 typed `runs.continue_retained`，CLI `run continue-retained <id> --revision N --data/--file`，主会话及负责该 Task 的 Issue owner 可经既有身份/来源/作用域边界调用。输入冻结 Task/action/generation/spec/host/lease/full pin、完整 protection-set SHA256 与精确保护身份集合，附独立 reason。request_id 去重和 Task 控制 CAS 在同一 app 事务中校验。
3. 只允许全部原保护已 linked、当前 archive 未 prepared、host normal 且原执行能力/完整 pin 仍支持的情况。查询并核实冻结原请求与当前相关 native/hosted 请求；任何 pending/unknown 或未应用业务结果阻止。空集合必须由账本真实查询得到，不能由调用者声明。不能把 maintenance linkage 当作业务完成。
4. 追加迁移保存独立 continuation resolution，引用精确原 Run/lease、Task revision、完整 pin 与保护集合摘要；旧保护、原请求、物理停止事实永久保留。该动作递增 Task 控制 revision，返回独立决议。相同 request_id 原样重放；新 capture 或 Task/action/pin/lease/control 变化使决议失效，不复用旧授权。
5. Hello 仍严格核原 runner 身份和原 lease。匹配有效独立决议后，复用现有 Hello 将 expired/lapsed 但未 released 的同一 lease 设为 active 并续租，再返回 Continue；决议提交本身不续租、不伪造 running。缺失 journal 不 Reassigned；只允许原 journal 提供合法 interrupted/starting/running/waiting 观测后继续。heartbeat 不能独立消费此授权绕过 Hello。
6. 原物理 release/stop 回执仍按历史保护规则存事实，不因独立继续把旧请求改成业务失败或成功。新的合法完成可按既有合同提交。实际旧 runner 的普通 Continue 会把 saved Thread 交给 harness，但 Orca 对坏 JSON 或失踪 terminal 会 fallback 新 launch，故不能把该路径当作严格续接证明。本批须增加显式协商的 strict-retained-resume 能力与专用 verdict，只有新能力 runner 可接受；旧 runner 一律仍 retain，不给忽略字段的机会。新模式在持久记录严格恢复身份后才由原 lease Hello 继续；普通旧协议的默认行为不变，当前/上一代各测协商与拒绝。首批只实现当前缺陷真实使用的 Orca，app-server 明确 unsupported，后续按独立合同支持。

## 验证与交付

先建立有意义 RED：新只读能力在当前 server 为 404，以及 protected 普通 Run 无继续入口；随后覆盖真实 App/DB 的 retain→独立继续→同 lease Hello Continue，含 expired/lapsed lease。明确保持原请求/unknown/pin/保护原文，重复请求幂等且修改输入冲突。负例覆盖 manager、paused/cancelled、stale CAS、lease/pin/spec/generation/action/host 漂移、不同 protection set、未 linked、prepared/maintenance、pending native/业务结果、新 capture、缺 journal、伪造输入与越 Issue 作用域。

CLI 覆盖 typed 参数、帮助、输出、保存原 envelope、单次 POST 与 unknown 后原 ID 对账，登记能力目录/入口测试；工具读写与 API 使用相同 app 校验。同步设计决策与生成 schema/类型入口；协议 wire 不变，新 owner 能力不改变旧 runner 请求默认语义。

定向检查仅使用已授权 2 CPU、package parallel 1、低优先级，不启动另一套 full/model 或生产巡检。局部验证后可先本地受控候选验证；正式 PR 与本地完整验证/独立 review/required CI 并行，最终门全通过才合入。任何生产继续动作另按精确读回结果交付，不在实现阶段自动执行。

## 严格原生恢复补正

旧 `RootObservationRequired` 不禁止 terminal 缺失时的 reset/launch；直接 server 解保护不满足本需求。严格模式必须验证原保存 Thread 能解码、原 worktree/terminal/pane/session/runtime/root launch/turn 一致；坏/空 Thread、terminal 缺失、身份改变和 resume 拒绝均保持待对账，零 launch、零 send，不能报业务失败清理原运行。缺 journal、held journal、旧 runner、不同 pin 同样保持留存。严格选择写入 journal 且重启保留，不能在下一次普通 Hello 退回 fallback。

严格 reattach 仅观察原 turn；没有明确独立继续许可时禁止任何隐式 Continuation、startup/recovery retry、原 prompt 重放或新 session。若实际原 turn 已取消，需要另一个明确授权的一次同 session continuation，使用新的去重 send ID、先对账已有 pending send，保同 Run/lease/pin；不能声称仅观察就恢复了执行。本轮在有界元数据确认前不同时扩两种模式，不把未知原生状态臆断成 cancelled/running。协议评估按 capability opt-in 扩展，N/N−1 旧请求默认语义与旧响应均不变；只对显式支持的 peer 返回新 verdict。

工作区同样采用严格附着：普通 Prepare 会重跑 profile，缺 state 时可重建 clone/worktree，
不能先调用它再核 Thread。runner 在任何 Prepare 前核本地严格权限与 held/thread/worker
支持；严格路径仅用显式 Reattach 读取原 state、resource/backend/provider/mount 身份和
已有目录，不运行 profile、Git/Orca 创建或 rename，不写状态。缺失或不一致保持 pending，
不支持该路径的 workspace 实现同样拒绝；普通任务的 Prepare 语义保持不变。

附着许可不等于旧业务结算许可。首次 strict verdict 仅能接回 started journal；已经进入
worker_done/submitting/releasing 的 journal，只有此前已保存同一 strict request 且真实
WorkerDoneAt/WorkerResult 均存在时，才能继续该 strict worker 自己的结果收尾。原始
give-up/release 的 accepted/pending_reconciliation 不是成功业务终态，不得清除请求、
事件或工作区所有权。strict 的 Start/setup、Wait 与本地事件保存错误均保持待对账，
不能构造 worker_start_failed 后走普通 finalizing 关闭原 terminal；明确 Stop/paused/held
仍遵循独立的既有控制授权。普通旧协议路径不因新增 strict 模式改变默认语义。
