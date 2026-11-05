# 当前范围纠正与实施顺序

本节先于新代码写入。owner source `msg_01M6NRJFA40Y2ABY7GKMCEHEPV` content revision 1 优先于旧 spec2、下方历史计划和原调查 A01–A28。原计划提交 `6a8090ea`、旧候选 `7c7696fb`、原三输入及全部失败/Run/decision/request/unknown 历史保留；下文旧 native 建议不再是当前门禁。

当前候选从 `7c7696fb` 续接，工作树核验干净，原 PR244 OPEN 且存在冲突；不另建 Task/Issue/环境，不改其他责任。完整 heavy 检查仍 HOLD，本轮仅源代码修订、必要低负载 targeted 和候选准备。独立 formal review 由 Tend 原责任链安排，不在本 worker 内启动其他 Agent。合入、安装与部署不在本轮授权。

## 修订后的实施批次

1. 更新 D174 和[替代验收矩阵](capability-declarative-acceptance.md)，逐条标注原 P0–P6、A01–A28 的适用、撤销或替代。
2. 定义/修订维持 CAS、请求去重和追加历史；新定义支持 declaration_v1，历史 tool_inventory_v1 元数据继续可读但不控制匹配。配置 `capability_version=1` 的 Agent capabilities 是显式用户声明，支持在一个 reload/register 中逐项或批量配置。无声明不会按 CLI/品牌/模型推断。
3. protocol matcher 只检查声明覆盖 required（包含 definition revision）；scheduler 另外保普通 executor 可用性、协商、身份/profile/backend、双 role 与共享预算。撤销 inventory/source/TTL/profile/session、prepare/adopt/consume/nativeguard/StartWithCapabilities 资格门。
4. run binding 固定 required/digest、原 pin 以及该 pin 的 Agent revision（声明随历史 inventory 冻结）；claim/reconnect/launch 不探测工具，不启动额外会话，普通 Start 仍通过原 resolver 的精确身份。保留旧 binding/evidence/revalidation 的读取，不重写历史选择。用独立声明 feature 区分旧 native-gated peer，避免旧 runner 接到新语义而拒绝启动。
5. 修正旧 review 三项：所有 capability precheck 在 NextAttempt 清 stop proof 之前；投影使用注入时钟且不再把 now=0 当 native 资格；解释候选不得覆盖原 required AgentID/RuntimeID/backend 来伪造匹配。新增反例回归。
6. 更新 CLI/API 帮助、必要目录/UI 的声明/可用性投影与配置示例；双 role required、过滤/分页与旧修订查询仍保留。真实生图调用为零；不声称任何生产 Agent 已配置或部署。
7. 必要 targeted 在现有工作树低负载运行，保原 CI journal-v14 fixture 失败与 P0 partial 事实。提交并更新原 PR 候选，记录 exact task base/head/tree 与 fresh PR base/target。最终 full/required CI/新独立 review 必须绑定最终候选；资源门未释放则明确 NOT_RUN/HOLD，不沿用旧通过。

兼容评估：旧 required 为空和旧 feature 的默认/持久化字节不变；新声明语义使用另一个协商 feature，只给理解该语义的 runner 新 assignment。旧 v1 native binding 作为历史读取，未结束 run 沿冻结 pin 与原恢复合同，不改原 request/unknown。新 binding 显式 match_policy=declaration_v1；无该字段的旧 binding 继续允许原 evidence-only peer 原样恢复，不向其发送新 match_policy/声明 current_match。没有删除字段或收紧旧有效输入，不改变 runner 2/1 代际；旧 runner 不获声明模式任务。迁移仅追加，旧 reader 拒绝新 journal；生产升级/回滚库存审计仍由独立交付窗口承担。

---

# Capability 基础设施实施计划

历史状态（已由下述范围纠正替代）：续跑实施计划，承接原计划提交 `6a8090ea` 和原三文件；尚无产品代码。按冻结合同纠正，缺少 runtime 接口仅限制真实确认与生图步骤，不阻塞其余产品编码。计划仅覆盖候选准备，不授权合入、安装或部署。

## 冻结输入与吸收

三份原调查输入已通过任务正式下载工具取得，并全文阅读、重新计算字节数与 SHA-256。保留原件和调查作者的结论；本计划是实施吸收记录，不替换或重新署名原调查。

