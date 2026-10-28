# Issue、Agent 与个人规划架构

状态：设计已确认，P1a 内部命名整理已完成本地检查与独立评审，P1b1 存储/纯规则及 P2a 纯配置基础已完成本地检查与独立评审，P1b2a 内部定义/边界代码、本地完整检查和独立复审已完成，P2b 本地配置持久化及 journal CAS 恢复已通过本地完整检查和独立复审；P1b2b owner 内部生命周期及逐批来源收尾已纳入；正式 review 的迟到份额缺陷已修正，r2 定向 race/vet 通过，独立复核及完整 make check 已通过；上述均未启用产品入口，task/manager 及其余行为尚未正式纳入，CI/合并/部署未完成。本文规定 D121–D126 的目标行为；现有工作项、后台会话和 runner 能力只作为可复用基础，不代表以下功能已经发布。实施顺序及验收见[实施计划](plans/issue-agent-refactor.md)。

## 1. 职责与领域对象

主 Agent 是用户默认的个人助理和全局规划者：接受模糊目标、整理 Inbox、创建或委派 issue、安排跨 issue 优先级和期限、沟通结果及必要决策。它不进行任何实际执行，包括源码调查、日志分析、命令、实现或验收。

每个 issue 有一个具体责任人，可以是 Agent 或用户。Issue manager 负责目标澄清、定义、计划、子 issue、任务委派和结果推进，在继承授权内自主行动。它与主 Agent 一样关闭原生执行入口；调查也通过 report-only task 完成。主 Agent 保留全局规划权，不成为 issue 内 owner 直接指令的审批瓶颈。

本产品信任唯一 owner 及其控制的主机、本地配置与数据库，不防 owner/root 主动修改，也不建设多租户或恶意本地管理员隔离。业务角色/对象权限由服务端控制，CLI 原生工具限制按适配器实际能力落实；main/issue/consultation 禁原生执行不能仅靠 prompt，worker 沿现有 CLI 权限执行。不额外承诺同 OS 用户下恶意进程隔离，不在本轮新建 OS 沙箱或凭据体系。冻结任务、独立验收、agent 作用域和外部资料不自动授权仍是必要边界，重启、重复、迟到、并发与 unknown 按正确性处理。owner 显式引用其他 Issue 的真实消息是资料引用，不转责任、不改原作者，也不需原消息 request/event 的真实性证明链；agent 自动来源准入在其实际入口独立校验。

Task 是一次有冻结合同、交付及独立验收的执行；run 是 task 中特定正式角色和代次的一次运行。Task 完成不代表 issue 或父 issue 完成。“没有活动 task”、用户静默或模型回合结束都不是责任终点。命名 Agent 的类别和名称不授予正式验收或 owner 权限。

Issue 和 sub-issue 使用同一实体及 API，以可空的 `parent_issue_id` 构成树。父子关系不是任务依赖；显式依赖单独表达，均检查环。Issue 可从模糊意图开始，定义包含目标、问题、约束、未知项、来源和完成条件，随调查结果修订。可选 stages 只是 issue 内的计划表达；需要独立责任人或生命周期时创建 child issue。

| 对象 | 必须持久化的内容 |
| --- | --- |
| Issue | 身份、父项、项目、标题、意图与定义、计划、来源事实、revision、状态、优先级、期限、下一步、责任人及 assignment epoch、授权来源、关闭证据 |
| Issue revision | 不可变定义/计划快照、理由、真实来源、请求及协调 effect 身份 |
| Assignment | 具体 Agent 实例引用或 owner、epoch、交接来源、前任/后任、状态和生效点 |
| Task | 唯一 primary issue、冻结 spec、可选 stage 关联、交付要求、实现及正式验收选择、workspace 合同 |
| Run | 正式角色、动作/代次、租约、实际 runtime/Agent 配置快照、原生执行身份、结果与不确定状态 |

每个 task 恰有一个 primary issue；其他 issue 只能显式引用结果，不能成为第二执行归属或隐式共享 workspace。Task spec、artifact、acceptance 与可编辑 issue 计划分离；定义修改不能覆写已启动 run 的合同或历史证据。跨 issue 使用提交、文件或报告引用，保留工作区独占和同责任归属约束。

