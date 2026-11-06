# 有界维护与外部接管

状态：独立设计增量审阅通过，正在实施 D180；交付前仍需完整验证、独立代码审阅与 CI。本计划覆盖 server 与 runner，不把一端的实现或验证声明为另一端已生效。

## 目标与边界

每个升级、维护和恢复阶段都有持久化的截止时间。等待超时必须结束本轮等待，给出真实结果和可执行的外部接管入口；修复版本不能因为旧组件永远排不空而永远无法部署。

正常路径保留当前严格准入。新增独立的显式 force 模式，接受本次 predecessor 的未完成和未知工作；允许越过逻辑排空、未响应 ACK 或不完整诊断，真实记录 forced、timed_out 和 unknown，不把它们改成 drained、healthy、completed 或已交付。

force 的机械底线是精确 source/process/ledger-or-journal/release 绑定、原请求去重，以及同一数据目录只有一个服务写入者。未确认旧 server 或 runner 已退出，绝不启动第二个 writer。runner 已退出不表示原 native worker 已退出；其业务未知、资源占用和待确认请求继续保留。

一次旧 drained 加若干次历史摘要未变化不足以证明之后无活动。prepared 前通过检查的调用可以晚于最后 capture 才 claim；空动态工具、只读 sandbox 和清理返回也不证明旧原生 effects 不存在。摘要只是触发案例，完整摘要分类与完美闭包证明不再作为强制部署的前置门。

## 持久阶段与预算

为 server、每个 runner target 使用相同预算结构，默认每个 component/host 总计六分钟：

| 阶段 | 默认上限 | 超时后必须提供的真实状态和动作 |
| --- | --- | --- |
| updater handoff/adoption | 60 秒 | 对账原安装和进程；外部接管卡住的 updater，不能盲发第二 start |
| admission fence 与 host ACK | 30 秒 | 保存已提交 gate/CAS 和缺失 ACK；显式 force 可接受未响应者 |
| observation/drain | 60 秒 | 正常路径超时结束；显式 force 或预授权 force-on-timeout 可推进 |
| 原服务优雅停止 | 30 秒 | 转入已授权的精确强制终止或等待外部 force |
| 精确强制终止及退出确认 | 10 秒 | 保留 stop unknown；未证退出不得启动第二 writer |
| backup/staging | 30 秒 | partial 不算完成；只对账原产物或显式继续，不重复覆盖原备份 |
| successor start 与 health | 90 秒 | 原 start unknown 先对账；外部可停止失败候选并独立前滚 |
| commit/link 与恢复 ACK | 30 秒 | 保留实际提交点和缺失回执；外部对账、前滚或显式保留维护 |

默认分项为 340 秒，总预算独立为 360 秒，转场只使用剩余预算。本次对独立 server 和每个 runner 窗口实施此合同。旧批量 coordinator 的 targets 已固定，但阶段/host 转移仍重设计时；batch 总截止时间为明确的后续工作，本次不声称已约束旧 batch，也不让旧 batch 的等待阻塞独立本机接管。

首次进入阶段时，在副作用之前原子保存 phase、phase_entered_at、deadline、overall deadline、原 inflight request/launch 身份及 generation。状态读取、心跳、新日志、进程重开和同请求重放都不续期。重复进入同阶段仍沿用原首次时间。

同 boot 使用持久 boot ID 与单调时钟预算；重启后不能证明剩余时间时按过期处理。墙钟回拨不得获得新预算。延长、重开资源窗口或改变 target 必须是外部独立授权、fresh CAS 和审计，不覆盖原超时记录。

仅包装 context.WithTimeout 不足以保证有界：mutex、文件复制/fsync、systemd 和子进程可能不返回。进程内等待使用有界调用；不能可靠打断的阻塞必须由独立 installer 从进程外接管。超时后的状态须在进程内控制失灵时仍可读取。

## 显式授权与外部接管

新 force 请求固定 owner/operator、维护或升级 ID、原计划与 release hash、当前 generation/phase、精确 source identity、允许动作、原因、期限和未知工作承接范围。worker、runner 心跳和模型不能自行授予强制升级权限。