| 原文件 | 字节数 | SHA-256 |
|---|---:|---|
| capability-investigation.md | 1888 | 385bebb9251b653210f9f1495cb056494d35e7421ef9586e1c77477b9b0d26e9 |
| capability-end-to-end-design.md | 27859 | 7dd6b65c855743b570497100cbed7dd06fdcc1f33cdf06b44921db6110d392e8 |
| capability-implementation-acceptance.md | 14881 | d244e9638b59aae6f90b6bb521ca1e3add32d604e9e3e3b59efe9a87a9d296ca |

完整采用原方案的三类版本（定义、Agent 声明、executor 实证）、不可变历史、精确来源/config/session/TTL、两角色独立 required 集合、claim 原子固定及 launch 复核、unknown 原请求对账、共享 admission 和具名 CLI/API 合同。类别仅代表委托角色；模型或 CLI 名称不授予能力。声明、模拟、工具暴露、真实调用成功必须分别呈现。

原 P0–P6、A01–A28 保留为实施和独立验收依据，不能把文档核验算作产品通过。原文建议的具体 TTL、超时和通知期限须形成可测试合同；初始采用工具证据最多五分钟、现有六十秒心跳围栏、单次无模型验证三十秒、十分钟一次持久阻塞通知，禁止自动验证循环。真实生图遵本任务最多一次最小图片调用的冻结上限；调用前必须核工具、可观察配额/费用及精确入口，不扩展预算。

## 当前基线、差异与根因

任务起点和当前 HEAD 为 `e8657a07a883bad758fbaff73f5e20ffbecd8c5b`；原调查基线为 `bc2be98a8e1e735f2d506195f0ecddf14ce91408`。初次及中断恢复后工作树均干净；没有产品改动或外部模型调用。

两基线之间主要新增 manager 原收据读取及停止证明，未实现通用执行能力模型。`internal/agentconfig/config.go` 仍接受格式 1/2 并包含第二协调入口 `OwnerRuntimeID`；`internal/protocol/agent_inventory.go` 仍保留 `OwnerBinding`。`AgentRequirement` 没有 required capabilities，`RunExecutionPin` 是可比较值；`agentexec.Pool.Observe` 的 Available 只来自 adapter 初始化，`probe` 检查 executable/workspace 协议或 app-server 启停。`harness.Workers` 只有 Start，没有权威工具枚举或准备会话消费接口。现有 bridge 的 tools/list 只列 Tend 自身 worker 工具，无法代表原生会话全部工具。

因此根因不是缺少 image_gen 布尔标记，而是缺少定义/声明、真实入口证据、冻结约束到调度/启动的事实链。直接把 Available、skills 名称或当前 worker schema 写为 profile confirmed 会跨会话扩大证据作用域，违反本任务的主要约束。

只读核对得到并行单入口候选为 PR #236，head `0f838ec4533f84ea37c8adc29ed3cda45e0a89f3`，状态 open。其格式 3 拒绝第二入口，显式 CLI；允许指定 Agent 时省略 category；协议改为 3/2，保留 runner-v2、拒绝协议 1。其公开说明仍报告 Orca coordination 未实现，不能把 CI 成功当作该路径完成。PR #240 是另一项已合入的收据恢复修复，不是单入口候选，不能机械等它作为本任务前置。

本计划没有检出、cherry-pick 或吸收该候选，也没有改其冻结合同。声明扩展应沿其最终单 RuntimeID 入口与不可变 public projection；能力绑定另表保存，保持原 pin 可比较及旧 JSON/hash。原责任链须确认采用的精确接口候选；这不要求先合入 PR，也不授权改造其他责任。

## P0：冻结接口和证据边界