Issue 支持 create/update、pause/resume、cancel、close/reopen、reparent 和 assign。关闭要求完成条件和责任终点有证据，且没有未处理子项、活动 task/run、未答 attention、未决控制操作或 unknown。父项不能通过关闭、取消、重挂载或删除 stage 遗失子项与承诺。Pause 只阻止新派发，已启动的冻结合同继续；要停止活动执行必须另有明确 task 控制。Cancel 先登记意图并明确处理子项及执行，沿正式 task 控制协调；无法证明停止时展示 pending/unknown，不能伪造已结束。Reopen 恢复 issue 责任，不复活终态 task/run。

Reparent 必须没有活动责任 turn 或执行，再在一个事务中检查树环、依赖和项目/授权范围；活动执行、授权冲突或 unknown 均阻止生效并报告具体原因。它不能扩张授权，不能把原 workspace 改成另一责任的共享目录。计划引用变化不替代明确 task disposition。

## 2. 责任、授权与协调身份

一个 issue 同时只有一个 assignee。提问、等待用户信息或动作不改变 assignee；实际转交责任才执行 assignment。交接先推进 epoch 并围栏旧写入，再核对旧 turn/未知操作，最后启用新责任人的控制权；交接中不能同时有两方控制。保存定义、来源、未完成义务和在途执行。旧 epoch 的 turn 可提交事实，但不能再控制 issue、派发执行或完成当前责任。未知原生操作须对账，交接不构成重放许可。

主 Agent 安排全局优先级、期限及责任；issue manager 在自身范围修订定义、创建子 issue/task、处理结果和 attention。Owner 在 issue 直接聊天中的准确原文可由该 manager 审核并成为授权来源，不要求主 Agent 重复批准。授权继承只能缩小，不能通过 child、类别、指令模板或直接 worker 消息扩大。用户拒绝、撤回、更正和关联修订继续使旧来源失效。

业务 effect 使用持久 `coordination_execution_id + effect_seq` 去重，来源消息只作授权与审计，不能成为每次新 native call 可重置的序号空间。源消息、协调 execution、native thread/turn、assignment epoch、预期 issue/context revision 分别保存。换 call ID、压缩、重启或收到补充消息均不能重复产生同一 effect。业务状态、事件、请求记录和 outbox 同事务提交。

主/manager 工具权限同时在服务端和工具目录实施，不能只靠提示词，或把 background principal 加进全部 planners 白名单。作用域为自身 issue、允许委派的子项、唯一归属 task、必要来源和 attention；全局改序交主 Agent。查看历史和证据不授予验收提交、代答 owner 或控制其他 issue 的权限。

## 3. 命名 Agent、类别与 runtime 库存

Category 是全局能力分类，不是正式角色。每个 runner/runtime 可登记多个本机 Agent definition，每个 definition 可属于多个 category。Hosted main、issue manager 和独立 consultation runtime 同样进入库存，不将可选项局限于 worker runner。

Definition 包含稳定身份、runtime/runner 身份、类别、角色指令、model、effort、支持能力、配置版本及来源。配置来自用户本机 CLI/Agent 设置；只上传允许的非密钥元数据、来源类型、版本和规范化内容 hash，不上传 token、私钥、完整环境或认证文件。路径等机器细节留本机，公共文档与提交不记录真实部署信息。

库存表达已加载、当前可用能力，不只登记候选配置。Runtime 健康、CLI 可用、能力协商和身份验证分别记录；目录中出现一个 Agent 不证明它能在某 host 执行。退役或更新阻止后续新选择，不改写已有 run。

选择条件为 category 加可选 HARD runner/runtime 约束；workspace host 也是硬约束。首次选择执行 host 必须证明该 host 可完成本 task 的实现和正式验收，两个角色使用同一 workspace host，模型或 Agent definition 可不同。正式验收沿用同一 task workspace 身份及冻结的基点/产物合同，验收自己的 scratch 和原生会话仍独立，不能换成另一份未证明等价的工作状态。缺少任一能力则明确排队，不能先实现再迁移验收工作区、忽略硬约束或回退默认 Agent。

候选按持久稳定次序选择，开始分配时固定选择及完整非密钥配置快照/hash。Run 恢复使用原快照和原生身份；更新本机定义只作用于未来选择。快照缺失、能力消失或原 runtime 不可恢复时明确受阻/对账，不静默重选。正式验收仍由 acceptance run、凭据和服务端路径授予，不能由 category 推导。

复用现有 TraeX 和 Orca harness，扩展已有验收 model/effort 选择，不增加 provider。新增通信字段先评估 breaking：仅当能力协商能保持旧有效请求、默认语义和 wire 形状时采用增量；否则提升协议，完整执行 N/N−1 和适配退役约束。

