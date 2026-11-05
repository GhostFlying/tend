# 声明式 Capability 当前验收矩阵

当前规范是 owner `msg_01M6NRJFA40Y2ABY7GKMCEHEPV` content revision 1；优先于旧 spec2、原三输入与旧 D174 native 描述。此文件在新代码之前写入，断言是待验证要求，不能当作已通过报告。原三输入保持原 file ID/size/SHA，原计划和失败历史继续可追溯。

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
