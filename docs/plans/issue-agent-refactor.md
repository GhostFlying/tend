# Issue 与 Agent 重构实施计划

状态：P0、P1 owner API/CLI、P2 配置库存、P3 执行调度及 P4 自主负责人已 CI 和 rebase 合并。P5 Attention/Planner 的规定 review、五项修正独立复审、正式完整检查及交叉编译已通过，准备提交。后继对话、归档和最终接口/UI 候选正在独立全检。全部黑白设计已确认，实际 UI 换行修复已复核。隔离真实双事项、回答接续和 workspace 复用已验证；关闭接点发现的新 Attention 证据缺口由后继窄修正处理。生产未部署，完整产品验收未完成。

## 进度账本与角色

本轮主协调由 root 单一会话负责；实施 Agent 固定为 GPT-6.1-Sol high，独立 reviewer 为不同会话的 GPT-6.1-Sol xhigh。独立 reviewer 不实施其评审改动，协调者不把实施自评当作独立结论。UI 设计另行经 xhigh review 和用户确认。

| 批次 | 依赖 | 设计 | 代码 | 独立评审 | 合并 | 部署 | Live 验收 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| P0 | 无 | 文档完成 | 无运行代码；本地 make check 通过 | 已通过，澄清增量确认 | PR #157 修复已合并；PR #155 CI 通过，rebase 合并至 `8c8c6ec6` | 本批不部署 | 无 |
| P1 | P0 评审通过 | P1a、控制分片及 owner API/CLI 组合计划已写 | P1a 已合并；P1b1/P1b2 本地完整检查通过；P1b3+b4 r2+P1c 已正式纳入，组合定向检查已通过；manager/Agent assignment 未启用 | 历史子批及 b3/b4 r2/P1c 候选预审通过；r1 CLI 两项 P2 已修复，r2 独立复审及完整检查已通过，已提交 | P1a PR #158、P1b1 PR #159、P1b2a PR #161、P1b2b PR #163 已合并；owner API/CLI PR #165 CI 通过并 rebase 合并 | 本批不部署 | 未执行 |
| P2 | P1 身份基础 | P2a/P2b/P2c–P2d 子计划已写 | P2a/P2b 本地检查和独立复核通过；P2c–P2d 本机配置、Category/安全库存登记及恢复入口已纳入，正式定向检查和完整 make check 均已通过；执行/调度由 P3 接续 | P2a/P2b 正式独立 review 通过；P2c/P2d1/P2d2 候选独立预审通过，正式组合人工及规定 CLI review 均通过，无 findings | P2a PR #160、P2b PR #162、P2c–P2d PR #164 CI 全通过并 rebase 合并；只读目录增量待正式检查 | 本批不部署 | 未执行 |
| P3 | P1、P2 | [执行计划](category-agent-execution.md)已写 | 显式 named factory、调度、模型/effort/指令及原 pin 恢复已正式纳入；组合定向 race/vet、schema/TS 生成与类型检查通过 | 候选 r1/r2/r3 独立复核通过，已采纳首资格、profile 和 start-over 修正；正式 r2 规定 CLI review 无 findings，r3 schema 独立窄复审及完整 make check/交叉编译通过 | PR #166 CI 全通过并 rebase 合并至 `bfb0ffff` | 本批不部署 | 未执行 |
| P4 | P1–P3 | [实际接入计划](hosted-issue-agents.md)已写 | r19 已纳入，正式 r13 完整 make check 与交叉编译通过 | 规定 CLI review 及 r17–r19 独立修正审查通过；不采纳意见有明确业务依据 | PR #167 CI 全通过，rebase 合并至 `668c8f74` | 本批不部署 | 原 A/B 恢复完成；新调查→定义更新→后继执行已验证，非最终产品验收 |
| P5 | P4 | [实际实施计划](attention-planner.md)已核定 | 44 路径正式纳入，r2 完整 make check、交叉编译通过 | 规定 CLI 五项 finding 均采纳；修正独立复审通过 | 准备提交，CI/合并待完成 | 本批不部署 | 隔离原问题 UI 回答与原 worker 消费、action 收件及 SSE 已验证；关闭证据接点后继修正中 |
| P6 | P3–P5 | 对话/来源合同已复核；原生身份增量计划已写 | P6a/P6b 与完整依赖组合定向检查通过，正式全检运行中；已知 runner 测试夹具修正提前纳入后重跑 | P6a/P6b 及最终组合预审通过 | 未合并 | 本批不部署 | 未执行 |
| P7 | 稳定 API、设计 review、用户确认 | 主界面及剩余十二张黑白设计均已确认 | 全部批准表面实现；实际八条 CI 浏览器命令和 Web 检查通过；真实只读检查发现长标题换行问题，r9 窄修复已在真实页面复验；最终全检运行中 | r8 和累计 UI 集成独立审查通过；r9 窄审通过 | 未合并 | 本批不部署 | 原对象与下载只读验证通过，完整产品交互仍待验 |
| P8 | P1 开发，P2–P6 演练 | 离线、只读 API 与在线准备最小合同已收口 | P8a/P8b/P8c 与 P5 组合定向检查通过，正式全检运行中 | P8a/P8c、P8b 及最终组合预审通过 | 未合并 | 本批不部署 | 未执行 |
| P9 | P0–P8 | 合同及真实验收矩阵已写 | 最终隔离候选已构建并核对实际资产；原账本责任恢复完成 | 候选组合独立核对通过；真实只读结果按范围核验 | 未合并 | 生产未执行 | 双主机、原问题回答、下载与工作区复用已验证；最终关闭、咨询、期限/原生控制及切换待完成 |

每个批次/子批建立如下证据行，开始下一阶段时填真实值，未发生项保持“待记录”，不能以计划填成完成：