## 4. 所有直接聊天

主聊天保持默认。会话有显式类型及绑定：main、issue manager、精确 active run、独立 named-agent consultation；界面选中状态不能决定实际收件人。Owner 原文和附件持久化真实作者、来源、版本与目标。

Issue 直接聊天由本 issue manager 处理；范围内新指令经来源审核后可更新授权和合同，跨 issue 冲突才通知主 Agent。Manager 对话、调查 worker 原生历史和验收上下文分离，不向独立验收泄露实现设计、自评或偏好。

Active worker 消息复用 D116 的 run-scoped supplement，绑定 task/run/action/spec/generation/control revision 和原始 owner 消息。Owner 可直接发送，无需主 Agent 转述；queued、accepted、unknown 与实际消费分别表达。补充不能修改冻结合同；目标/范围变化由 issue manager 正式处理。Finished run 只读，后续执行使用新 task/action，不能自动 reopen、把补充迁移到下一 run 或重发 unknown。

独立 named-agent consultation 的普通讨论只创建独立逻辑会话，不创建 issue 或 task。管理类和执行类模板都可用于讨论，但会话本身不开放实际执行、正式验收提交或隐式业务权限。需要源码/日志调查、产物、实现或正式评审时，显式 promote 为 issue/task：保存 owner 请求、讨论中选择的事实、promotion effect 身份与关联，事务去重；重复点击、断线重试或 native call ID 变化不创建多个 issue/task。

Promote 可关联已有 issue 或创建新 issue，由对应 manager 准备冻结 task；执行模板在 promotion 后成为 run 配置候选。Reviewer 模板建议不是正式 acceptance。正式验收只取得共享目标、约束、原始证据及实际交付，不把咨询中的 implementation rationale 整段复制过去。原咨询保留独立历史和到业务对象的链接。

## 5. Attention、Inbox、下一步与唤醒

Attention 与 assignment 分离，类型为 decision/action/information，保存 issue、提出者、来源、问题/选项、必要动作或信息、阻塞范围、revision、状态及回答证据。聊天、Inbox 和 detail 展示同一 decision/attention 身份。阅读、下载、归档或 agent 自述不能 resolve；只有明确 owner 答复或可验证实际动作结果沿授权路径结算。若问题来自等待中的 worker，答案必须回到原 decision/version/run 的正式回答通路，继续原 run；不能只结算 attention、发聊天文本或创建 retry 来代替回答。Replace/withdraw 留历史，不虚构用户答案。

Inbox 是用户来源与待处理义务的工作台，独立于现有传输去重 inbox 表。第一版支持手动消息、文件和来源元数据注入，保留来源稳定 key、原件、时间及更正，不实现飞书连接器。Next action 保存动作、责任主体、触发/阻塞条件、期望时间及来源；主 Agent 处理全局排序，manager 推进本项。

Deadline 保留来源、时区和修订，区分外部期限、用户承诺与预计时间，并显式记录 soft/hard 及已知/未知；Agent 的估计不能冒充用户承诺或确定截止时间，也不能默默改期。未显式配置提醒时，默认在已知到期前 24 小时和到期时提醒主 Agent；登记时已进入 24 小时窗口则产生一次相应临近提醒。未知时点不生成虚构提醒，先表达信息缺口。提醒不授权延期、执行或联系第三方。改期、取消、assignment 变化使未领取旧提醒失效；已领取/unknown 留真实结果。

| 事实 | 默认明确收件人 |
| --- | --- |
| Task 实现/正式验收结果、需处理失败 | Task primary issue 的当前责任 Agent |
| Child issue 结果、责任终点或阻塞升级 | Parent issue 的当前责任 Agent；根 issue 按需要通知主 Agent |
| Owner attention 答复 | 发起 issue 的当前责任 Agent，保留原 owner 证据 |
| Issue deadline、跨 issue 优先级冲突 | 主 Agent |
| 直接 owner 聊天 | 被明确选择的 manager、run 或 consultation |

复用现有 outbox、followup occurrence 和 background batch，不引入第二定时器或巡逻 Agent。Wakeup 固定 source event、目标 conversation、issue、assignment epoch 及原因，事务去重；只合并未领取输入，保留全部来源。重启恢复原 delivery，unknown 不重放。没有 active run 的纯 manager、规划 attention 和 followup 等待不占 worker slot；活动 worker 等待回答、human_control 或 unknown 仍遵循原 run 的租约、workspace 和资源占用/对账语义，不能一律释放。无事实变化、不需用户动作时静默，不周期发送“仍在等待”。

