# 维护控制的状态收敛与中断续行

状态：已实现，必要定向 regular/race/vet 已完成；统一检查、审阅和交付由后续门槛执行。依据：D225 的业务对象/CAS 幂等及 D155/D180 的独立维护责任。

## 问题与范围

通用请求档案退役后，App 直接对实际 archive preparation 与 host control 执行 CAS。
updater 的 prepare/cancel/finish 却仍假定重发原 request 会返回旧成功回执：恢复前先重放 prepare，
再重放每个 host maintenance，最后才恢复 host 和取消 preparation。原准备已成功时，第一步即为
过时 CAS；重复 prepare、部分准备后的取消、恢复中断及重复 finish 都共享这个缺口。
updater 单测的 authority fake 仍有旧 request map，掩盖了真实 App 行为。

本次只修该控制生命周期。原安装、健康样本、DB link、业务 Run/native unknown、lease 和
独立维护授权仍各自按原规则核验。恢复控制不证明原业务执行成功，不再安装或启动候选。
不恢复通用 request ledger、返回缓存或请求到业务对象映射，不新增表、证书或逐项审批。

## 真实状态与原计划

设原计划 PreparationRevision=P，host 原 Revision=R。用现有 archive status 读取 SoT。

| 控制对象 | 初始状态 | 本窗口设置后的状态 | 已恢复状态 |
| --- | --- | --- | --- |
| Archive | prepared=false, revision=P-1 | prepared=true, revision=P | prepared=false, revision=P+1 |
| 原 normal host | normal, revision=R | maintenance, revision=R+1 | normal, revision=R+2 |
| 原 maintenance host | maintenance, revision=R | 不修改 | 仍 maintenance, revision=R |

原计划及 updater 当前维护对象绑定维护身份和转换边界；原 request ID 仅继续用于传输关联。
实际对象处于精确允许的后状态即表示该转换已满足，不声称它一定由某次传输请求执行。
额外 revision、错误 mode、同一 host 被 upgrade 接管或缺失原 host 都不能认作本窗口完成；
不覆盖这些变化，也不为接受它们擅自刷新原计划 CAS。新出现的无关 host 不在恢复写范围，
不因它存在而重新建立全局 run-zero 或主机空闲门。

ArchiveAuthorization 是已存在的观测，不是不可变逐步回执：bounded extend 当前也会更新它。
可用其与已有原计划/Audit核对来源，但不能凭它的空值或后续恢复快照新增不可通过的权限门；
不能把当前 restored 观测覆盖解释为原安装没有获得授权。重启恢复依赖实际控制状态及既有
维护对象，不引入平行进度账本。

## 准备

1. 保留现有 plan、phase、generation、expiry 和 failed-parent 检查。
2. 先读并分类原计划涉及的 archive/host 控制，再做缺少的转换。
3. Archive 在初始状态才执行原 prepare CAS；已为 true/P 时跳过，不重放旧 CAS。
4. 原 normal host 在 normal/R 时才执行原 maintenance CAS；已为 maintenance/R+1 时跳过。
   原 maintenance host 不写。已恢复状态不能作为新的 prepare 续行，避免重开已取消窗口。
5. 每个外部调用仍受原 context/期限约束。错误或未知停止后续写；之后从 SoT 判断是否已提交，
   不用 transport retry推断成功，不补发已满足的转换。部分准备是可取消的实际状态。

## 取消与完成共用恢复

用一个局部的状态分类/恢复 helper，供 CancelCommittedMaintenance 和 FinishCommittedMaintenance
使用，而不是分别新增流程。

1. 只读识别原计划边界；恢复从不先创建 preparation 或 host maintenance。
2. 原 normal host 若仍 normal/R，说明未设置，无需恢复；若为 maintenance/R+1，才以原 restore
   输入 CAS 到 normal/R+2；已经 normal/R+2 则跳过。原 maintenance host 保持原值。
3. Archive 若为 true/P，执行原 cancel CAS；false/P+1 已取消则跳过。false/P-1 且所有 host
   都在初始状态时是未准备，无控制需要释放，不制造一次虚假的 prepare/cancel。
4. 支持部分 host 已恢复、响应丢失、进程重开，以及独立正式控制已先恢复 host/archive 的场景。
   对已 false/P+1 的 archive，不再 prepare；仍有处于精确 maintenance/R+1 的原 host 时仅收尾它。
