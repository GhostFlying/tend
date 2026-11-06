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
