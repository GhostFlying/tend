# 声明式 Capability 当前验收矩阵

当前规范是 owner `msg_01M6NRJFA40Y2ABY7GKMCEHEPV` content revision 1；优先于旧 spec2、原三输入与旧 D176 native 描述。此文件在新代码之前写入，断言是待验证要求，不能当作已通过报告。原三输入保持原 file ID/size/SHA，原计划和失败历史继续可追溯。

| 原阶段 | 处置 | 当前出口 |
|---|---|---|
| P0 | 替代 | 冻结声明/required/pin/协商合同；不需要 native 接口收敛 |
| P1 | 适用 | 定义 CAS/修订、显式 Agent 配置、原请求登记与查询 |
| P2 | 撤销 | 权威工具实证、工具源/TTL/profile/session、prepare/adopt 不是产品前置；旧记录只读/对账保留 |
| P3 | 替代 | 双 role required、声明覆盖、普通 executor 可用性与身份/预算；claim/launch/reconnect 一致 |
| P4 | 替代 | 目录/Task 展示配置声明及调度状态，旧实证独立标历史，不冒称配置上线 |
| P5 | 替代 | image_gen 配置闭环与新候选验证；撤销真实生图、费用观察及 native 实证出口；实际调用零 |
| P6 | 适用但本轮不执行 | 独立版本化交付、唯一受控升级窗口；本轮无 merge/deploy |

| 原验收 | 处置 | 当前断言 |
|---|---|---|
| A01 | 适用 | 定义追加 revision、CAS 竞争/幂等、退休与历史可读 |
| A02 | 适用 | 多 category 与显式 capability 分离，同四元组多身份不合并 |
| A03 | 替代 | 配置 validate/reload/register/reconcile 原 request/CAS；登记是声明，不是 confirmed 工具 |
| A04 | 适用 | 保旧配置/hash/spec/pin/unknown；迁移只追加，旧 reader 拒绝不支持的存储 |
| A05 | 撤销 native 门 | 工具来源/schema/skills 不参与标签匹配，品牌不自动赋值；旧证据仍可读 |
| A06 | 替代 | 区分显式声明/无声明、普通 executor 可用/离线；声明不表示实际调用成功 |
| A07 | 适用身份部分 | 不跨 Agent/runtime/revision/profile/backend/provider；不拼多个 executor |
| A08 | 替代 | 普通 heartbeat/incarnation 围栏保持；工具 TTL/session 结束不影响标签匹配 |
| A09 | 撤销 | 不需要 prepare/adopt/consume/nativeguard，普通启动与原共享预算不变 |
| A10 | 适用 | 双 role required 独立冻结；重复/未知/退休校验；空 required 无新标签门 |
| A11 | 替代 | 只有显式声明覆盖的同入口可领取；定义/Agent revision/pin 和 binding 原子固定 |
| A12 | 替代 | 无声明/修订不匹配/旧 feature 为 unavailable，离线/容量为 pending；explain 与 claim 一致 |
| A13 | 替代 | 不自动 probe，原持久 Attention/有界恢复保持；unknown 仅原请求对账 |
| A14 | 替代 | launch/reconnect 读取同 pin 冻结声明，普通 Start；工具消失不作标签资格门，身份变化仍拒绝 |
| A15 | 适用 | 两 role 独立；普通 reviewer 不被隐式要求 image_gen，waived 仍按冻结 policy |
| A16 | 适用 | 普通共享 admission、unknown 占位保持；无新增验证/免费执行槽 |
| A17 | 替代 | 独立声明 feature 协商；旧 native-gated peer 不接新任务，空 required/历史 wire 不变 |
| A18 | 条件适用 | 本次无 breaking wire；保现有 2/1。若后续单入口升级属另一候选，不在此移植 |
| A19 | 适用 | named CLI/API/help/配置 reload/register 与原 request 恢复路径 |
| A20 | 适用 | 筛选/分页/水位、当前/历史 revision、空/未知区分 |
| A21 | 适用 | CAS/stale/幂等/原 request unknown；历史验证对账不重新调用 |
| A22 | 适用 | owner 配置意图与 runner 身份边界；不新增 worker/manager 权限 |
| A23 | 替代 | UI/目录展示“配置声明”和普通调度资格，不以旧工具实证或名称伪称上线 |
| A24 | 替代 | 配置示例逐项或批量为明确选中的官方登录 Codex Agent 声明 image_gen@revision；不检查登录凭据/品牌，不宣称已应用 |
| A25 | 撤销真实调用项 | 生图调用为零；原失败/unknown/费用未知保留，不造 native ready |
| A26 | 适用 | 新 exact head/base 的独立 formal review、仓库 review、最终 full 与 required CI；heavy HOLD 时 NOT_RUN |
| A27 | 本轮不执行 | 仅代码/候选，受控交付由父责任链另行执行 |
| A28 | 本轮不执行 | 不声称生产声明配置或部署已完成 |

旧 source review 的三个 finding 都要有回归证据：precheck 失败不得在未 Start 时清 stop proof；注入 now 的目录/claim 不再因工具时效分歧；解释不能覆盖原 AgentID/RuntimeID/backend 来制造候选资格。旧 review/full/CI 只覆盖旧对象。原 CI journal-v14 fixture 错建后续表须纠正 fixture，不改已提交迁移。P0 external cancellation/partial 与未知 OS exit 保持原事实，不推断争用根因。