| 批次 | 固定 base SHA | 实施 head SHA | PR / CI | reviewer / 结论 | 部署 build / manifest | Live 对象 / 结论 |
| --- | --- | --- | --- | --- | --- | --- |
| P0 | `7761e61c0e9acdb0209fddff7f5d0e55830b4c65` | 原评审 `5b20a8ad4536cb70341607066452a07e487a702e`；rebase 本地 `e11f326da321121cb85f7fa3186532b8c2b8506c` | [PR #155](https://github.com/GhostFlying/tend/pull/155)；本地 make check 通过；基线修复已随 PR #157 合并；head `02ea061f` 的 CI `40757992209` 通过，rebase 合并至 `8c8c6ec660de59e2cbe48a0ce3db9d8f9a119fb5` | GPT-6.1-Sol xhigh 通过，澄清增量已确认 | 不适用 | 不适用 |
| P1a | `5b20a8ad4536cb70341607066452a07e487a702e` | 原评审 `d52d093741da41e78dc60effb0493a67058eded5`；rebase 本地 `71ae095858626acae6a9ad5e5450724c0e4ea936`；冻结实现快照 SHA-256 `b94229bf4efe0dc91932358af1091004951c7b101be73442fb3dc288863b218f` | 本地 make check 通过；[PR #158](https://github.com/GhostFlying/tend/pull/158) CI 全通过，rebase 合并至 `2b39915f` | GPT-6.1-Sol xhigh 无 findings；后续仅状态记录增量 | 未部署 | 未执行 |
| P1b1 | `242ff5200c7bda28c70de3c313ffef7a55d64717`（含独立基线测试修复） | 原评审 `9b7b85530b65dc2516425c8e7a131d762ead08a1`；rebase 本地 `07effd5231375a31460eb384682e65969b062fd7`；r2 实施快照 SHA-256 `8f99a5d2fbb478ce8a28fec78312a9cdff8a4549d4255e5fa99b867d4d2c06c6`；入口未启用 | 首轮完整检查因 schema fixture 上限失败；修复后定向升级/维护 race、第二轮 make check 均通过；PR CI 未完成 | GPT-6.1-Sol xhigh 首轮及 r2 增量均通过，无 findings；随后仅本次台账增量 | 未部署 | 未执行 |
| P2a | `9b7b85530b65dc2516425c8e7a131d762ead08a1` | 原评审 `e2929e1f4c2cf170e24dbc580cf0f31b39289ab0`；rebase 本地 `3fcc88b37ae2c71971dfd7c38facb8652037ed64`；该片纯配置包当时无生产调用点，后续加载/登记见 P2c–P2d | 定向 race/vet 与本地 make check 通过；PR CI 未完成 | GPT-6.1-Sol xhigh 人工及规定 CLI review 通过，无 findings；随后仅本次状态增量 | 未部署 | 未执行 |
| P1b2a | 原评审 `ffd0cf92510fce66496b81bef8c676ad08639263`；rebase 验证 `3fcc88b37ae2c71971dfd7c38facb8652037ed64`（tree 一致） | 已提交 `2edfda826ea247ea46d7a4cca121d5afb3f6d36d`；r3 实施 patch SHA-256 `cb0a8786e5670d91e83e6e0b9391e0bd0b3e458a641daf9792f36420ae59078c`；随后仅状态文档增量 | 定向 race/integration 编译及 r3 完整 make check 通过（app 2016.963 秒、mainthread 388.341 秒、Web 全部）；过程证据见子计划；PR CI 未完成 | r1/r2 findings 均采纳；r3 独立复审、本次状态记录窄范围核对通过 | 未部署 | 未执行 |
| P2b | `fdd81c682875bfeeefceb2dfc1a9405c6acdae99` | 已提交 `1279b4748bc006dc286cc58484926b5d2bd1889f`；本地配置存储与 journal 11 CAS 恢复，该片未接公开/生产调用；后续入口见 P2c–P2d | 定向 registry/journal race/vet 及发布兼容回归通过；r1/r2 完整检查及修正定向 race/vet 通过；PR/CI 未完成 | 正式 r1 review 一项 typed 空列表缺陷已修正，r2 独立复核通过 | 未部署 | 未执行 |
| P1b2b | 原 `1279b4748bc006dc286cc58484926b5d2bd1889f`；rebase `ae891c455031fca1098ff26ff2c06d3530c51683`（tree 相同） | 已提交并 rebase 为 `2ed83de7944773d5a0a416666bcb35884c62fe56`；六个 app 文件匹配原已复审候选后，正式 CLI 迟到 share 修正已纳入；无 task/manager/公开入口 | r1 定向 race/vet 通过（app 71.361 秒）；r1 完整检查为已知 finding 主动未完成；r2 定向 race/vet 通过（81.205 秒）；r2 完整 make check 通过；PR/CI 未完成 | 原候选复审通过；正式 r1 CLI 一项迟到 share 缺陷已采纳修正，r2 独立增量及正式组合复核通过 | 未部署 | 未执行 |
| P2c–P2d | `2ed83de7944773d5a0a416666bcb35884c62fe56` | 本批纳入本机配置/Category/安全库存登记和恢复入口；无 factory/调度/hosted 可用性 | 候选及正式组合定向 race/vet、roots 生成/类型检查和完整 make check 均通过；PR/CI 未完成 | 三片候选及正式组合人工/规定 CLI review 均通过，无 findings | 未部署 | 未执行 |
| P1b3+b4 r2+P1c | `7b0edfb4ce7d340a4f2e26546d4e5d0608de92e5`（tree 等价 P2 已验证提交） | 已提交并 rebase 为 `964167d50923fd5ce5053e1187c9624f5540856f`；b3 候选 migration 0055→0056，P2 roots/CLI/API 语义合并 | 定向 race/vet 与 schema/types/CLI 检查通过；r1 CLI 两项 P2 已修复，r1 make check 主动未完成；r2 独立复审及完整 make check 已通过；PR/CI 未完成 | b3、b4 r2、P1c 候选预审均通过；r1 CLI 两项 P2 已采纳修复，r2 独立复核通过 | 未部署 | 未执行 |

本次 rebase 保留历史评审 SHA/实施快照，以上本地新 head 不等于 PR 已更新或 CI/合并已完成。P0 先前 CI 因 idle 摘要 fixture 失败；独立修复 `315f0c5f` 已经 [PR #157](https://github.com/GhostFlying/tend/pull/157) 合并。PR #155 rebase 至 `7435b030` 后的[新 CI](https://github.com/GhostFlying/tend/actions/runs/40757992209)已通过（包括浏览器回归及 Windows/macOS 交叉编译），PR 已 rebase 合并至 `8c8c6ec6`。P1b2a r3 完整检查时段 UTC 18:28:06 至 19:02:18，退出 0；日志 SHA-256 `3bfe1500d1f55cbe7c4ea45d7ab14a77792c18268ca8eb6cb3ae5d4d54905801`。P1b2a、P2b、P1b2b 已提交；P2c–P2d 子批当时在 P1b2b 完成基线上纳入配置/库存链路，不带入 P1b3/b4、factory 或调度；该子批已经本地完整检查与独立 review 通过并提交，P1 owner 组合当时从 `7b0edfb4` 纳入已预审的 P1b3/b4 r2/P1c owner 链路，该片本身无 factory 或命名调度；P3 接续见下文。

Base 在开始批次与评审前分别固定，若 rebase 改变 head 则更新并判断是否重审。公开账本只记录公开 PR、commit 和无私密信息的证据引用；机器运行路径、凭据及原始发布证据保存在忽略的本地记录中。

## 固定边界

- 主 Agent 和 issue manager 都只规划和委派，源码/日志调查也走 task；保留独立执行和验收。
- 同一 Issue 递归组成 parent tree；一个具体责任人和 assignment epoch；task 唯一 primary issue，stages 可选。
- Global category、每 runtime 多 Agent/多 category；支持 HARD runner 和 workspace host，首次选择就要求同 host 的实现及验收能力。
- 直接聊天保留 owner 原文。独立普通咨询无 issue/task，显式执行 promotion 原子去重；run 补充不修改合同或复活终态。
- Attention 与 assignment 分离；deadline 默认前 24 小时和到期唤醒主 Agent；无变化静默。
- 复用 outbox/followup/batch；手动来源先行，无飞书连接器；Orca 接管保留，Cove 只定义合同。
- 旧账本一致性只读离线档案加新空业务账本；reader 不调用正常 store.Open，unknown 阻止切换。本计划取代旧 mandatory-message-context 部署计划的删除历史、不保留归档/恢复材料安排，不改历史文档。
- UI 先生成设计、xhigh review、用户确认再编码；中间批次不部署。

## P0：设计与落地合同

产物为本文、`docs/issue-agent-design.md` 和 `docs/design.md` 的新决策。核对最终语义、兼容/退役义务、公共文档无机器信息、批次验收及切换停止条件。独立 reviewer 不承担实现；意见逐条判断并记录，获下一批指示后实施 P1。

## P1：递归 Issue 领域与责任

P1a 先做机械命名整理，详见[独立子计划](issue-internal-names.md)：核对 protocol DTO、domain/store/app、task ownership/context 和 views 的依赖图，把可独立迁移的 Go 函数/命令名称及 helper 改为 Issue。保留 protocol DTO 反射名、已提交 migration、table/JSON/wire 身份、旧 owner API 和旧 UI 行为；不添加无用途 aliases，不手改 UI；schema/types 重新生成后验证原字节不变。此批不改变授权、状态机或派发行为，不宣称公共 cutover 完成。

P1b 的独立存储/命令/上下文/执行闭环分片和启用门槛见[控制实施计划](issue-controls.md)，合同已获独立评审确认可实施；P1b1 代码、本地检查与独立评审已完成，入口仍未启用。P1b 增加 canonical Issue revision、parent/dependency、assignment epoch、task primary ownership 和 create/update、pause/resume、cancel、close/reopen、reparent/assign 行为；追加 migration，不改历史文件。采用新列或新表前在本批接口说明中固定历史读取与新活动路径，不让旧 work_item JSON 暗中变义。Stages 可选，第二 issue 引用仅表达结果引用。共享 helper 只在不改变历史语义时提取，task/run/artifact/acceptance 状态机保留。

P1c 增加 owner API/CLI、views 与生成 schema/type 胶水，提供准确 revision、epoch 和阻塞原因。旧活动 facade 的调用点和移除批次登记在本计划，最终 P6/P7/P8 cutover 就绪后移除写入口；历史 reader 不退役。涉及通信兼容的 facade 同时登记协议退役版本。P1a 及早期 P1b 为已分离的可构建批次；P1b3+b4 r2+P1c 组合为一片可实际使用的 owner 链路（见 [组合计划](issue-owner-delivery.md)），不混入未经批准的可见 UI 改动。

必须验证：父子/依赖无环、单责任人、唯一 primary issue、可选 stage、revision 冲突、pause 阻新派发且活动合同继续、取消的正式协调、关闭/重开、reparent 拒绝活动 turn/执行及授权冲突、unknown 阻止伪关闭、旧 epoch 无控制权限。Assignment 按围栏旧写、核对旧 turn/unknown、启用新责任的顺序生效，不接管 worker 或改 spec。完成条件为 API/CLI 可创建/修订/委派/读回，领域和事务测试、旧执行回归、make check 通过，自动推进未启用。

## P2：本机 Agent 库存与冻结配置

P2a 的纯配置校验、身份与公共投影见[独立子计划](agent-definition-config.md)，纯配置基础及本地检查已完成；P2a 本身不接加载或登记调用，后续入口见[配置与库存控制计划](agent-inventory-control.md)。定义仍 staged/unavailable；真实角色指令注入、原 runtime/snapshot 可验证恢复、同 host 全局 workspace/backend 协调和实际执行能力仍待后续，不以纯包或登记测试替代启用门槛。

P2b 的本地配置持久化和通用 journal CAS 恢复见[本地存储子计划](agent-registry.md)。本片追加 journal 11，旧请求 NULL 不回填；本地快照、成功 receipt、CAS 和 unknown 恢复不等于 server 库存或 runtime 已启用。定向 race/vet 和发布兼容回归及 r1 完整检查通过，正式 review 的 typed 空列表缺陷已修正并通过小包回归、r2 独立复核与完整检查；CI/合并未完成。

P2c–P2d 的[配置与库存控制计划](agent-inventory-control.md)接通固定来源严格 loader、原私有 socket 本机控制、owner Category CLI/API、条件 hello ack、journal 原请求登记与重启对账。公共 DTO 唯一定义于 protocol；server 仅保存版本化安全投影与原子回执，不接收本机私有配置。入口已纳入，状态区分本机 local_accepted、server registered_staged、unknown 与 runtime unavailable；候选预审不替代正式组合检查。该批不加载新 Agent factory、不改变旧 worker/model 缺省、不启用 run 选择或调度。

后续仍依赖 P1 身份基础。全局 category 与 runner/runtime 本机 Agent definition 的安全库存基础已接入；hosted main、issue、consultation 的真实受限 runtime 登记尚未实现。提供来源类型、版本、规范化 hash、能力、加载/健康时点、退役状态及 owner 可读原因。

扩展现有 execution profile，不替换 harness。定义解析优先级和实际 run 快照：model、effort、指令、harness、category、definition 版本/hash 固定；凭据只在本机引用。旧请求保留默认行为，新增能力不足不能静默忽略选择。

验证多 category、重复身份/版本冲突、去密、更新只影响未来、退役不改历史、原快照恢复、缺 runtime/snapshot 明确受阻；category 不授正式角色。真实本机只读核验 CLI/模型可用性，与业务 run 测试分开。

## P3：按 category 和硬约束调度

依赖 P1/P2，实际调用与验证见[执行计划](category-agent-execution.md)。扩展纯函数 scheduler 和领取事务，输入包含 category、可选 HARD runner/runtime、每 role model/effort 筛选、workspace host、实现/验收可用配置。当前 task/spec 首次 named 分配及 start-over 后新资源必须在同 host/backend 同时有双方候选，只冻结当前 run；验收到分配时再选择。同 run 恢复不重选、不跨 host 搬 workspace。实际 factory 使用 ExecutableRef，LocalReference 仅来源元数据；多个 runtime 共用全局槽位与唯一 workspace 协调器。私有 instructions 经真实适配器注入，验收会话/凭据/上下文隔离保持。wire 1/0 通过 capability/条件 ack 增量接入，server migration 57、journal 12，旧 reader 拒绝新执行状态。

为不支持 category、runner、模型、验收、workspace 和离线 runtime 返回明确 queue reason。新增通信先评估 breaking；需要升协议时同批实现 N+1、保留 N、退役 N−1 并更新登记/拒绝测试。

验证硬约束交集、只有实现能力不领取、同 host 不同角色 Agent、候选稳定、并发领取、能力丢失、恢复、旧 runner 协商及现有验收选择。Fake runner 和真实 harness fixture 证明固定选择及两角色配置正确，不部署。

## P4：Issue scoped 自主协调

具体实际接点与范围见 [P4 具名托管 Agent](hosted-issue-agents.md)。候选基于正式 P3 r2，追加 migration 58；具名配置是唯一设置源，canonical 管理执行缺失冻结 selection 时阻塞，不退回旧 D111。

依赖 P1–P3。演进 background pool 和每项逻辑会话；manager 使用主 Agent 同等原生执行隔离，按 issue scope 扩展 Tend 工具，不放开全局白名单。

允许来源审核、定义/计划修订、子项/task 委派、completion review、attention 及范围内继续推进。全局主 Agent 规划优先级和期限，不是每次本项写入的必经环节。持久化 coordination execution/effect_seq，独立保留 owner source、native identity 和 assignment epoch；调整 D119 的来源/回执/逻辑轮，不允许 call ID 绕过去重。

验证直接 issue owner 指令经审核后派发、兄弟/其他 issue 越权拒绝、child 不扩授权、main/manager 无 native 执行、调查产生 report task、旧 epoch、unknown 回执、来源更正、compact/handoff/restart 和 effect 去重。完成 review 由所属 manager 处理，不全部 escalate 主 Agent。

## P5：Attention、明确唤醒和个人规划

依赖 P4。统一 decision/action/information 义务与原决策来源/版本，独立于 assignee。Chat、Inbox、detail 共用身份；读/下载/归档/普通通知不回答。

扩展 outbox/followup/batch 的明确目标和 epoch：task 到所属 issue、child 到 parent、owner 答复到责任 manager、deadline/跨项冲突到主 Agent。Deadline 带来源/时区/revision，默认前 24 小时及到期，登记已在窗口内只触发一次。Next action 带责任、阻塞、时间。无变化静默，不增加计时器或巡逻 Agent。

验证回答不改 assignee、实际动作/信息结算、等待 worker 经原 decision/version/run 正式回答继续、replace/withdraw、路由去重、未领取批合并、繁忙 manager、交接、重启、unknown、改期/取消/时钟回拨、外部期限/用户承诺/估计及 soft/hard/未知区别、归档不 resolve、提醒正确且不广播无变化状态。无 active run 的纯 manager/规划 attention/followup 等待无 worker slot；故障测试验证活动 worker 等待、human_control、unknown 保留原 task/run/decision 身份和按原状态规则的租约、workspace、资源占用，不被规划等待逻辑释放。

## P6：直接聊天、咨询和手动来源

依赖 P4/P5，run supplement 使用 P3 固定身份。提供 main、issue manager、精确 active run 和独立 consultation 会话，owner 原文/附件按目标保存。

Issue owner 消息直接进入其 manager；run 补充沿 D116，owner request 去重不伪造 main commit。普通 consultation 无 issue/task、无实际执行；显式 promotion 创建/关联 issue，冻结所选事实并派 task，以持久 effect 防重复。终态 run 只读，新执行是新 task/action；验收不获取实施 rationale。

手动 source 注入提供原件、稳定 key、时间、更正，无飞书接入。原生入口保留 Orca controller 和 owner-only takeover/handback，查看无副作用；Cove 只定义接口和契约测试。新公共 API 就绪后退役活动旧写 facade，旧 UI 在 P7 cutover 前继续使用明示兼容读接口。

验证四类聊天身份隔离、直接授权、误投拒绝、supplement 终态/unknown 不重放、普通咨询零业务对象、promotion 并发重试只一次、模板不授 acceptance、来源更正、查看不接管和接管后回交。

## P7：经确认的 UI

用户已确认主界面及剩余 02–13 全部十二张黑白设计稿。按已批准页面分组实施，不重复请求这组确认；新增或明显改变布局、信息结构和交互流程时重新出图、独立 review 并由用户确认。CLI、API、领域逻辑及前端数据适配继续独立推进。

UI 实现沿用 frontend-app-builder 与 Image Gen 已形成的设计，保留实际现有黑白风格：白色内容、黑色文字、浅灰侧栏和细边框。已否决的概念稿不作为实现依据。实现后以真实桌面和移动端浏览器截图逐张对照已确认设计，并使用 view_image 检查，记录差异和修正；代码 review 不替代设计确认。

确认后复用 detail overlay、会话、rollout 和状态组件，按生成 API 类型实现。展示 issue 责任/执行角色、队列/实际运行、待确认/unknown，直接聊天不强制跳主 Agent。管理 Inbox 和全局 next action，不以机器实现细节替代用户决策。新 UI 切换后移除活动旧读 facade，历史档案读取保留。

验收键盘路径、窄屏、树导航/返回/滚动、深链接、并发更新、刷新/消息恢复、attention 同身份、只读档案及无副作用原生入口。执行相关前端 skills 和真实浏览器检查；fake API 或截图不能代替真实账本交互。

## P8：离线归档与空账本工具

可在 P1 后开发，最终演练依赖 P2–P6。新增专用 snapshot/export/reader，不调用正常 store.Open，不开 writer、不迁移、不设 WAL、不建 blobstore/服务循环。固定版本静态导出加原始 SQLite snapshot、文件、报告、rollout 和 manifest，记录覆盖与缺失。

私有切换清单：冻结领取及 owner/manager chat、control、source、settings 和后台新业务写入准入，排空已接纳请求与后台/执行，核对 unknown，只允许必要 maintenance/runner 回执沿受控通路结算，最终停止全部 writer 后取一致性 snapshot，校验 hash/引用、预演档案读回、显式静态设置导入、新 deployment identity、隔离 runner journal/凭据、运行与回退材料。验证完成后显式恢复写入和领取，不能丢失已确认请求。机器目标、路径、账号和证据只存 `.local/`。

验证档案前后字节相同，无 WAL/SHM/目录副作用，无写入/自动唤醒，owner 认证或本机权限保护、匿名读取拒绝，原件独立下载回比，原 acceptance/transcript 可读且采集缺口诚实、未知作者不能授权，新业务为空，旧身份请求/回执拒绝。故障测试让 owner/manager chat、control、source、settings 写入与切换并发，断言冻结后拒绝新准入、冻结前已接纳写入全部包含于最终 snapshot、仅必要维护回执可排空、最终全部 writer 停止，验证前不会恢复领取或写入。Unknown 阻止 cutover；新账本已有外部副作用时 rollback 先对账，不能直接换 DB。P8 不删除原迁移、历史 reader 或未核清资源。

## P9：完整发布与真实 E2E

依赖 P0–P8，含 P7 确认和实现验收。每批独立 PR、make check、独立 review、CI；大改重审。最终 rebase 最新 main 线性合并，禁止 merge commit/squash 和直接推 main。中间批次不部署；最终受控发布证明 server/runner 协议、实际库存、程序/配置身份、档案 hash 及新账本身份。

真实验收使用隔离且明确授权的目标，至少包含：

1. 模糊目标经 main 建 issue，manager 委派调查、更新定义和 child/task，正式独立验收交付；main/manager 无实际执行。
2. Owner 直接 issue 指令、active run supplement、独立咨询零 issue/task、一次去重 promotion；验收无实施 rationale。
3. Attention 答复继续原责任、child 到 parent、deadline 到 main；manager 等待静默且不占 worker slot，活动 worker 继续原租约/资源语义。
4. 重启后 wakeup/固定配置恢复，Orca 查看/接管/回交，unknown 不重放。
5. 档案原文/附件/验收可读且无写入，新账本无历史业务，旧客户端/runner 无法重放。
6. 同时存在两个独立 issue：各自 manager 仅推进自身/child/task，互不覆盖定义、来源、epoch 或执行；main 能比较优先级、期限并跨项规划，局部 owner 指令不等 main 批准。
7. 同 category 在不同 runner/runtime 存在多个真实候选：稳定选择、显式 HARD runner 和 workspace host pin 分别生效；被选 host 同时满足实现/正式验收，实际模型与 effort 可从冻结快照和原生运行证据回读，恢复不因目录更新而换配置。
8. 在实际浏览器可见 Inbox 的原始手动来源、待答 attention 和 Next action，完成答复或计划调整后读回同一账本身份与版本；刷新/重启后状态和阅读路径一致，无变化无额外广播。

记录真实对象、事件、请求、Agent snapshot hash、task/spec/run/generation、acceptance、版本与发布证据；区分已合并、已安装、已启用和独立验证。构建/fake harness 成功不等于 live E2E 通过。

## 每批共同门槛

实施前更新本文状态与对应决策，记录实际接口、兼容评估和剩余问题。每个 commit 原子可构建，提交信息不含测试结果或私有环境信息。执行 make check（协议退役、格式、vet、staticcheck、race tests、Web 全套），独立执行仓库要求的 xhigh review，逐条处理意见并在 PR 说明理由；CI 通过才合入。

独立评审命令固定为 `codex review --base main -c model='"gpt-6.1-sol"' -c model_reasoning_effort='"xhigh"'`，同时记录当次 main 的固定 SHA、实施 head 和实际评审会话身份。CI 除 make check 外必须通过仓库要求的 Windows/macOS 交叉编译。Rebase 后若差异实质改变则重跑相关检查和独立评审；不将旧 head 的通过状态移用于新 head。

验收至少覆盖领域状态、app 回滚/去重/冲突、角色/作用域、run 恢复和 unknown、schema 一致性、真实前端、档案无副作用。故障测试使用 TestFault 命名，真实 harness 使用 integration tag 显式执行；单元测试不联网或调用模型。

P1b1 发布链路补记：[PR #159](https://github.com/GhostFlying/tend/pull/159) 在 head `16c415306b8df659c2cec94dd49f1f84428dd786` 的 CI `40771577330` 全通过（含浏览器与 Windows/macOS 交叉编译），rebase 合并至 `7cb9966965e2b184676a831cef3073d28a89a1ee`。本轮 owner 公开组合 r2 完整检查与独立复核通过，证据见 [组合计划](issue-owner-delivery.md)，其 PR/CI 尚待后续。

P2a 当前 PR 状态补记：[PR #160](https://github.com/GhostFlying/tend/pull/160) 的 base 为 `7cb9966965e2b184676a831cef3073d28a89a1ee`，head 为 `84d2c1e7d926caf7ee180ba6bbc07ed7a8cf6f9f`；CI 待完成，尚未合并。P3 本批从已完成 P1 组合的 `964167d50923fd5ce5053e1187c9624f5540856f` 纳入 68 文件候选，正式组合定向 race/vet、schema/TS 生成与类型检查已通过，规定 CLI review 和完整 make check 待执行；CI/合并/部署未完成。

## P3 正式检查与合并进度补记

P3 正式 r2 规定 CLI review 正常退出且无 findings；r3 仅补原 tools schema 的可选 agent_selection，原严格测试不变，独立窄复审通过。r3 完整 `make check TEST_TIMEOUT=60m` 于 UTC 2026-10-28 23:56:24 至 2026-10-29 00:38:14 退出 0（app 2465.547 秒、mainthread 411.145 秒、tools 71.456 秒、Web 全部通过），使用已有本机参数，未修改仓库默认超时；Windows amd64 与 macOS arm64 交叉编译通过。全检日志 SHA-256 `6aa8abb8095508e2fc33d8d52a201cdacfb7d111301c596a26f77f5c0a472613`。首轮格式失败、第二轮 schema 缺项及 40 分钟 package timeout 均保留为失败记录，不记作通过。PR CI/合并、部署和真实模型验收仍待完成。

P2a [PR #160](https://github.com/GhostFlying/tend/pull/160) 的 CI `40781168826` 全通过，rebase 合并至 `b66a94838fc045f317be1cc819d1090d0d486588`。P1b2a [PR #161](https://github.com/GhostFlying/tend/pull/161) 的 CI `40790535737` 全通过，包含浏览器回归和 Windows/macOS 交叉编译，rebase 合并至 `c806b859a8a39cf0c72165b9d135f26667f126e1`。既有历史 base/head 和过程记录保持，不以本地候选通过替代其他批次 CI。

P4 正式纳入补记：固定候选 r4 patch SHA-256 `3ac992986f6ba8dc380072e8418c3b5fc4d572fa893b63df7717fdc40db292d4`，59 文件；三项修正已独立复审通过。正式前件为 `c69c66b2be9b49aa1fbe824d9ea6283bec5dc880`；本次保留 P3 完整检查、上游 CI/合并记录及其 tools schema 修正，仅合并最新状态文档，不改变已预审的运行代码。正式组合检查与规定 CLI review 待完成。

正式第二轮冻结补记：r1 完整检查自然结束为 exit 2，App、mainthread、tools、store 通过；API 的过时 assignment 断言及两项后台身份适配 E2E 失败已分别修正，Web 因 Go 阶段失败未运行。r5 六项修复和 E2E 四文件增量独立复审均无新增 findings。最新前件 `3ec40b016a28f2e0ae1a7c2b2528698520d58c9c` 与原前件的源码树一致；本轮仅更新前件提交身份和进度记录，不覆盖 r1 失败事实。第二轮规定 CLI review 和完整检查结果另行补记，不将定向通过写成全检通过。

正式第三轮准备补记：r2 规定 CLI review 正常退出但提出四项 P2，均采纳并形成 r6 九文件修正。独立复审确认 owner assignee 来源绑定、交接时已明确未发送旧回合退役、named 工具前的 steer 来源回执以及维护摘要冻结模型均按实际 caller 修复；四条旧代码红例与新代码定向 race 保留。r2 全检在运行中主动中止以检查修正版，属于未完成，不作为产品失败或通过证据；当时 API 已通过，Web 未执行。r2 Windows/macOS 交叉编译通过仅归属于其冻结候选。r6 完整检查与规定 CLI review 另行记录。

P1b2b PR #163 的 CI `40800622561` 全通过，包含完整检查、浏览器回归与 Windows/macOS 交叉编译；rebase 合并至 `cae8f956fd177cf90abd21d6b737c802e127b0ca`。P4 第三轮前件更新为 `cd6ac785c44876ce08dd102c14cc3e2c9f9da8d5`，与先前前件的源码树一致，既有检查证据仍保留原提交身份。

P2c–P2d PR #164 的 CI `40805226503` 于 UTC 2026-10-29 02:59:23 全通过，包含完整检查、浏览器回归与 Windows/macOS 交叉编译；rebase 合并至 `2210f315a62efb49a28662a69dbc074110481fd7`。P1 owner API/CLI [PR #165](https://github.com/GhostFlying/tend/pull/165) 的 base 为该提交、head 为 `4f66351444b127026ad99f9cef7895bf31434755`，CI 待完成。P4 新前件 `e159e70e22b0f36a8e72d7e7a7985fd4acd6a1f6` 与原前件源码树一致；本次不改变已检查的前件实现。

P4 第三轮完整检查于 UTC 2026-10-29 02:14:00 至 02:58:35 自然退出 2：App 2647.905 秒，两条负责人交接回归因旧普通投递仍 sending 而失败；API、E2E 及其他 Go 包通过，Web 未执行。日志 SHA-256 `96deb4efcf00e1d386a97e8947c84ec5a8ddc782e9e5d9e9d7a96237acbf8908`。本轮正式 CLI review 提出的重挂领取窗口与父负责人读取后代成果问题已由 r7/r8 修正、定向检查和独立复审通过；两条完整检查失败仍另行修正后再正式验证，不将定向通过当作全检通过。

第四轮正式验证准备：r8 的首次普通投递重挂围栏及后代成果读取均已独立复核；r9 仅修正两条测试的实际投递收束顺序，保留 sending 时拒绝交接和原有收尾断言，生产代码不变。r9 单文件独立复核通过。当前前件 `e159e70e22b0f36a8e72d7e7a7985fd4acd6a1f6`，正式组合为 66 文件；规定 CLI review、完整检查及 CI/合并仍须分别完成。


## 第六轮正式验证准备

P1 owner API/CLI [PR #165](https://github.com/GhostFlying/tend/pull/165) 的 CI `40809780053` 第二次尝试全部通过（包括浏览器回归与交叉编译），rebase 合并至 `b7d9a3c229b5b8b26a257ad6b2dfa24322cc6daa`。首次仅暂停接管回归超时，原 head 的该用例三次定向 race 通过；未确定根因、未修改测试，不宣称已修复。P3 [PR #166](https://github.com/GhostFlying/tend/pull/166) 从此 base 提交，head `a690deb806b0800500aadc50edd5886fc8e4b27d`，CI 尚在运行。其源码树与原已验证提交相同。

P4 第四轮完整检查于 UTC 2026-10-29 03:56:56 自然通过，但该轮规定 CLI review 的四项实际缺陷另经 r10 修正和独立复审。第五轮规定 CLI review 正常退出并提出两项 P2：子负责人缺少继承上下文、不可用模型占满后台池；均采纳并由 r11 修正。第五轮完整检查主动中止为 INCOMPLETE_SUPERSEDED，不作为通过或产品失败；交叉编译通过仍仅属于第五轮冻结候选。r11 七文件独立窄审通过，正式第六轮基于上述 P3 head 重新冻结，完整检查与规定 CLI review 待执行。组合候选中归档准备和启动退出通知的交错问题属于后继 P8 接点，单独修正和复核，不提前声明完整依赖链通过。


## 第七轮正式验证与 UI 批准补记

P4 第六轮规定 CLI review 于 UTC 2026-10-29 05:04:33 正常退出，但提出三项 P2，不记为通过：启动前 hook 读取失败误判未知、已冻结普通事件重试绕过同项待核回执，以及终态 canonical Issue 的待投递事件在重启后失去发现。均经实际调用核实并采纳；第六轮全检于 05:08:20 主动中止为 INCOMPLETE_SUPERSEDED，不是通过或产品测试失败。第六轮交叉编译通过仅属于原冻结候选。

r12 补齐上述路径及原 context 取消后的已知未发送清理，保留真正 turn/start 未知、原执行快照、合法恢复、原 batch 和旧历史行为。定向 race 为 TraeX 1.153 秒、App 18.481 秒、mainthread 44.737 秒，vet 通过；固定十文件增量和 74 文件组合已独立窄审通过。第七轮从相同前件重新冻结，完整检查和规定 CLI review 尚待完成。

UI 剩余十二张图已对照当前实际黑白界面逐张独立校对，用户明确回复“全部确认，可以按图实现”。连同此前批准的主界面，现有图稿范围全部进入实现；新增或明显改变布局、信息结构、交互时仍重新设计 review。前端实现与后台字段/薄读取适配分工，复用现有请求身份、读取失效与来源保存机制。

第八轮正式验证准备：P3 PR #166 的 CI `40819277346` 全通过，rebase 合并至 `bfb0ffff3b715ffa9b88a03b4a8e982231bfb414`。第七轮规定 CLI 正常退出但发现 canonical 主会话来源分类与延期队头阻挡冻结续接两项 P2，均采纳；第七轮全检主动中止为 INCOMPLETE_SUPERSEDED。r13 及后继自身回复唤醒/提示词修正均完成真实调用回归与独立窄审；本轮正式组合冻结为 79 文件，规定 CLI、完整检查、CI/合并及部署仍须分别记录。隔离真实账本保留原责任，待新不可变构建恢复，不以人工改分类或清理待决掩盖缺陷。


P4 r14 当前候选：formal r8 独立 review 四项实际 caller finding 已采纳（来源准入、后代验收引用、named owner-source schema、分角色 metadata repair）；r8 本地全检主动未完成，原执行证据保留。r14 在独立副本进行实际 red→green 和定向 race/vet/生成一致性，待独立复审与唯一正式全检；未合并、未部署。后继 B1 stable definition/controller slice 已独立复核，owner metadata 后片与真实隔离验收保持独立，不将候选当线上能力。

第九轮正式验证准备：第八轮规定 CLI review 自然退出但提出四项实际调用缺陷，均采纳；第八轮完整检查主动中止为 INCOMPLETE_SUPERSEDED，交叉编译通过仍只归属该冻结版本。r14 的 queued 来源准入/后继份额、后代验收引用、source selector schema 和角色对应 repair 指令已完成定向 race/vet 及独立窄审。当前 84 文件组合基于 `bfb0ffff3b715ffa9b88a03b4a8e982231bfb414`，规定 CLI 与完整检查另行执行，不以候选预审代替合并门槛。

P4 r15 后继修正候选：第九轮规定 review 的四项实际来源消费/owner 纠正唤醒缺陷已通过正常调用复现，有限入口清单覆盖分类 target、嵌套完成证据、会话 preview、context/task/fact 来源及 owner 旧分类入口。本片沿原准入与事务 fanout 最小修正，独立 review/正式完整检查待执行；第九轮 fullcheck 为主动停止、未完成，不记通过或产品失败。B1 owner 投影、可见 UI 和原真实模型隔离账本保持独立。

第十轮正式验证准备：第九轮规定 CLI review 自然退出，但提出分类目标、嵌套 owner 证据、会话预览的来源准入，以及 owner 显式纠正关联后的负责人唤醒四项实际缺陷，均采纳。第九轮完整检查于 UTC 2026-10-29 07:09:37 主动中止为 INCOMPLETE_SUPERSEDED；此前 API 已通过，App 未结束，不记作全检通过或产品失败。r15 的实际 red→green、定向 race/vet、生成一致性及独立窄审通过，复用现有准入和同事务唤醒规则，不限制用户查看原文或直接整理关联。当前 85 文件组合基于 `bfb0ffff3b715ffa9b88a03b4a8e982231bfb414`，规定 CLI、完整检查、CI/合并仍须独立完成。隔离测试对停滞的 metadata 修复使用了一次明确 owner 中断，并在归档准备与实际执行收束后停止测试服务；原责任和数据完整保留，不表述为自然恢复成功。

P4 r16 后继候选：formal r10 规定CLI的两项准入缺陷已用正常caller复现并修正，定向App/mainthread race与vet已通过，独立review/正式完整检查待执行。冻结manager续接受同Issue普通receipt围栏；main当前配置缺失时继续原合法pin、新输入不抢先准入。第三条blanket子树native endpoint门槛经独立caller核对不采纳：已确认child.close并非业务unknown，原native回执保留对账，父项不能因没有后继wake的纯尾部被永久阻塞。现业务unknown与迟到激活约束不改。

第十一轮正式验证准备：第十轮规定 CLI 自然完成并提出三项 P2；冻结 manager 续接绕过实际 receipt，以及当前 main 配置缺失阻塞原冻结 repair 两项均采纳并修复，第三项要求子树原生会话完全收束的门槛不采纳，原报告与业务调用依据保留。r16 五文件增量、86 文件组合经独立复核；真实正常 caller、普通 receipt 对账与原 foreground 优先的定向 race/vet 通过。第十轮完整检查于 UTC 2026-10-29 08:07:50 主动中止为 INCOMPLETE_SUPERSEDED，API 已通过而 App 未完成，不记为通过或产品失败。当前正式组合仍基于 `bfb0ffff3b715ffa9b88a03b4a8e982231bfb414`；第十一轮规定 CLI、完整检查、CI/合并另行记录。

收口补记：P4 r17 独立复审通过并纳入，第十一轮全检自然失败的实际用例另行诊断；不把定向通过或 CLI 正常退出写成全检通过。UI r2 原四项修复已核实，新增刷新后旧请求版本冲突丢草稿场景由实现 agent 修复；Task 当前 Issue 字段候选已冻结并送独立审阅，活动旧入口退役仍待组合完成。

r18 收口：原全检三组失败已确定复现，局部修正与原真实调用回归通过并独立审过；正式完整检查与交叉编译使用新冻结字节。Task 当前 Issue 投影独立 review 已通过，UI 正在切换其真实调用，未替代活动旧入口退役或部署验收。

## P4 正式完整检查通过

r19 的私有维护摘要修正及实际撤回 CAS 测试经独立窄审通过。正式 r13 在 UTC 2026-10-29 11:03:46 至 11:51:26 自然退出 0：`make check TEST_TIMEOUT=60m` 使用仅为 `go test` 添加 JSON 事件输出的透传入口，测试范围、race、超时及退出码不变。605 个 App 测试、其余 Go 检查、45 个 Web 测试文件/281 项测试、类型/lint/格式和构建通过；Windows amd64/macOS arm64 编译通过。源文件与冻结的 92 路径快照逐字一致，日志 SHA-256 为 `4fc97ee4fd4606fefddc5984fa562e8efecc1f38ffece4d8e975d114f92001df`。

第十二轮未知中断与重试的真实摘要失败保留；本次结果不改写历史。规定第十一轮 CLI review 及后继独立修正审查关闭代码 review，不重复已关闭的全量审查。当前仅追加进度文档，尚待 PR/CI 与 rebase 合并，不声明生产部署或完整 P9 通过。


## P5 正式独立批纳入

从 P4 已提交的 `069289c5edb8da0f390b20407a77370d1961d274` 接续已审 42 路径 P5，原代码、测试和生成件精确保留；只合并本批状态记录。P4 [PR #167](https://github.com/GhostFlying/tend/pull/167) 正在 CI，本片不改写其全检通过、历史失败或独立评审结论。P5 原定向检查及独立复核已通过，本轮完整检查、规定 CLI review、提交/CI/合并和部署尚未完成。不纳入后继期限字段、P6/P8 或最终 UI，以独立可检查批次接续。

P4 [PR #167](https://github.com/GhostFlying/tend/pull/167) 的 CI `40868884929` 已全部通过（完整检查、八项浏览器回归、交叉编译），rebase 合入 `668c8f74ff234dde4ae148d30637339fb4ad1002`。P5 正式首轮 CLI review 的五项实际调用问题已采纳并形成定向回归修正；首轮全检主动停止为 `INCOMPLETE_SUPERSEDED`，原日志保留。修正版本的独立增量复审与完整检查并行进行；尚未部署。

## P5 正式完整检查通过

固定 base `668c8f74ff234dde4ae148d30637339fb4ad1002` 的 44 路径 r2 于 UTC 2026-10-29 13:34:03 自然通过完整检查：41 个 Go 包、App 631 个顶层用例、Web 45 文件 281 用例及类型/lint/格式/构建；Windows amd64、Darwin arm64 全包交叉编译通过。固定源码和索引未变。规定 CLI 的五项 finding 均修复并独立复核，沿用该轮加窄修正关闭 review，不重复整轮。当前只更新进度文档，提交/CI/合并待完成。

隔离组合的真实 Attention 回答和 action 确认已完成，原 worker 实际消费回答。后续关闭引用已确认 Attention 的新消费接点另有窄修正，未计为本批已实现；完整 P9 与生产部署仍未完成。