1. 以当前起点及上述精确并行 head 核对 Config、projection、AgentRequirement、claim/恢复和 session 生命周期接口。恢复后重新读回候选，不能继承已漂移 head 的结论。
2. 定义 `CapabilityReference`（ID、definition revision）及声明扩展位置，规定 canonical 排序、去重、克隆、Equal 和 nil/empty 历史行为；两角色分别冻结。
3. `RunCapabilityBinding` 独立存 required digest、证据引用及同入口 session/reservation 安全引用，不改变原 RunExecutionPin 的可比较语义。
4. 解决真实入口工具证据接口：要求受信任 adapter 返回完整性、tool/schema/source digest、来源版本、config/Agent/runtime revision、session/scope 及观察期限。仅 session scope 时必须准备并消费同一 session，使用原 admission，支持请求恢复与未知保留。
5. 当前机器正式 Orca 命令 schema v1 共 320 命令，没有上述工具枚举或 prepare/adopt 命令；agent launch 的可见 flags 为 agent/worktree/prompt/model/effort/surface/focus/retry-launch。该结论限于正式可发现接口，不宣称外部实现绝无隐藏能力。当前 Tend 接口也不能填补该链路。不得猜隐藏协议、以 worker 自报 confirmed 或另起 Agent 绕过。
6. 本会话暴露 image_gen schema，但未核实其工具来源版本、公开配置修订与可调度入口之间的权威关联，且没有发现相应配额/费用查询工具。当前状态只能是工具暴露已观察、实际执行未验证；调用次数零。交原责任链提供合法接口/精确绑定及可观察预算路径，或在原责任链安排对应 runtime 支持，保留本任务原合同。

## P1：定义、声明与注册

- 在 protocol 定义版本化能力、验证策略、引用、公开状态/原因，首例 image_gen。store 追加 migration 保存定义历史与 CAS 当前指针；写入走 app 的同事务请求去重、事件和 outbox。退休阻止新约束，不删除旧历史。
- Agent 声明随本机配置修订、私有 snapshot 与公共 hash 登记；旧配置和原 hash 不重序列化、不补确认。声明缺失表示没有声明，不能解释为已完整观测缺能力。
- 公共 `capability list/show/history/put` 与本机 validate/reload/register/registration/reconcile 同片提供完整输入、CAS、原请求 replay、帮助和实际入口用例；不引入远程私有配置写入口。

## P2：executor 证据与本机恢复

- 实证为追加事实，区分 tool_inventory、execution、simulation，保存精确绑定、source/schema digest、完整性、时间、request/sequence/incarnation 和安全产物引用。公共 DTO 不输出私有路径、配置正文或凭据。
- `unverified/confirmed/unavailable` 和派生 expired/binding_changed/session_ended 共用匹配逻辑；旧 sequence/重启/迟到结果只留历史，heartbeat 不能续工具 TTL。
- 受信任 adapter 经 runner 身份上报；普通 owner/worker 不能写确认字段。支持不了的路径明确 adapter_evidence_unsupported，工具未知不能写 tool_absent。
- 私有具名 `agents local capabilities verify/status/reconcile` 绑定稳定 request_id/CAS 和 journal；一次验证、超时取消、重启恢复、同原请求对账。不得隐式付费调用或创建免费并发槽位。
- 出口要求至少一条真实非模拟入口能提供合法完整证据。当前 P0 缺口未解时不把本阶段记完成，也不编造可成功匹配的实现。

## P3：Task、调度、启动与解释

- 将两角色 required 从 OwnerTaskSpec 显式映射到不可变 Spec、规范化/Equal/schema/hash/查询；显式 revision，拒绝未知/退休或冲突引用，非空集合要求 named selection。
- 复用原纯调度和同事务 claim：先现有身份、角色、profile、host/backend、workspace、reviewer 与共享预算围栏，再检查唯一入口声明和有效证据全集。不得拼接不同入口。
- 冻结证据、session reservation、run pin、spec、lease/request/event/outbox 同事务提交；事务失败不残留预约。launch 同入口复核，丢失则 capability_lost，不执行目标业务或暗换入口。
- 原 continuation/handoff/reconnect 优先固定身份及历史 pin；恢复证据追加，unknown 不释放、不换请求重发。
- explain 与 claim 共用纯 matcher，返回 eligible/pending/unavailable、missing、原因、checked/next/deadline 及恢复动作；只读查询不探测或创建 lease。等待超限一次持久通知，无无限静默重试。

## P4：完整查询、工具与必要 UI

- 定义列表/历史、Agent capabilities 列表/show/history/evidence、Task capability-explain 及 task show/list 完整 named CLI/API，按原建议逐叶提供 help、完整 JSON、输入校验、筛选/分页及固定水位游标；跨页换筛选报错。
- 公共写提供 dry-run 原 Envelope、request show/replay，stale 不自动换 CAS。D142 路由/operation/本机入口目录与真实 CLI 入口测试同步，不用通用 RPC 壳代替。
- 协调工具只读目录及本 Issue 合法 Task 解释，worker 只读本 run；不扩大原生执行权限或允许写 confirmed。
- Agent 目录显示声明、当前实证、最近调用及未知/失效原因；Task 展示 required 和阻塞恢复。schema/TS 运行生成器。只做本能力必要投影，不实施 UI235、不影响非生图交互 sandbox。