`force-on-drain-timeout` 必须在截止前随计划或独立授权持久化，限定 component、target、候选、阶段与有效期。超时后执行已保存的权限，不由 agent 临时宣称得到授权。未预授权时，超时输出下一条完整外部命令及原状态，外部可提交新的显式 force。

接管不能只依赖可能卡住的 control socket 或同一把进程内 mutex：

1. 独立 installer 读取持久 state/journal、实际 kernel 身份和固定产物，验证自己独立的安装锁与本机 authority。
2. control 调用在预算内不能返回时，接管者按已授权的 PID/start/hash/launch 精确停止卡住的 updater，保留受管 child、数据、journal、原请求和历史。进程不匹配或退出不明时只记 unknown。
3. 原 updater 确认退出并取得真实独占锁后，才加载原状态接管；不能把安装器锁、SQLite 单连接或看到空 slot 当作服务/journal 单 writer 证明。
4. 依据已保存 phase/inflight 对账原 stop/start。lost reply 不产生第二个 native 动作。新动作使用独立授权 ID；重复 force request 只读回原结果。
5. 接管命令本身逐步有界；部分成功、超时和无法确认退出都有终态输出。需要新窗口时保留旧记录，用 fresh CAS 授权继续，而非把旧计时器重置。

外部 force 可以结束卡住的失败候选、选择新候选前滚或终止本窗口；不能虚构健康与 link。不可逆提交以后保留新数据，只前滚，不自动恢复旧数据库、旧 journal 或旧二进制。恢复 ACK 超时可保留实际维护状态并完成“安装但恢复未确认”的结果，不能假称所有 host 已恢复。

## server 路径与旧环境自举

新 installer/updater 可沿已有的保留 child 第一跳安装到旧环境，不要求旧 server 增加 API。旧逻辑仍可拒绝普通排空；显式 force 模式独立接受 drain=false、未响应 ACK、摘要和其它已明确承接的未知，不改写原 archive 观察。

停止前的 Run、receipt、summary、host runtime captures 用于尽力审计和定位。无法查清全部旧工作必须显式记录，但不能重新成为 force 的无限前置门。原 server 的 artifact/process、实际 data file、目标 release 和本机 authority 仍须绑定，错误目标不能通过“接受未知”豁免。

从旧 server 退出到新 server 启动的事实是单 writer 的物理边界。原 server 未确认退出时，外部可继续精确 stop 或结束本次操作；不得启动新 writer。确认退出后才做有界备份与 successor launch；部分 backup、开始迁移及 launch unknown 都保留各自事实，不冒充完成。

successor 进入任何业务/native admission 前，验证固定 updater 的显式 force 授权、原 source 退出和自身身份。它从自己的完整数据库取得权威恢复库存，保护所有 open/unknown Run、原请求和迟到 facts，包含 pre-stop capture 未见的晚到记录；不把诊断清单当最终全集。

优先复用现有 maintenance registration、retained-run protection、startup fence、request records 与审计事件。登记和保护在一个事务内完成；持久化不明时沿原身份对账。新 server 在维护模式下应提供有界诊断和恢复入口；恢复库存、健康或 link 失败必须可由外部接管/前滚，不能变成另一个永久等待环。

摘要未知处理只增加防止重复执行所必需的保护。实施前先证明现有 frozen identity、main_turn ownership、OnToolCall 与 callback admission 已拒绝哪些 orphan；不用“所有 effects 都无害”作为假设。对缺少 fork 身份的旧 started 摘要，保留未知且不自动重新 claim/resume/publish。优先在现有持久维护记录/事件保存所需保护；不要求一套完整 namespace 恢复系统才允许部署。旧结果晚到可作为事实保存，不能充当新授权或覆盖原未知。

## runner 路径与授权

现有 standard upgrade grant 要求 open runs、journal pending operations 和 blocker 清空，不能把它解释为保留活 Run 的强制授权。新 force mode 必须有独立 owner/CAS 授权与能力协商；runner 不能因本地超时自行扩大旧 grant。

独立 runner plan 固定该 target 的 host/configured selector、实际 runner instance、旧 artifact/process、journal device/inode/incarnation、候选和管理能力。offline、manual 或没有既有 updater ownership 的 runner 不能被默认为已受管：必须有明确的外部 adoption/force 源绑定，或明确报告该 target 尚未执行；不能漏掉它后声称 batch 完成。