5. 初始 archive false/P-1 与已经变动的 host 不是本顺序可产生的准备状态，明确冲突，不认领。
6. 恢复后再次只读确认具名目标均达到允许的释放状态，再保存原 cancel/finish 的真实结果。
   不用 HostedDrained、OnlineReady、open_runs=0 或历史业务全部成功作为释放 admission 的条件。

现有 bounded 未建立 fence 的专门完成规则保留，不用它绕过本路径的真实 foreign control drift。
完成窗口到期、generation 漂移、linked proof 缺失等现有拒绝仍保留。旧窗口历史不刷新；若需要
新的有限完成授权，仍走既有独立 bounded extend，状态收敛不自动发授权。

## 诊断与兼容

删除仍声称通用 request replay 返回旧 receipt 的误导说明，CLI/help 保留稳定业务身份和
explicit CAS 的含义。若改错误传播，只沿现有结构化 code 加具体阶段，不能泄露内部数据。
不为失败重建永久错误请求档案。

预计产品范围为 updater/committed_archive.go 的局部 helper与调用、相关测试及必要 CLI/help
说明；真实 App/archive handler 的 CAS 规则保持。现有请求字段、状态 format、schema、runner
wire 和控制路由均可保持不变。本提案改变 updater 如何推进已授权转换，不给旧 peer新增
必填字段或改变 archive API 的 stale-CAS 拒绝语义。

## 必要回归

- 用真实 App + ArchiveControl handler/HTTPAuthority 证明旧实现失败：prepare 成功后正式 finish
  不得再发送过时 prepare；不以 request-map fake 代替该链。
- 分别在 archive prepare、每个 host maintenance、每个 host restore、archive cancel 已提交后丢失
  响应；每个边界都重开 updater，再从真实 App 状态继续，已提交对象不再增长 revision。
- 部分 host 设置后失败，重启再 prepare 只做缺少的转换。
- 未准备取消零写；部分准备取消不先设置原本未触碰的 host；原 maintenance host 永不释放。
- restore 响应提交后丢失、部分恢复重启、独立正式 host restore/archive cancel 后 finish，均从
  实际状态收敛且不重复安装、停止、启动、创建业务动作或增长控制 revision。
- 普通/receipt-recovery/bounded 的 cancel与finish 接同一真实恢复逻辑；bounded timeout 历史、
  complete预算和重复完成行为保真。重复 finish 不重新 prepare，不更换原对象身份。
- 具名对象额外 revision、同 mode 但更晚 revision、错误 mode、upgrade 接管及 host缺失拒绝；
  正常无关 host/运行中的业务/真实 native unknown 不被当作本控制释放的额外前置门。
- 保原 failed-parent、source_operation、未建立 fence、启动/健康/DB-link 与旧请求/CAS负例。

先跑能复现真实链的必要定向测试，再跑受影响 race/vet；完整检查与发布由统一门槛执行。
本次实现未进行生产操作、模型调用、构建、完整检查、独立审阅、提交或发布。

## 受控交付顺序

实现验证通过并合入后，构建新的完整发布集。已经安装并 DB-linked 的原服务无需重做安装：
先沿正式 `tend updater self-update plan/apply/status`、完整 format 2 发布集保留当前服务，
交付修正后的 updater；不使用承载 ForwardRecovery 的 `updater handoff`。fresh 读取原维护对象与
恢复后的精确控制状态，必要时使用独立有限完成授权，然后由正式 finish 收敛原窗口。
其后才开始一次正常完整 fleet 升级。force、重放安装、手写账本均不能替代此产品行为验证，
原业务对账与本次维护安装的验收继续分开。

验证补充：真实 App 首次回归已复现旧实现 HTTP 409。受影响旧 bounded 负例随后暴露：
原审计已证明 fence 未建立时，后来的 true/P 或 false/P+1 不能倒推出原准备成功；实现据
该既有真实反例保留 unchanged-baseline 门，原正反例均保留。新增真实 App 测试在 race
下逐子例重复全量迁移触及原十分钟上限；夹具改用仓库已有 storefixture.Template，仅共享
已关闭的空 schema 原件字节，各子例仍独立打开数据库及原件目录，无延长任何期限。
