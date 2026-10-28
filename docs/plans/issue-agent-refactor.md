# Issue 与 Agent 重构实施计划

状态：P0 文档、独立评审和本地 make check 已通过，PR #157 基线 fixture 修复已合并，PR #155 完整 CI 已通过并以 rebase 方式合并；P1a 内部改名代码、本地检查及独立评审已完成，CI/合并/部署未完成；P1b1 存储/纯规则代码、本地完整检查及独立评审已完成，未启用入口，PR CI/合并未完成；P2a 纯配置基础、本地检查及独立 review 已通过，PR CI/合并未完成；P1b2a 内部定义与 legacy 边界代码、本地完整检查和独立复审已完成，公开入口未启用，CI/合并/部署未完成；P2b 本地存储与 journal CAS 恢复已纳入，本地完整检查和独立 review 已通过，配置缺陷已修复并复核，CI/合并未完成；P1b2b owner 内部生命周期和逐批来源收尾已纳入；正式 review 的迟到 share 缺陷已修正，r2 定向 race/vet 通过，独立复核及完整 make check 已通过；P2c–P2d 本机配置接受、owner Category CLI 与 runner 安全库存登记/恢复链路已纳入，候选独立预审及正式组合定向检查通过，规定 CLI 独立 review 和完整 make check 均已通过；P1b3–P1c 与其余 P2–P9 尚未正式纳入。本文是实施合同，不是发布声明。设计见 [Issue、Agent 与个人规划架构](../issue-agent-design.md)，Refs: D121–D126。

## 进度账本与角色

本轮主协调由 root 单一会话负责；实施 Agent 固定为 GPT-6.1-Sol high，独立 reviewer 为不同会话的 GPT-6.1-Sol xhigh。独立 reviewer 不实施其评审改动，协调者不把实施自评当作独立结论。UI 设计另行经 xhigh review 和用户确认。

| 批次 | 依赖 | 设计 | 代码 | 独立评审 | 合并 | 部署 | Live 验收 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| P0 | 无 | 文档完成 | 无运行代码；本地 make check 通过 | 已通过，澄清增量确认 | PR #157 修复已合并；PR #155 CI 通过，rebase 合并至 `8c8c6ec6` | 本批不部署 | 无 |
| P1 | P0 评审通过 | P1a 子计划已写，P1b 控制分片合同已评审可实施；P1c 待核定 | P1a、P1b1 代码及本地检查通过；P1b2a 内部定义/边界代码、本地完整检查和独立复审已完成，P1b2b 内部生命周期已纳入；r1 CLI 一项迟到 share 缺陷已修正，r2 定向 race/vet 通过，独立复核及完整 make check 已通过；task/manager/公开入口未启用，其余未正式纳入 | P1a/P1b1/P1b2a 独立 review 通过，本次状态记录已独立核对 | CI/合并未完成 | 本批不部署 | 未执行 |
| P2 | P1 身份基础 | P2a/P2b/P2c–P2d 子计划已写 | P2a/P2b 本地检查和独立复核通过；P2c–P2d 本机配置、Category/安全库存登记及恢复入口已纳入，正式定向检查和完整 make check 均已通过；新 Agent 执行/调度未启用 | P2a/P2b 正式独立 review 通过；P2c/P2d1/P2d2 候选独立预审通过，正式组合人工及规定 CLI review 均通过，无 findings | 未合并 | 本批不部署 | 未执行 |
| P3 | P1、P2 | 合同已写，接口待核定 | 未开始 | 未开始 | 未合并 | 本批不部署 | 未执行 |
| P4 | P1–P3 | 合同已写，接口待核定 | 未开始 | 未开始 | 未合并 | 本批不部署 | 未执行 |
| P5 | P4 | 合同已写，接口待核定 | 未开始 | 未开始 | 未合并 | 本批不部署 | 未执行 |
| P6 | P3–P5 | 合同已写，接口待核定 | 未开始 | 未开始 | 未合并 | 本批不部署 | 未执行 |
| P7 | 稳定 API、设计 review、用户确认 | 主界面及现有风格方向已获用户确认；其他页面待单独出图/review/确认 | 未开始 | 未开始 | 未合并 | 本批不部署 | 未执行 |
| P8 | P1 开发，P2–P6 演练 | 合同已写，接口待核定 | 未开始 | 未开始 | 未合并 | 本批不部署 | 未执行 |
| P9 | P0–P8 | 合同已写，发布清单待核验 | 未开始 | 未开始 | 未合并 | 未执行 | 未执行 |