每个 runner 同样执行有界 fence、ACK、drain、stop/kill、backup、start/health 和 commit/restore。显式 force 可接受未排空和未响应 ACK；精确旧 runner 未退出或 journal writer ownership 不明时，不能启动新 runner。现有 journal.Open 的单连接并不是跨进程独占锁，需通过真实进程退出、受管单实例和必要的 journal ownership 边界证明单 writer。

原 native worker 可能在 runner 退出后仍运行。新 runner 必须保留原 run/lease/native identity、slot、workspace、pending request 和 journal 中的 held/fenced/unknown；不把替换 runner 当成 worker exit，不伪释放资源，不清 unknown，不新开同一执行。启动先对账原请求和原进程，readiness 分别显示“进程已运行”“恢复待对账”“新业务准入”，不能伪造旧 Ready/grant。

server 不可用时不能靠 runner 自授 grant。外部本机 force 只接受已固定的 owner/operator authority、精确目标和资源窗口；与 server grant 的权限边界要有独立拒绝测试。原普通升级和 legacy runner 交互保持原行为，明确报告哪些新 runner 能力已实际部署。

## 最小实现、版本与 CLI

共享 budget/phase transition 和 force audit 类型；server、runner 分别接入自己的真实生命周期，避免复制两套不同超时规则。先把可能永久阻塞的真实调用接入 deadline 和外部 takeover，再补 status/CLI，不用日志文案代替执行语义。

旧 updater 宽松解码 format 3 并写回，不能靠新增 optional 字段保存新 force 权限。新模式使用旧程序在写前拒绝的 format 4；新程序保留读取旧 1/2/3。原 state snapshot 的 normalization 只允许既有 generation/epoch/boot 变化，原身份验证后才显式进入 4；取消、重启、前滚、head/journal、same-byte adoption 与旧程序拒绝均须覆盖。旧历史和已取消计划不被重写。

优先复用现有业务持久化而不追加 schema。若必要保护确实需要新表，只能追加迁移，并用实际支持的旧 schema fixture 完整升级验证后填写 release min/max；不能机械把 manifest min 提高而排除正在运行的源版本。updater format、server schema、runner journal 和 wire 协议是不同边界。

server/runner 的旧有效通信、默认 upgrade grant 和 readiness 不被 force 模式静默收紧。新 force authority 必须显式协商；若实施需要不兼容通信，按仓库 N/N−1 规则升级并保留适配/拒绝测试。新字段不得让旧 decoder 静默抹去权限或持久保护。

CLI 同一变更覆盖全部能力：固定 targets/预算的 plan、apply、显式 force、预授权 force-on-timeout、fresh CAS extend、cancel、status、原请求对账和外部 installer takeover。status 输出首次 phase 时间、deadline/剩余预算、真实 inflight 身份、已知/未知事实、force 授权来源、已安装与已验证的差别，以及当前可执行的下一步。命令本身有 deadline，不能全部依赖卡住的 socket。

生成 Schema/类型走现有 generator。只读诊断可增强 captures 的完整性和组件定位，但不能以需要一个新 inspection helper、全量 effect 分类或完美摘要证明为由阻止受权 force。

## 必要验证

先建立真实组件 RED，再实现 GREEN；每个反例验证可观察行为，而不是镜像字段赋值。

