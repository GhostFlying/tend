# 普通任务按工作要求选择执行器

## 目标与边界

普通模型创建新任务或新责任时表达 category/capability，由系统选择具名执行配置；
implementation 与 implementation-economy 仍由派发者按难度和成本选择。
保留有真实用户来源的明确指派、真实 workspace/host/OS/资源要求，以及系统已冻结的
续接、重试、恢复身份。Agent 可以并发使用，不增加每 Agent 独占状态。
本轮不合并 category/capability，不清理私有配置，不部署或重启。

## 已确认路径

- `internal/protocol/agent_execution.go` 的 `AgentRequirement` 同时承载工作要求和
  agent/runtime/model/effort 过滤，且也被不可变历史和续接使用，不能直接删除字段。
- `internal/tools/hosted.go` 的 `hostedMutationSchema` 从 owner 输入协议生成 named
  工具；`exportSchema` 负责展开供模型阅读，底层有效请求校验和历史 callback 是另一层。
- `OwnerTaskSpec` 另有 `acceptance_execution`；`OwnerTaskRequirements.host_id` 是
  真实资源约束也可能是过度限制的来源，不能全部删掉。
- `prepareOwnerExecution` 会从真实 owner 的保留库存补齐 Agent/runtime/profile/host，
  这是系统维护身份，不能当作普通模型显式指派，也不能破坏同 Issue 的独立协作。
- `IssueOwnerRequest`、request-owner 和 status 的 owner 输入也是新责任选择入口。
- runner 当前协议为 4，保留 3 适配；任何收紧旧有效通信必须按 N/N−1 评估。

## 实施步骤

1. 补全 named、legacy/MCP、owner API/CLI、普通 worker、self-execution、独立审阅和
   续接的真实调用图，核实哪里校验输入以及哪里冻结选择。按实际业务调用者区分
   owner 明确指派、模型新任务要求、系统维护 pin，不能只看同名 source_message_id
   就声称用户指定了具体 Agent。
2. 编码前把确定方案补进本计划：优先采用明确的普通工作要求输入与系统/人类选择
   通道，避免让普通 schema 暴露一组可随意填写的硬过滤字段。检查所有嵌套入口、
   acceptance_execution、owner 选择和 host 约束；不能只改一个描述字符串。
   不机械删除 AgentRequirement 的历史字段。若仅增加当前发现面投影且保持旧 wire
   有效性，说明为何不需协议升级；若收紧旧请求或默认语义，实施完整协议升级、
   N 适配、N−1 退役与拒绝测试，不能静默忽略旧有效字段。
3. 同步适用的协议类型、生成 schema、CLI 完整输入/帮助、能力目录和设计决策。
   用户指派引用必须保留真实来源；不能让模型通过自行声明 provenance 获得授权。
4. 验证端到端业务转换：普通 category/capability 创建后可选多个合格执行器；
   用户指定能保留；真实 host/资源约束生效；已有 pin 的续接/失败恢复不重选；
   无 capability 要求时空声明不是不合格；同 Agent 并发仍受真实 slot/共享资源约束。
   测试区分真实转换和 fixture 预置，不通过生产操作验证。
5. 在收敛实现和自测后进行原 Issue 的一次真实独立 whole-PR 审阅，保留所有原件。
   尽早创建 draft PR，允许本地必要验证和完整 CI 并行。最终合入前确认当前候选、
   base 和 required checks；使用保护规则下的 fresh-main rebase merge。

## 并行边界与交付

首次分配公平选择由另一条主线负责；本项不另写候选随机/负载算法。共享接口预计为
AgentRequirement、owner/self-execution 的输入补齐，以及设计文档；尽早交换真实 diff。
对方实际合入后一次性核查最终基线和交互影响，复用仍适用证据，按实际 delta 补查。
完成报告应自包含说明问题、行为变化、兼容性选择、实际检查及缓存/跳过、PR/合入状态。
交付仅合并；部署、升级 Start、生产配置和服务重启不在授权内。

## 确定方案与兼容边界（D250）