## 协议与历史兼容出口

当前树为 2/1，strict 解码拒绝未知字段；并行候选已走 3/2。这是实际冲突，不能照搬调查的“基于 2/1 增量”作为既成结论。

- 精确接口决定后评估所有 hello/heartbeat/claim/assignment/reconnect 发送点。优先只向协商 executor-capabilities-v1 的 peer 发送新字段；旧无 required 的请求、默认及恢复行为原样。
- 非空 required 绝不给不能理解的 runner；公开定义/声明格式、任务/journal 可读范围与 release manifest 同步。旧 reader 不能丢约束执行。
- 若实际收紧旧有效请求或改变恢复语义则递增协议并保留 N−1、删除 N−2 通信 adapter/marker/登记、增加拒绝测试；在并行 3/2 基础上重新判断，不机械再升级，也不退回 2/1。
- 迁移只追加；副本验证旧 config/spec/pin/receipt/unknown 原字节/hash/语义和 nil/0 CAS。通信退役不删除历史读取。发布库存/排空只留后续 delivery 责任，本任务不操作。
- 实施时在 docs/design.md 分配当时未占用的新决策编号，同提交更新；不抢占并行候选或当前树已经使用的 D173。

## P5：首例、检查与候选

真实 image_gen 首例必须来自最终精确入口和会话；先读取合法工具来源和预算，最多一次最小图片调用，记录 request、配置修订、时间/状态及原输出 SHA、大小/可解码；费用无法读取即 unknown。未知响应只查原请求/已有产物，不重试或换 provider。没有合法证据接口时交回精确技术问题，不能用 schema 或替身通过首例。

按以下分组覆盖原 A01–A28；当前所有产品项均未执行，不能记通过：

| 原验收项 | 实施及验证落点 |
|---|---|
| A01–A04 | 定义 CAS 竞态/去重、同四元组多身份、声明/注册响应丢失、原 bytes 与未知请求迁移 |
| A05–A09 | 工具来源/完整性/模拟负例、跨绑定与会话反例、TTL/乱序/重启、prepare/adopt 和共享 reservation 恢复 |
| A10–A16 | 两 role 冻结/非法输入、同事务选择、解释一致、有界通知、launch 丢失/原身份恢复、独立验收及共享预算 |
| A17–A18 | 实际协商路径的新旧组合、strict decoder、旧请求恢复；实际 breaking 时 N/N−1 与退役拒绝 |
| A19–A23 | 每叶 CLI 真入口/help、筛选分页游标、请求恢复、权限反例、当前/历史 UI 诚实呈现 |
| A24–A25 | 一次真实生图、原图校验、费用/拒绝/未知证据，禁止未知重试 |
| A26 | 最终 commit/tree/diff、完整 make check 退出状态、PR CI 真实读回；原责任链独立 formal-review |
| A27–A28 | 超出本任务权限；后续独立 delivery_authorization 负责受控升级与真实上线读回 |

定向回归先证明错选/丢字段/跨 session/错误恢复会被阻止，再运行完整 make check 并保留日志、最终 head/tree 和退出状态。PR 触发 CI 后真实读回；没有运行即未知。仓库独立 review 和 Tend 正式验收由原责任链的独立 reviewer 执行，本 worker 不启动原生 subagent 或外部 reviewer，不用实现者解释替代独立判断。

## P6：交付边界

完成后显式提交 commit/diff、计划、设计吸收、A01–A28 矩阵、命令日志及真实首例原件。列出未完成/未知及风险，不把候选/CI 当上线。

原能力责任与 Agent、设计、board 责任独立关联；只有确需生图阶段依赖 image_gen。正式合入和受控升级留原 Issue 交付义务、精确版本化批准与独立 delivery_authorization 任务，在同一协调窗口执行。本任务无合入、安装、部署或其他任务窗口操作权限。

## 本次续跑批次（编码前修订）

