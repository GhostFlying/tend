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

独立 owner 取消之后，原 accepted 请求按既有合同仍不改写，不能要求它事后变为
succeeded。runner 仅在 authenticated Hello 对原 journal 返回 Stop 时，另外持久记录
原 Assignment/lease 与 Hello request 身份绑定的结算许可；不清 strict 附着约束，也不将
单独 Forbidden 当作结算证据。物理停止仍是前置条件。原凭据撤销后无法发送的请求和
事件在 Done 的同一事务转入保留 metadata 历史，再退出 pending 队列。重启保留该许可，
后续 Retain、Continue 或新的 strict authority 使旧许可失效；缺失原 journal 或身份不符继续拒绝。

strict 最终请求遇到非成功或凭据撤销会明确唤醒 Hello 对账；Stop 若到达仍在报告的 executor，
其退出必须再次唤醒，避免新许可被一次仍在退出的执行器消耗后无人收尾。

独立复审控制补正：严格附着成功后，显式 Pause 仍须中断已经验证归属的原 terminal；
不能与 fence/shutdown 的零自动输入混为一谈。仅在本次 beginRetained 成功后允许该
Pause 中断，未知/未附着仍不发送输入，Hold 与显式 Stop 保留既有控制语义。
原 pin 可用性同时接受已协商 continuation 能力与原 backend 下的 AgentContinuations
完整精确 pin；不要求它仍是新任务库存，不借此扩大任何新任务选择范围。真实
Hello/heartbeat 与 request/CAS/原pin保留回归覆盖库存变化后的继续和续租。

最终复审事件分支补正：strict 报告先上传事件，凭据撤销可能在该分支返回 Forbidden，
在尚未发送最终请求前就退出。因此该分支同样须明确唤醒 authenticated Hello 对账；
不把 Forbidden 本身当作终态、不丢弃事件或更改原请求。回归使用真实 App/RPC 的
成功回执丢失场景，先消费已有报告唤醒，再独立制造事件 Forbidden，要求仅凭此
新唤醒自动收到原身份 Stop 并保留原事件/请求历史后收尾。另以真实取消上下文
验证 strict Reattach 期间 Stop 仍通过下一轮持久请求读取识别取消并进入既有停止流程，
不依据单次 completionPending 臆断无限重试，不扩展状态机。

后继完整 stack 复审核实两处控制次序缺陷，本批仅修这些路径：

- strict continuation 对单个 journal 不准入时，将该原 Run 保持 fenced/retained，继续应用同次 Hello 的其他 Run verdict，并允许 host 完成启动；不伪造成功、不分配新 Run。真实 Runner.Run 多 Run 回归验证旧 releasing/give-up 无合法结果时不会退出整个 host。
- 独立 Stop 快照不能跨 settlement gate 授权 cleanup。Stop 的原身份、物理停止证明及当前许可须在该 Run 的同一 gate 内重新核验，再清理并在 journal 单事务中校验原 Stop、归档请求/事件和 Done。Continue/ClearStop 与新的 strict authority 写入使用同一既有 gate；已 Done 的 journal 不接受新 authority 覆盖历史。活动 executor 之外没有在运行的 cleanup；原 journal 事务的 Stop 条件仍独立保护最后落盘。

先用真实并发 barrier 复现 revocation 先完成后旧 cleanup 不得结算，以及 cleanup 已持 gate 时新 authority 不能抢先清 Stop/覆盖归档；保留原请求、事件、assignment/thread 与 Stop 历史。目标验证使用获准空闲 2 CPU/p1/race，不启动 full、模型或生产操作。修改规模与 review 范围按最终实际差异评估。

同一并发链中，Continue 在 reporting executor 尚未退出时撤销旧 Stop，不能只更新
当前 active 条目后丢失后继启动；该控制同样设置既有 reconcileOnExit，复用退出唤醒，
限定 reporting 场景，不扩展普通工作中的调度或控制状态机。

最终 strict 报告尝试补正：正式 RPC 将受保护原 release 的 durable accepted 回执映射为
unknown_outcome/HTTP 500，原 spec 后继替换后 lease 仍可鉴权且原 accepted 不改写。
如果 strict 报告沿 host lifetime 无限 transport retry，已经持久的 Hello Stop 无法重新读取，
退出唤醒也无从发生。先用真实 App/RPC 的 accepted→spec supersession→Hello Stop
复现，保留原 Run/lease/pin/请求回执；事件上传的相同 transport 等待链也做受控故障对照。
仅 strict 报告采用一次现有 request-timeout 内的上传尝试，然后保留原 envelope/请求身份、
原事件及 unknown，返回 pending 并唤醒 Hello；普通 legacy retry 语义不变，不新增全局
超时，不把 accepted/500 作为业务完成，不释放服务端旧 lease 或重放 native 输入。

strict 单次尝试之后仍须保留既有 RetryDelay，避免成功 Hello Continue 立即再建 reporting
executor 而跳过原退避；等待只在 strict pending 报告退出前发生，独立 Stop/runner 取消
可提前打断。用真实 wire 计数验证短于既有间隔的观察窗内不会重复 POST，并验证 Stop
仍能收尾；不限制无关 RPC。

本 PR 的新 journal 16 也暴露旧 CI 历史 fixture：它先建当前库，只删一个表后把版本号
退为 14，留下 16 新表使迁移重复 CREATE。仅修测试，直接运行前 14 条真实迁移并写入
原 usage/cursor，再用正式 Open 升级到当前版验证保留；已提交迁移内容保持不变。