本轮新增回归出口（上轮独立验收 changes_required 后，编码前记录）：

| Finding | 当前验收补充 |
|---|---|
| R8 / A02、A03、A04、A21 | 本机 reload/registry 与服务端 register 都拒绝同 environment/Agent/revision 添加、删除或替换 capability 引用；检查全部接受历史，不能通过删除再重加或退回旧 revision 绕过。正常提升 revision、原请求 replay、集合次序/nil 等价及历史同声明恢复仍有效；拒绝不推进库存或写新快照，旧快照/pin 不变。 |
| R1 / A04、A14、A17、A21 | 缺省 MatchPolicy 的旧运行可接收同原 pin 的有效新复验收据；过期/错 pin/旧 incarnation 不授权恢复；原 binding 不变。declaration_v1 不读该适配、不产生 native 门。 |
| R2 / A12、A20 | 声明满足时，host capacity/runtime capacity/普通 adapter 不可用为 pending，候选身份和原因可读；Claim 不领取，恢复可用后可领取。错误冻结 Agent/runtime/backend 不能因可恢复等待被放宽。 |
| R3 / A13、A19 | 当前指南和持久 Attention 只要求声明配置和普通 executor 可用性；旧 verify/StartWithCapabilities/source/session 材料仅标历史，不作当前资格要求。 |
| R4 / A12、A13、A20 | 配置存在而从未 Hello 的主机是未观测事实，不能中断全任务解释或等待通知。健康声明 executor 的 explanation/claim 保持一致；未观测主机有明确原因，不能获得执行资格；超过十分钟的等待仍只持久通知一次且不 probe。其他存储错误仍返回。 |
| R5 / A10、A12、A13、A15 | implementation required 为空时，首次分配受 acceptance required 阻塞也必须十分钟后产生一次 acceptance 通知，解释保留 acceptance required，implementation 不继承标签。双角色按各自 role 去重；waived、已分配而不再检查前置、健康 reviewer 不产生多余验收通知；重复及并发 sweep 不重复 Attention/事件，不创建工具证据或执行。 |
| R6 / A20 | 同 Agent/runtime/backend 的当前与在途保留 pin 均须跨页完整返回，游标以完整冻结 pin 区分 Agent/runtime/inventory revision、snapshot/hash、profile、worker/backend/purpose。limit=1/2 与不分页集合一致、无重复；当前无声明与原保留声明独立呈现。游标继续绑定筛选/事实水位，旧键格式或事实变动明确拒绝并要求重新分页。 |
| R7 / A20、A23 | Task 候选 React key 使用完整冻结 pin，仅无 pin 时使用独立 fallback；同入口当前 alpha@2 与保留 alpha@1 同时显示后，重新读取返回仅 beta 时不得遗留 alpha 的声明/资格行。真实 React DOM 覆盖移除、重排与重新加入，查询不变更 Task 身份且无重复 key 警告。 |

本轮 main 整合补充出口（不替代既有 A01–A28/R1–R7）：

| 范围 | 必须观察 |
|---|---|
| A04 迁移 | main server 90/journal 16 正向升级后原 request、assignment、停止证明、retained authority 字节不变；capability 表追加可读；旧候选 88/16–17 冲突明确拒绝，拒绝不改原版本或数据。已部署 main SQL 字节保持。 |
| A14 整合 | retained-resume 仍传入普通 Start，capability precheck 失败不清 stop proof、不启动；标签不探测 native 工具。 |
| A17/A18 | main 新入口/schema roots 和旧声明 feature 协商并存；N/N−1 默认不变，不借整合改协议代际。 |
| A19/A23 | 两侧 API/CLI 目录与生成类型完整、两侧 Web 回归入口保留；Capability 决策编号改 D176，main D174/D175 保留。 |
| A26 | 最终 exact integration head/tree/main/base、targeted 初始失败/修正/终态、CI 真实状态分开记录；full HOLD/NOT_RUN，新的独立 review/验收未执行，不引用旧通过。 |

Task required 更新 P2 有界补充（编码前；不替代原历史）：

| 范围 | 必须观察 |
|---|---|
| R9 / A10、A21 | 无新增 mount 时，implementation/acceptance 分别新增和替换为未知 definition、未知 revision、退休 definition 均拒绝；合法新增/替换成功。保存真实 RED/GREEN，拒绝无 task/spec/event/outbox 副作用，原请求 replay、payload conflict、stale CAS 保持。 |
| R9 / A04、A10、A15 | 同 role 未变历史引用在退休后允许合法其他更新（包括新增 mount），排序等价、删除可行；跨 role 新增与 revision 替换重新校验，不能从另一 role 借冻结豁免。原 spec/pin/binding 读回不变。 |
| A26 当前门状态 | full 资源已经 release；implement 仅低负载 targeted。新对象需独立增量 review、once 机械绑定和独立核验后由验收 Run claim 一次，旧 review/full/CI 仅作历史。未执行不报 PASS；本轮不 merge/deploy。 |