1. 先交付定义 CAS/历史、私有声明及公共登记、完整具名查询/CLI/目录。声明采用独立可选扩展版本，不占用并行单入口配置格式；有声明时拒绝旧 peer 登记，不修改旧 snapshot/hash。
2. 两 role required 放入现有 selection，深复制、集合比较与规范化贯通。纯 matcher 共用于 claim 和解释；当前 adapter 没有受信任证据时安全阻塞，不能把 Available 提升为 capability。
3. 新增最小受信任 runtime 证据合同：adapter 结构化 tool inventory（来源/config/scope/TTL），runner 身份上报与追加历史；当前不支持枚举/prepare/adopt 的适配器明确 unsupported。session-only 证据不得授权新会话。实现 negative、TTL、错绑定及模拟反例；未来 profile 证据必须由明确支持的受信任 adapter 提供。
4. 采用现有 2/1 feature 协商，新增 executor-capabilities-v1，不收紧旧有效输入；新字段只向已协商 peer 发送，旧 required 为空的任务默认及恢复不变。若实作无法保持则停止该协议路线并报告精确缺口，不静默改代际。
5. 保守路径也要完成定义/声明/Task 输入、查询、调度与解释测试和必要 UI 投影；真实 image_gen 仍最多一次，当前不可观察费用且无精确入口证据时不调用。未完成的真实适配与独立验收如实列出。
6. 不修改 AnswerDecision/resumeAnsweredQuestion 控制流；独立 review 由 Tend reviewer 承担。完成本批候选与检查后再按原责任链交付，不把本批进展称整体验收通过。

本次恢复核对：工作树干净，HEAD 为原计划提交。三个输入再次正式下载、全文阅读并重新核验原尺寸/SHA；不重做调查。并行 Agent 候选只是接口参考，不吸收其实现、不等待其合入；能力仅绑定现有 worker 的单 RuntimeID，拒绝将 OwnerBinding 或其他 purpose 证据拼接进 worker 能力。

## 已落地候选批次

本次实现依据 D174。新增 server migration 88、runner journal 16/17，定义/声明/证据/两 role required、具名查询与本机验证恢复、claim 原子绑定/launch 围栏和必要详情投影。保守 adapter 边界已在本仓库设计实现，不再被当作全部编码的外部前置；当前生产 adapters 仍明确不支持 authoritative inventory/atomic launch，真实调用和完整生产闭环保持未验证。

定义 read 的 CLI 修订参数采用 `--definition-revision`，避免与既有 CAS flag 重名；对应 HTTP 仍为 revision。session prepare/adopt 本批明确 unsupported，不以 profile 推断填补。原始设计/验收文件继续作为原作者冻结输入，不另建调查。详细验证、矩阵与未完成边界随候选报告交付；独立 review、CI、合入和部署不会由本实现者自报通过。

## 本轮 review 修复计划（编码前）

本轮沿原分支与 PR244 修正上轮独立验收的三项 finding，owner v1 声明语义保持最高优先级。原候选及失败事实不覆盖，不新增环境或 Agent。

1. R1：缺省 MatchPolicy 的旧 evidence-only binding 使用独立历史恢复适配，按原 pin、incarnation、sequence、有效期判定新鲜 revalidation，恢复 ReportCapabilityEvidence 的同事务追加和 assignment 投影。仅旧绑定进入此分支；declaration_v1 的新运行继续普通声明匹配/Start，不加 native 门。回归覆盖原收据返回、原绑定不变、过期/错绑定拒绝及新声明不生成复验。
2. R2：保留普通 adapter 不可用的候选事实供只读解释，claim 仍只使用可用 executor。按完整冻结身份/role/backend 约束核验可恢复性后，将容量、runtime admission、离线/adapter 等待投影为 pending；无声明/修订/不兼容仍 unavailable。通过 App 查询与 Claim 真实路径验证拒领和恢复后可领。
3. R3：更新当前使用指南与一次持久等待通知，恢复建议为显式配置声明及恢复同 executor 普通可用性；旧 verify/source/session 材料明确为历史诊断，不作为新匹配前置。
4. 运行有界 targeted 回归，记录 exact head/tree、task base、PR head/base/main、diff 与结果。heavy full 继续 HOLD；独立 review、最终 full/required CI、目标集成仍须原责任链安排，本轮不 merge/deploy。