- 组件与每 host deadline；旧 batch 总预算未在本次实施；轮询、心跳、重开与新日志不续期，墙钟回拨/boot 改变不延时，fresh CAS extend 留原记录。
- updater mutex 不返回、控制 socket 挂起、systemd 子进程不退出、copy/fsync 卡住和 partial backup。独立 installer 能有界接管原 updater，受管 child 和数据保留；未拿锁或未证退出不产生第二 writer。
- 原 stop/start 回复丢失与进程身份变化。仅沿原身份对账，不重复 native；preauthorized drain force 到期推进，未授权拒绝自抬权。
- server 的旧 prepared/readers 反复 attachment 及晚到 claim。普通 guard 继续拒绝，显式 force 保留真实 drain=false/unknown 并有界停止原 writer；successor 不重新执行原请求。
- successor start/health 超时与迁移已经开始的失败。精确结束失败候选后可新授权前滚，不回旧库、不重置原预算；commit/link/restore 超时真实可见。
- runner 正常严格 grant 与独立 force authority；active native worker、held/fenced run、pending journal operation、lost completion 经过替换仍保留，旧 runner 退出不伪造 worker 退出或 slot 释放。
- manual runner、offline target、管理能力缺失、错误 host/configured selector/journal/launch、journal ownership 冲突；targets 不被静默删减，未执行不计成功。
- 新 updater format 3→4、重开/取消/失败前滚、head normalization、旧 binary 在写前拒绝 4，旧普通路径保持；需要的 schema/journal/wire 兼容各自独立验证。
- 真实 CLI plan/force/status/reconcile/extend、dry-run 和恢复语义，帮助/能力目录/生成检查齐全。完整 make check、规定独立 review 和 required CI 覆盖最终候选。

## 分工与交付

共享预算和 updater takeover、server app/恢复保护、runner 生命周期/authority、CLI/协议和端到端验证可分工实施，但共享一份确定的合同和最终候选。每个实现只声明自身完成的范围。

旧环境紧急恢复优先使用已验证的受控/offline 入口；若只缺一个小的显式 force 能力，先做最小可审阅、可验证的补丁，不等待本计划所有增强完成。外部 break-glass 的 kill/临时残留清理必须保留原数据、journal、未知和进程身份，实际操作者独占现场；不能把清理当作原业务已完成。

后续正常维护增加稳定 reader admission fence 和分组件诊断，以减少需要 force 的次数。这个改进不阻止本轮外部强制恢复，也不改变 force 必须如实承接未知和单 writer 的核心边界。

配置中的 NodeSelector 只声明主机准入选择器，不冒充实际 tailnet 节点证明。本机 installer 通过固定 instance、实际 PID/start/hash/launch 与 journal 的 device/inode/incarnation 核验物理目标。

## 第二轮独立审阅的恢复边界修正

完整检查通过不替代独立故障探针。当前审阅原件与反例保持不变；在审阅源证据封存之前只读准备，封存后追加本段再修改实现。各项先保存可编译的真实 RED，再验证 GREEN；不自动重跑完整检查，不在本轮自行提交或发布。

- 取消是原窗口终态。server 与 runner 对同一已取消 source/maintenance plan 的新 apply、force、extend、takeover 明确拒绝并给出创建独立新 plan 的恢复入口，不能返回成功却仍停留 cancelled，也不复活旧窗口。原 cancel request 的幂等读取保持。
- fresh extend 只更新授权预算，不撤销同一 source plan、source process、target 和恢复 lineage 中已经明确授予的 force 权限。普通 apply 加 extend 不能取得强杀权限，不相关历史 replacement 的 force 也不能传入。原 stop/kill 已发出但回执未知时仍只核对原身份，不能因 fresh window 重发原副作用。
- 第二次 runner replacement 在覆盖活动 Bounded 之前，完整归档旧窗口、所有授权、审计、outcome 与 SourceExit，保留旧 grant 和 operation。新 replacement 只使用自身 lineage；旧 request 经实际 status/reconcile 和进程重开仍可回读原完整事实。
- runner 最终提交重放继续要求原 StoppedConfirmed；只发送已有原 Envelope，不制造新的业务动作。原成功终态 ACK 与原 assignment 的事实需持久保存。只有匹配的 host Stop、原成功 ACK、原停止证明和精确冻结 run/lease 共同满足时，才允许收口这一原 Run；accepted、失败、缺少停止证明或身份不符均保留原 request/run、quarantine 和资源未知。
- 托管 manager 的原 releasing 事实也必须有窄收口：原 managerStopped Envelope 已获得真正成功 ACK，保留 server 已验证的停止合同，再加原物理停止证明和新的匹配 host Stop，才结算该原 Run。unexpected-exit 路径不额外要求不存在的 completed close；若使用 closeReply 作为终态证明，则必须匹配原 completed close request 与冻结 LastSequence。不据普通 turn reply、错误 cursor、unknown 或本机清理推断完成，不创建 native 请求、workspace 操作或替代 manager 身份。该路径与普通 artifact/verdict 终态分别验证。
- App 将 bounded runner 的不可变保护库存与既有 server maintenance 保护一起纳入 owner-control 的 saved give-up 收口。只消费已有 run-authenticated 原 fact；独立 owner cancel/pause/terminal control 必须匹配原 run/task/action/host/lease。裸 owner control、force grant 或 admission release 不会替其他未知 Run 伪造结束，Reason 文本不能作为 native 物理停止证明。
- committed runner 的物理退出继续由既有 monitor 监督同版本重启：先确认原 exact PID 退出且原 launch/hash 无其他 live writer，归档原 operation，再持久记录独立 restart attempt 和新的 launch。只启动已 committed 的不可变当前 bytes；原 Binding、force lineage、window、SourceExit 与 journal quarantine 保留，不重发未知原生工作。新进程不继承 force startup hint，启动仍读取持久 quarantine。App 和 runner 复用既有 UpdaterReceipt 的 immutable Binding 与已确认 CurrentProcessInstanceID/CurrentSHA256 后继证明，核当前实际 self、stable recovery ID、journal/capability 和递增 generation；fresh owner release 仍绑定原 target 并只解除全局 admission。不能复用旧 InstanceID 冒充新 generation，也不改普通 checkRuntime 规则。丢失 restart Start 回执仍只 Find 原 attempt；控制 socket、进程身份或 receipt 不符保留 unknown，外部接管沿既有独立授权入口。

