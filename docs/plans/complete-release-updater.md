# 完整发布与普通升级的 updater 交接

状态：R1 已合入；R2 主体与聚焦回归已完成，独立 review、真实隔离交接及完整验收进行中。Refs: D198、D200。

## R1 完整发布

每个通过完整 CI 的 main 提交发布 Linux amd64、arm64 完整包，包含 server、
runner、updater、CLI 与同源 Web。增加发布集清单，绑定源码与全部组件摘要；
原 service manifest 保持格式 1。暂存、分发和验证不得遗漏 updater 与 CLI。
发布使用 main 加完整 commit 的不可变 prerelease；重跑验证原资产，禁止覆盖。
同时组合两个平台为 fleet 包，兼容既有混合架构主机选择。先建立固定 Git tag，
使用支持 draft 的查询恢复首次/中断发布；全部资产核验完成才公开。

## R2 普通 updater 交接

先交接对应 updater，再切换受管服务；runner 沿用逐台维护和备用主机规则。
独立 installer 保存原请求、发布集、配置、进程及锁身份，确认旧 updater 退出后
取得同一锁并收养仍运行的服务。共享文件安装串行，完成证明逐实例产生。
不借用故障恢复授权，未知只对账原 attempt。补齐 CLI、能力协商、状态与恢复。
首次旧部署由新产物的独立 installer bootstrap；生产部署另行确认。

R2 发布集使用格式 2 明确要求 updater 先更新，格式 1 保持原分发语义。
新模式仅向协商自身交接能力的 peer 发送可选目标，首次 Hello 不发送未协商的
新观测字段。独立 installer 在受管 unit 外运行，原 service PID 与 writer 保持；
普通操作不能借用历史维护/force 记录。旧产物首次 bootstrap 仍需其精确退出
行为证据；现代 updater 用自身启动身份及保留 child 的明确能力证明。
sidecar journal 记录 stop/start 的意图与一次派发，恢复只观察已发出的外部操作。
共享固定二进制使用独立安装锁，各实例分别确认旧进程退出、新进程持锁与原服务
收养。原 updater state 格式、服务配置和业务数据库均不因监督器替换而改写。

## 验证与交付

覆盖两个架构完整性、源码身份、损坏与缺失、部分发布恢复及失败 CI 不发布；
覆盖服务 PID 保持、每阶段中断、锁冲突、PID 复用、共享文件与丢失回执。
保留历史清单/journal 与 N/N−1；聚焦验证、最终 make check、独立 review、
required CI 和最新 main rebase 后经 PR 合入。发布不等于生产部署。

### R1 首次 main 发布验证修正

首次 main 构建与聚合检查成功，但发布查询使用了 GraphQL Release 不支持的 targetCommitish。改用实际 schema 的 tagCommit.oid 核对已固定标签，并让发布恢复 fixture 校验查询字段；原源码身份、draft 与资产完整性规则保持。

### R2 收尾：取消与迟到交接回执

已准入的独立 installer 可能在业务批次请求取消后继续运行。取消前必须对账原监督器交接；无确切完成回执不能以服务尚未替换推导没有外部工作。暂停/整体到期后仍读取并保存原回执；runner 未决目标不因恢复旧服务接单而被清除，完成观察后才释放控制。覆盖未确认取消、迟到回执与超时后保留目标。

未准入的 server 交接可直接取消；coordinator 必须先在 app 事务中按批次 revision 与阶段/整体期限取得监督器派发准入，再调用 installer。准入与取消通过同一 ledger 顺序互斥，不把“尚未换服务”当作“未派发”。

仅更新 updater 的主机恢复 normal 后，保留的观测目标不能被误判为未恢复业务；逐台调度等待该主机确认最新 revision。暂停后的 retry 保留已发出的原监督器目标与 ready/恢复阶段，不重置成 queued 或生成新 attempt。

准备阶段明确拒绝且尚未派发 installer 时，独立持久化绑定原请求/发布集/实例的拒绝回执；这与派发结果未知分开。按原 request_id 的只读状态查询可恢复丢失的拒绝响应，server/runner 保存证明后允许取消。拒绝身份不可重新用于派发，后续业务重试使用新批次。

状态展示同步覆盖 Web 发布详情和 CLI 普通文本：区分等待证明、准备拒绝和已确认交接，显示最后观察的 updater/服务进程及原请求/发布集。沿现有 live snapshot，不新增轮询；历史包不显示不存在的监督器要求。