每个批次/子批建立如下证据行，开始下一阶段时填真实值，未发生项保持“待记录”，不能以计划填成完成：

| 批次 | 固定 base SHA | 实施 head SHA | PR / CI | reviewer / 结论 | 部署 build / manifest | Live 对象 / 结论 |
| --- | --- | --- | --- | --- | --- | --- |
| P0 | `7761e61c0e9acdb0209fddff7f5d0e55830b4c65` | 原评审 `5b20a8ad4536cb70341607066452a07e487a702e`；rebase 本地 `e11f326da321121cb85f7fa3186532b8c2b8506c` | [PR #155](https://github.com/GhostFlying/tend/pull/155)；本地 make check 通过；基线修复已随 PR #157 合并；head `02ea061f` 的 CI `40757992209` 通过，rebase 合并至 `8c8c6ec660de59e2cbe48a0ce3db9d8f9a119fb5` | GPT-6.1-Sol xhigh 通过，澄清增量已确认 | 不适用 | 不适用 |
| P1a | `5b20a8ad4536cb70341607066452a07e487a702e` | 原评审 `d52d093741da41e78dc60effb0493a67058eded5`；rebase 本地 `71ae095858626acae6a9ad5e5450724c0e4ea936`；冻结实现快照 SHA-256 `b94229bf4efe0dc91932358af1091004951c7b101be73442fb3dc288863b218f` | 本地 make check 通过；PR/CI 未完成 | GPT-6.1-Sol xhigh 无 findings；后续仅状态记录增量 | 未部署 | 未执行 |
| P1b1 | `242ff5200c7bda28c70de3c313ffef7a55d64717`（含独立基线测试修复） | 原评审 `9b7b85530b65dc2516425c8e7a131d762ead08a1`；rebase 本地 `07effd5231375a31460eb384682e65969b062fd7`；r2 实施快照 SHA-256 `8f99a5d2fbb478ce8a28fec78312a9cdff8a4549d4255e5fa99b867d4d2c06c6`；入口未启用 | 首轮完整检查因 schema fixture 上限失败；修复后定向升级/维护 race、第二轮 make check 均通过；PR CI 未完成 | GPT-6.1-Sol xhigh 首轮及 r2 增量均通过，无 findings；随后仅本次台账增量 | 未部署 | 未执行 |
| P2a | `9b7b85530b65dc2516425c8e7a131d762ead08a1` | 原评审 `e2929e1f4c2cf170e24dbc580cf0f31b39289ab0`；rebase 本地 `3fcc88b37ae2c71971dfd7c38facb8652037ed64`；该片纯配置包当时无生产调用点，后续加载/登记见 P2c–P2d | 定向 race/vet 与本地 make check 通过；PR CI 未完成 | GPT-6.1-Sol xhigh 人工及规定 CLI review 通过，无 findings；随后仅本次状态增量 | 未部署 | 未执行 |
| P1b2a | 原评审 `ffd0cf92510fce66496b81bef8c676ad08639263`；rebase 验证 `3fcc88b37ae2c71971dfd7c38facb8652037ed64`（tree 一致） | 已提交 `2edfda826ea247ea46d7a4cca121d5afb3f6d36d`；r3 实施 patch SHA-256 `cb0a8786e5670d91e83e6e0b9391e0bd0b3e458a641daf9792f36420ae59078c`；随后仅状态文档增量 | 定向 race/integration 编译及 r3 完整 make check 通过（app 2016.963 秒、mainthread 388.341 秒、Web 全部）；过程证据见子计划；PR CI 未完成 | r1/r2 findings 均采纳；r3 独立复审、本次状态记录窄范围核对通过 | 未部署 | 未执行 |
| P2b | `fdd81c682875bfeeefceb2dfc1a9405c6acdae99` | 已提交 `1279b4748bc006dc286cc58484926b5d2bd1889f`；本地配置存储与 journal 11 CAS 恢复，该片未接公开/生产调用；后续入口见 P2c–P2d | 定向 registry/journal race/vet 及发布兼容回归通过；r1/r2 完整检查及修正定向 race/vet 通过；PR/CI 未完成 | 正式 r1 review 一项 typed 空列表缺陷已修正，r2 独立复核通过 | 未部署 | 未执行 |
| P1b2b | 原 `1279b4748bc006dc286cc58484926b5d2bd1889f`；rebase `ae891c455031fca1098ff26ff2c06d3530c51683`（tree 相同） | 已提交并 rebase 为 `2ed83de7944773d5a0a416666bcb35884c62fe56`；六个 app 文件匹配原已复审候选后，正式 CLI 迟到 share 修正已纳入；无 task/manager/公开入口 | r1 定向 race/vet 通过（app 71.361 秒）；r1 完整检查为已知 finding 主动未完成；r2 定向 race/vet 通过（81.205 秒）；r2 完整 make check 通过；PR/CI 未完成 | 原候选复审通过；正式 r1 CLI 一项迟到 share 缺陷已采纳修正，r2 独立增量及正式组合复核通过 | 未部署 | 未执行 |
| P2c–P2d | `2ed83de7944773d5a0a416666bcb35884c62fe56` | 本批纳入本机配置/Category/安全库存登记和恢复入口；无 factory/调度/hosted 可用性 | 候选及正式组合定向 race/vet、roots 生成/类型检查和完整 make check 均通过；PR/CI 未完成 | 三片候选及正式组合人工/规定 CLI review 均通过，无 findings | 未部署 | 未执行 |
| P1b2b–P9（其余逐批追加） | 待记录 | 待记录 | 待记录 | 待记录 | 待记录 | 待记录 |

本次 rebase 保留历史评审 SHA/实施快照，以上本地新 head 不等于 PR 已更新或 CI/合并已完成。P0 先前 CI 因 idle 摘要 fixture 失败；独立修复 `315f0c5f` 已经 [PR #157](https://github.com/GhostFlying/tend/pull/157) 合并。PR #155 rebase 至 `7435b030` 后的[新 CI](https://github.com/GhostFlying/tend/actions/runs/40757992209)已通过（包括浏览器回归及 Windows/macOS 交叉编译），PR 已 rebase 合并至 `8c8c6ec6`。P1b2a r3 完整检查时段 UTC 18:28:06 至 19:02:18，退出 0；日志 SHA-256 `3bfe1500d1f55cbe7c4ea45d7ab14a77792c18268ca8eb6cb3ae5d4d54905801`。P1b2a、P2b、P1b2b 已提交；本片在最新 main 之上的 P1b2b 完成基线上纳入已预审的 P2c–P2d 配置/库存链路，不带入 P1b3/b4、factory 或调度。

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

P1c 增加 owner API/CLI、views 与生成 schema/type 胶水，提供准确 revision、epoch 和阻塞原因。旧活动 facade 的调用点和移除批次登记在本计划，最终 P6/P7/P8 cutover 就绪后移除写入口；历史 reader 不退役。涉及通信兼容的 facade 同时登记协议退役版本。P1a/P1b/P1c 分别是可构建、可评审 PR，不混入未经批准的可见 UI 改动。

必须验证：父子/依赖无环、单责任人、唯一 primary issue、可选 stage、revision 冲突、pause 阻新派发且活动合同继续、取消的正式协调、关闭/重开、reparent 拒绝活动 turn/执行及授权冲突、unknown 阻止伪关闭、旧 epoch 无控制权限。Assignment 按围栏旧写、核对旧 turn/unknown、启用新责任的顺序生效，不接管 worker 或改 spec。完成条件为 API/CLI 可创建/修订/委派/读回，领域和事务测试、旧执行回归、make check 通过，自动推进未启用。

## P2：本机 Agent 库存与冻结配置

P2a 的纯配置校验、身份与公共投影见[独立子计划](agent-definition-config.md)，纯配置基础及本地检查已完成；P2a 本身不接加载或登记调用，后续入口见[配置与库存控制计划](agent-inventory-control.md)。定义仍 staged/unavailable；真实角色指令注入、原 runtime/snapshot 可验证恢复、同 host 全局 workspace/backend 协调和实际执行能力仍待后续，不以纯包或登记测试替代启用门槛。

P2b 的本地配置持久化和通用 journal CAS 恢复见[本地存储子计划](agent-registry.md)。本片追加 journal 11，旧请求 NULL 不回填；本地快照、成功 receipt、CAS 和 unknown 恢复不等于 server 库存或 runtime 已启用。定向 race/vet 和发布兼容回归及 r1 完整检查通过，正式 review 的 typed 空列表缺陷已修正并通过小包回归、r2 独立复核与完整检查；CI/合并未完成。

P2c–P2d 的[配置与库存控制计划](agent-inventory-control.md)接通固定来源严格 loader、原私有 socket 本机控制、owner Category CLI/API、条件 hello ack、journal 原请求登记与重启对账。公共 DTO 唯一定义于 protocol；server 仅保存版本化安全投影与原子回执，不接收本机私有配置。入口已纳入，状态区分本机 local_accepted、server registered_staged、unknown 与 runtime unavailable；候选预审不替代正式组合检查。该批不加载新 Agent factory、不改变旧 worker/model 缺省、不启用 run 选择或调度。

后续仍依赖 P1 身份基础。全局 category 与 runner/runtime 本机 Agent definition 的安全库存基础已接入；hosted main、issue、consultation 的真实受限 runtime 登记尚未实现。提供来源类型、版本、规范化 hash、能力、加载/健康时点、退役状态及 owner 可读原因。

扩展现有 execution profile，不替换 harness。定义解析优先级和实际 run 快照：model、effort、指令、harness、category、definition 版本/hash 固定；凭据只在本机引用。旧请求保留默认行为，新增能力不足不能静默忽略选择。

验证多 category、重复身份/版本冲突、去密、更新只影响未来、退役不改历史、原快照恢复、缺 runtime/snapshot 明确受阻；category 不授正式角色。真实本机只读核验 CLI/模型可用性，与业务 run 测试分开。

## P3：按 category 和硬约束调度

依赖 P1/P2。扩展纯函数 scheduler 和领取事务，输入包含 category、可选 HARD runner/runtime、workspace host、实现/验收可用配置。首次 host 候选必须同时支持两个正式角色；稳定选择并固定快照，恢复不重选、不跨 host 搬验收。

为不支持 category、runner、模型、验收、workspace 和离线 runtime 返回明确 queue reason。新增通信先评估 breaking；需要升协议时同批实现 N+1、保留 N、退役 N−1 并更新登记/拒绝测试。

验证硬约束交集、只有实现能力不领取、同 host 不同角色 Agent、候选稳定、并发领取、能力丢失、恢复、旧 runner 协商及现有验收选择。Fake runner 和真实 harness fixture 证明固定选择及两角色配置正确，不部署。

## P4：Issue scoped 自主协调

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

用户已确认主界面的现有风格方案；该确认仅覆盖主界面设计和风格方向。其余五张页面方案暂不确认，按用户指定的工作日统一确认，在此之前不再请求类似确认。其他页面仍须独立设计、review 与用户确认；当前不编码可见布局和交互，待对应 API 与设计就绪后实施。CLI、API、领域/存储/权限/调度以及不引入新 UI 决策的生成类型和数据状态逻辑可独立推进，不依赖 P7 审图。

UI 编码前必须读取 frontend-app-builder skill，检查现有页面并保留当前蓝灰中性色风格和导航基础，再使用 Image Gen 形成主聊天/Inbox、Issue 树与详情、Agent 库存、直接聊天、attention 和档案入口设计；独立 xhigh 设计 review，修订后请用户确认。主界面以本次已确认的现有风格方案为准，其他页面尚未批准；先前被否决的概念不能作为实现依据。确认记录是 UI 编码前置条件，总体实现授权不能替代该明确停点。后端和无 UI 验收可继续。

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