本轮收敛普通模型的公开发现面，不收紧既有 wire 的有效输入。新建工具沿用
`agent_selection` 的 category / required_capabilities，不新增平行调度合同。
`hostedMutationSchema`、Main 的同名 owner 工具和 `owner_capabilities schema_type`
同时投影所有可写选择类型：去掉 agent/runtime/model/effort、acceptance_execution，
以及 Issue create 的 assignee/legacy manager 旁路。consultation promotion 保留
manager.category_id/reason，避免删除该入口唯一的工作类别通道。真实 requirements.host_id、OS、labels、
workspace 保留，说明只用于实际资源要求。旧请求仍完整校验/执行其硬过滤条件，
不静默丢弃；通用 owner_call 的原始 JSON 也保留这一兼容性。发现面不是授权边界，
不能声称从此无法通过旧 wire 发送硬过滤。

服务端在每个新 Task spec 版本冻结 `selection_source`：真实 Actor（优先于被委托的
owner Principal）、原 request、输入的选择/acceptance_execution/资源要求，及系统
补齐的 owner pin。模型不能输入这份元数据来声称人类指派；source_message_id 只是
材料引用。人类通过认证 owner API/CLI 指派，来源为 owner；Main/Issue manager 走
owner authority 仍记录真实 agent。原 Issue 选择继续用已有不可变 revision actor/source
记录，不把引用的消息作者当选择者。不回填或推断历史来源。

调用链：named Main → owner tools → canonical owner command；Issue manager/runner
MCP → hosted mutation → 同一 canonical command；owner API/CLI → 同一 command；
canonical create → domainSpecFromOwner → prepareOwnerExecution → createTask；
update → domainSpecFromOwner → UpdateIntent → 新 spec；acceptance 的输入在同一 spec
中覆盖。历史 tasks_create 不再可新调用，其保留实现也冻结来源，防止未来误接入。
既有 Task action/continue/recovery 不建立新选择来源，不改变原 pin 或重放 unknown。

没有删除旧字段、增加旧请求必填条件、改变有效输入/默认执行语义，runner 通信仍为
4/3，无新增通信适配器或退役动作；原 schema/CLI 继续覆盖全部 owner 输入。
发现投影与原有效协议分开验证；不能把投影当 N/N−1 退役。回归覆盖真实 canonical
创建及 worker claim、同类别多个候选、独立验收过滤、模型伪来源、人类指派、系统
owner pin、资源限制，并复用/补查既有 continuation/slot/capability 用例。

并行边界：PR332 的实际 delta 由 owner 跟进；本项不改 scheduler、候选排序/随机、
共享资源准入。共享 docs/design.md 仅新增 D250；AgentRequirement 所在文件仅新增
来源类型，不删除或改变匹配字段。实际合入后核新 main 和最终 diff 再补查。

## F1 修正：保留咨询转新责任的工作类别通道

原候选的独立验收指出：CreateIssuePayload 有 owner.category_id 替代 manager，
PromoteConsultationPayload 却没有。把两者一起删除 manager 导致 Consultation 发现面
无法指定工作 category，省略后按旧语义固定回落 issue-manager；未登记该旧类别时
真实 promotion 返回 not_found。接受此缺陷，保留原验收 changes_required 与复现原件。

本次只修正发现投影：PromoteConsultationPayload 保留 manager 字段，将其投影为
category_id/reason（复用 IssueOwnerRequest 的类别/原因结构并隐藏 agent_id），
不暴露新责任不需要的 replace、epoch、source 或具体 Agent。新建责任应提供该类别；
existing_issue_id 的来源共享仍使用已有责任，不要求重新选择类别。CreateIssuePayload
继续使用 owner.category_id，隐藏其 legacy manager/assignee。底层 wire、CLI 完整输入、
旧 manager.agent_id/省略 manager 的既有语义和系统 pin 均不改变，不新增协议适配。

回归补齐 NamedSpecs(Consultation) 和 owner 类型发现；通过真实 consultation、认证
owner 消息、HostedCoordinationCommand、PromoteConsultation、canonical Issue 创建，
验证 investigation 与 implementation-economy 到达最终 OwnerSelection，不依赖注册
issue-manager。另保留独立验收原 overlay 复现，修复后验证其发现面断言。

验证采用原完整候选检查（已由独立验收核验原件）的有界复用，加本轮 tools/app 等
实际反向依赖的增量 make check 和新鲜定向 race；原任务总 diff 仍以任务起始提交为
基线，本轮增量检查单独标明前候选基线与证据身份。若影响计划要求完整兜底或发现
新失败，遵守其实际选择，不将旧完整检查描述为新候选的重新全量执行。