文件所有权：updater 的 cancelled/extend/history、supervisor monitor 与其组件测试由核心实现者独占；runner 终态 ACK、停止证明和逐 Run 收口只由 runner 修复者修改 internal/runner；App give-up/owner-control 仅由 App 修复者修改 internal/app/bounded_runner_maintenance.go、maintenance_run_protection.go 的对应 settler 和独立测试。计划由核心实现者单写；共享 protocol、schema 不在该最小修正中新增字段或版本。

## 第三轮独立审阅的取消监督与 release 修正

- 取消 bounded 维护不取消原有进程监督。仅原 fixed updater 已拥有且 Current、Process 与冻结 source 完整相同的 runner，取消后仍可按既有同版本监督在 exact source 退出、原 launch/hash 无其他 live writer 时记录独立 restart attempt。授权时持久捕获原 Current/Process 与 source 相同且原 operation 为 nil/terminal 的既有监督事实；旧记录缺少该事实不推导 ownership，未提交 candidate 不因取消转成受管版本。归档原 cancelled operation/window，新的 matching source-version restart 不被已取消 candidate target 抢占，原 cancel request 在归档和重开后仍按完整 payload/action/grant 幂等返回；丢失 Start 仍只核对原 attempt，错误 bytes、source 身份及 live sibling 拒绝启动。
- manual source 的空 Process 不等于旧进程退出。取消不新增永久 adoption 权限；即使 source 后来退出，也不从取消推导自动 Start。状态明确说明未受管并要求独立新 source plan。已有监督与 manual 源分别使用真实组件正负反例验证，重开不改变该边界。
- 独立 admission release 只有在固定 updater 的 committed receipt 已绑定原 target 与当前实际 runtime process/hash 时才可接受，首次 target 也不能仅凭同 instance、journal 版本及 capability 在 checking 阶段提前 release。安装期 health 的 first-target 原条件保留，不通过放宽 updater commit fence 消除反例。
- 同一 recovery lineage 已成功 release 后，不同新 request 明确拒绝并引导对账原 release。按不可变 host release event 查重，不能仅依赖可能被普通 control 或新维护替换的当前 control pointer。原 request 去重重放保持，新独立 lineage 在自身 commit 后仍可 release；runner journal 的完整 release JSON 身份校验不放宽。

本轮 updater 核心与设计/计划由核心实现者独占；App release 与其 committed-receipt helper、独立测试由 App 修复者独占。完整检查、正式 review 与 CI 的旧通过仅绑定旧候选；完成必要 focused 验证后再次统一冻结，不在修复过程中自行 full、提交或发布。


## 第四轮独立审阅的三个局部修正