## 6. 原生入口与接管

Orca workspace/terminal 入口继续支持，复用 provider/session/terminal 身份、human_control、handback 与 controller 观察。查看链接、transcript 或资源不构成 takeover。实际控制变化和 owner 显式动作才走既有接管协议；接管与回交由 owner 授权，Agent 不能代用户宣称接管。Unknown send、身份变化与缺失观察继续保留占用和对账。

Cove 本次只定义兼容的入口身份、controller 观察、能力、未知状态及 handback 合同，不实现完整 adapter、不声称可用。入口 URL 可打开不证明控制权、run 身份或 workspace 合同正确。

## 7. 旧账本归档与全新业务账本

本设计取代 `docs/plans/mandatory-message-context.md` 中当时采用的删除旧数据、不保留归档/恢复材料的部署安排；历史计划不改写。不把历史 work_item 自动转换为 issue。保留原始 migration 和不可变证据，产出旧账本只读离线档案，新业务账本为空。

只显式导入静态设置，如仓库元数据、host/Agent 分类及允许设置，重新验证本机绑定和可用性。不得导入历史消息、任务、run、承诺、待决、去重域或租约并伪装成新账本业务。旧持久格式在档案中保留，机械代码重命名不暗改历史 JSON 含义。

归档前冻结新领取和新写入准入，拒绝 owner/manager 聊天、控制、source、settings 及后台新业务写入；已接纳请求、run、控制操作、message receipt、观察和后台执行必须排空/核对，不能丢失已确认写入。只允许排空所需的 maintenance 和 runner 结果/对账回执沿受控通路完成，最终快照前停止全部 writer；unknown 未核对则停止切换、保留现场。使用一致性 snapshot，包含数据库、原文件、报告、rollout 和必要索引；manifest 固定格式/schema/build、源 ledger identity、时点、每文件大小/hash 与覆盖缺口。原生 transcript 记录实际采集范围、thread lineage 和截断/未知缺口，不以账本投影冒充完整原生历史。未知作者或身份不成为 owner 授权。独立校验档案、新账本和 runner 身份后，显式恢复写入及领取准入，不能在验证途中自动开放。

档案 reader 永不调用正常 `store.Open`：该入口建 writer、迁移并可能导出 legacy blobs。离线导出器只读取已冻结、自包含 snapshot，以专用只读连接读取，不设置 WAL、不执行 migration、不建 blobstore。静态 viewer 读取固定版本导出对象，原 snapshot 保留供回比；不启动 App、outbox、followup、observer、agent 或维护循环。档案只允许 owner 认证入口或受本机访问权限保护的离线读取，不能把历史正文/附件公开托管；静态表示不等于匿名公开。入口没有业务写入 API，临时访问材料也不写入档案。

新账本生成新 deployment identity；runner journal、lease 和凭据绑定同步隔离。旧客户端请求或 runner 回执不能在新身份重放。切换前保留程序、配置、数据和网络身份回退材料。新账本无外部副作用时可按已核验切换点回退；已有新执行/外部副作用后先对账，不能直接换回旧 DB 导致重复执行。

## 8. 过渡、UI 与发布

先做内部 Go/domain 命名整理，再增加树、assignment 和生命周期行为。机械批次保留旧持久 migration、wire JSON 和旧 UI 行为，生成 schema/types 可作为一致性胶水；不能把该阶段称为公共 API 或产品改造完成。明确标记过渡 facade 及调用点，在新 API、manager、UI 和档案切换就绪后退役活动工作项入口，历史 reader 保留。若过渡涉及通信适配则登记退役版本。

UI 先使用 frontend-app-builder 与 Image Gen 形成设计，经独立 xhigh 设计 review 后由用户确认；确认前不写 UI 实现。API/领域和无 UI 验收可继续。UI 忠实表达真实账本，不以本地标签替代服务端语义。

每批独立构建、测试和 PR，运行 make check、独立 xhigh code review、CI，rebase 后线性合并。中间批次不部署；完整功能、档案/切换演练和真实 harness E2E 就绪后，按受控流程一次部署并做真实 owner 闭环验收。实际机器与发布证据只保存在忽略的本地记录中。