- 缺少 archive observation 的 forced parent 仍可保留原有效 force 历史，但普通 Prepare/Authorize 必须明确返回需要独立 bounded 授权的错误，不能解引用 nil、补零观测或借旧 force 授权新候选。通过实际 BoundedCommand 和 live supervisor 入口证明 fresh force 可推进新的 target，原 nil observation 与 parent 历史保持不变。
- 已记录的 active server force 请求先按同一 plan、完整 authorization payload 和 action 精确去重，再检查新请求的过期时间。原请求过期重放只返回已记录结果，不保存、不续 deadline、不执行 native 动作；新过期请求和修改原 payload/action 仍拒绝。历史 maintenance 继续使用原 status/reconcile，不新增跨计划授权。
- runner 初始 Hello 可以解除 global admission fence。receipt reader 与 usage 循环以本次 Run 内的实际启动标记协调，在 Hello 前后共用启动点，保证解除 fence 后恰好启动一次；不改变上传请求、cursor、journal writer 或旧未知事实。

核心实现者独占 updater 两处局部修正、其回归和设计/计划；runner 实现者独占 runner.go 的循环启动局部及独立测试。原完整审阅与完整检查证据保留，完成原 probe 和必要负例后冻结 source，新的 full 与 CI 仍须覆盖最终候选。


## 已安装服务未建立 archive fence 的收尾

PreparationRevision 表示预期 prepare 成功后的 revision，原 prepare CAS 是该值减一；操作计划应使用 fresh archive revision 加一。问题：显式 force 可在原 archive prepare 的 CAS 已明确落后时完成服务安装和 ledger link，但严格 finish 仍重放原失败 CAS，导致没有实际 fence 可恢复的维护无法收尾。原计划、失败请求和观测均不可修改，健康服务不应再次 Stop/Start。

- 只在既有 same-plan ledger_linked、fresh CAS 和有效 complete 窗口内，检查原 force audit 及对应完整 saved authorization 的 source、target、plan digest、真实 predecessor exit 和当前 committed proof/receipt/process/version。
- 从不可变原 force audit 取得完整 prepared=false 基线；fresh Preparation 全字段相同，且原 prepare CAS 严格小于该基线 revision，使延迟原请求不能日后建立 fence。所有原 host PreviousMode 必须是 maintenance；fresh host 集合/control 与原 audit、原计划精确一致，动态 runtime ACK 不作为 control 身份。
- 满足时只追加说明未建立 fence、无需恢复外部状态的审计，并完成已安装服务的维护；不调用 archive/host 写入、不 Stop/Start、不改已 adopt 计划、原 409/timeout/unknown 或任何业务事实。普通 finish 和所有身份、prepared、revision、host 漂移仍使用原严格行为。
- 在实际 off-by-one prepare-CAS 失败后 force/install/link/finish 的组件链上先留真实 RED，再验证 GREEN；覆盖持久重开/重复 finish、原失败与 audit 不变、零外部写/进程动作，以及上述 proof/baseline/control 反例。实现仅 updater 与设计/计划，不改 CLI、wire、schema 或状态结构。


## 仅替换 updater 的安装后恢复

- 复用独立 bounded takeover 与显式 UpdaterTarget pin，在服务已 commit/link 的前提下只替换 fixed updater；原 maintenance plan、committed handoff journal、服务进程、业务状态和维护 deadline 不变，不重新授权服务 Stop/Start。
- 原 authorizing source 仍保留在原计划；另从当前 Current/CommittedProof、linked receipt、SourceExit 固定实际 retained server。每个破坏与启动边界验证当前 server 的真实 kernel identity、原 updater identity、固定 manager/unit/config 和同一 lock inode。
- 独立 versioned continuation 与 hash-bound head 保存新的 updater candidate、原 handoff journal hash、当前服务 proof 与原 installer 有界窗口。新 updater 启动只能通过该独立证明认可不同 candidate SHA；原 journal/hash 不覆盖。启动回执丢失只核对原 attempt，不重复 native start；后续同 bytes supervisor 启动继续校验持久来源。
- 回归覆盖真正不同 updater bytes、原始 handoff journal 与活服务：旧 updater 退出后新 updater 获得同 lock，服务 PID/start/hash 保持；候选、kernel identity、lock、CAS 或 proof 不符拒绝。旧回执/unknown 不改写，不将本机 updater 更新宣称为服务再次部署。
