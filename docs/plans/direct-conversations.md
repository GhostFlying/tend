# P6 CLI/API 与直接对话最小计划（架构已收口，ignored 实施）

状态：P6a 对话/来源/咨询与 P6b 原生身份完整 caller 候选均已独立预审通过；本副本已组合 P4 r6 和 P5 r3，正式组合仍待评审/全检。仅新 ignored 副本，不修改主 checkout/P4/P5/P8 冻结原件；无正式全量检查、提交、发布或部署。P6b 已接通 Orca terminal/session 真实上报，不以仅 unknown 投影替代实际入口。依据用户直接聊天/手动来源/原生控制合同和 p6-source-pointers；不推进 UI、不请求审图。建议一个可调用的 P6 候选，内部按下列两条完整调用链实施，不先交空表或未接入接口。最终正式分片由 root 决定。

## 前件与本轮已确认的真实接点

固定参考：4776 + P3 r3 + P4 freeze-r4（manifest233ffd81…6909/full59 patch3ac99298…92d4）+ P8a-r3 + P8c12 + P8b35。P8b完整manifest785d9ef3…84df；其新存储60依赖实际 P5 SQL59 e2c59fa7…c38。P5其余仍活动，仅仅读 Attention DTO/既有 respond caller，不复制为源码基线；实施前须取得最终 P5 冻结依赖。P4 r5 已独立 clean 并冻结（full61 patch5f5bc647…722、manifest713b5690…ce4），本候选已将其相对 r4 的真实修改三方组合到独立基线和候选，无冲突；保存逐路径 pre/post hash，不修改 P8b 已冻原件。并已按冻结 preimage/hash 纳入 P4 后继 correction4（工具后台身份真实读取修复）；SQL58 现为5c6bf2fd…b7df，P5 SQL59 保持 e2c59fa7…c38。P5完整 Attention caller 仍待最终组合，不能宣称同一 Attention 决策入口已在此片新增。P6所需新 migration 计划61或实际 final next，不改59/60，不空占位。

1. P4 SubmitMessage 对 canonical Issue 已只有 !canonical 才排旧 main notice，不重复实现通知抑制。现 p.Decision && conversation!=main 会拒绝；P5 AttentionCommand.respond 已复用原 Decision/Choice/version/worker答复路径，直接聊天须对齐此链，不建第二答案模型。
2. SendRunMessage 只 Authorize MainAgent，要求 SourceMessageID/CommitSeq/claimPlanCommit；server侧仍看 host.Worker。P3真实 GetRunExecution(run).Worker/Runtime/Backend 才是 named run 身份；legacy 无pin路径仍按原主机 worker。RunMessage.Source 已持久 Principal；SupplementalContext 却硬编码 authenticated main，需要修真实来源说明。
3. P4 HostedCoordinationCommand 验 execution/effect_seq、thread、当前 Issue/assignment epoch/primary，并明确拒绝 Consultation business mutation；run supplement 要接这个真实 wrapper，不模拟 owner。Consultation 复用同一 hosted lifecycle/禁原生执行工厂，不能拿 runner execution-only CLI 直接当可咨询 runtime。
4. 附件已有不可变原件、message引用、owner下载；背景会话已有原 outbox/pool、native pin/receipt/recovery。Orca已有 provider/workspace/session/terminal、takeover观察与handback；SetControlMode已拒绝 main 代用户接管或交回，ReportTakeover是原run报告真实事实。复用，不新造 provider/controller。

## 调用链 A：Issue / 精确 run 的 owner 聊天与手动来源

实际入口：扩现 `tend conversation list|show|send`（原 handoff 子命令保持）、`tend run message|messages <run>` 与 `tend source inject|show`，owner API继续 `/ops` + 现conversation/run只读路由与原文件流。命令层只stdlib+protocol；业务验权/持久化全经app。CLI为mutation保存/展示原requestID，unknown不改ID重发。

- Issue direct send 显式 conversation ID，校验当前 responsibility但不依赖 main繁忙/空闲；原文始终保持 owner身份。普通消息不改 task spec。责任已转 owner或manager离线时留真实目标/状态，不能代选另一收件人。对话、Inbox/detail回答同一 Attention/Decision；明确 owner答复由P5 respond/version/原 run结算，普通补充文本、read/archive都不伪造回答。尽量使用独立 Attention respond 动作，旧 main p.Decision 路径保持兼容，不把所有非main普通消息变成答案。
- Run supplement 入口必须携带精确 task/run/action/spec/generation/control revision，复用原队列/Claim/Report/receiver/thread/turn receipt。owner直接发使用当前认证 Principal +原request；不强造 source_message/plan_commit。named issue manager 经 P4 execution/effect_seq 校验当前 Issue 与 task immutable primary，再进入同一 SendRunMessage.Apply；不扩为任意跨Issue。legacy main SourceMessageID/CommitSeq 路径原样保留。run消息读取同步扩 owner与有scope的manager；原run自己读取不变。
- 对 named run 从 GetRunExecution 取冻结 Worker/runtime；当前 host.Worker/新 inventory 改变不能误拒或迁移原消息。legacy 无pin才用旧fallback。旧执行终态拒发，不自动指向后继run/新turn。TraeX只对现 active turn send；Orca沿 receive_messages/ack_message，不PTY注入。accepted仍只表示投递/worker receipt，不表示已读、服从或完成。
- SupplementalContext 用既有 Source Principal/SourceMessageID 显示真实 owner / issue manager / legacy main 来源；正文仍独立引用。改范围须对应 manager正式更新 task/新run，不把supplement升级成权限/验收变更。
- 手动来源只做原文/文件/链接/作者/原时间（可未知）注入，复用现有 message、不可变attachment及内容版本机制，附最少 typed metadata。认证上传者与外部原作者分别保留；外部内容不自动变owner授权。仅保存显式输入，不自动抓链接、不加连接器。
- 不另建 source_key/current-history 替换体系或独立 correct 命令。更正是一个明确链接旧来源的新注入，原文/原件/原作者保留；是否转发仅控制目标context，不删除来源审计。owner可显式引用其他Issue/只读档案材料，不转责任或构造真实性证明链；正式验收仍按role选择，排除实施推理/自评。

## 调用链 B：独立 named consultation → 显式 promotion；原生入口合同

实际入口：`tend conversation consult --data <registered-agent-ref-and-title JSON>` 创建逻辑咨询，随后同一 send/show；`tend conversation promote <id> --data <explicit-selected-source JSON>` 调 owner `/ops`。普通咨询不创建 Issue/task、worker slot或授权执行。

- 最小扩 agent_conversations 的显式 kind/选定definition绑定；main/既有Issue逻辑ID与默认行为不改。consultation配置只由P4本机定义唯一提供；选择已登记且真实支持 hosted no-native-execution 的 runtime（可以复用模板model/instructions，但execution-only安装不能假装可用）。固定选定definition/runtime revision、实际native配置与历史；缺配置显示原因，重启不暗换代理。复用 BackgroundPool、Manager、原 receipt/summary/stop，而不是第二管理器。
- 一条窄的 consultation promotion/delegate 操作同时服务 owner API 与实际 hosted consultation 工具。咨询中真实 authenticated owner 请求执行即足够意图来源；不强制先做另一个owner promotion事务、按钮确认或证明链。hosted调用保留真实 consultation Principal，验证本conversation/execution/effect_seq与所选已持久source，复用P4稳定effect和同一promotion app用例，不模拟owner、不授予通用业务mutation。普通问答仍不创建任务；改变native callID或重连重试也不重复建对象。

- 新Issue进入当前manager真实责任/唤醒链，由其经已实现 OwnerTaskSpec/coordination 创建冻结task；选已有Issue则给该manager明确新来源/请求。测试必须走到实际task/runner领取，不能只证明建了promotion表。咨询建议/reviewer意见不是正式acceptance；转入任务只选共享目标/约束/原始证据，implementation rationale不能整段给独立验收。原咨询历史保留为只读引用（普通对话仍可继续），不是变成worker会话。
- 最薄 owner native-entry GET/CLI show 展示现存真实 Orca workspace/provider/session/terminal 与 controller观察/capability；只读取。缺少已持久身份就 unavailable/unknown，不从可打开URL推断控制权、不猜session/terminal。owner takeover/handback沿现 task CAS/SetControlMode 与原run观察，查看不发控制写、supplement不被误判为外来接管；Agent不能代owner交回。
- Cove只做 owner入口身份/capability/controller-observe/takeover/handback/unknown合同与负控，明确 adapter_not_implemented/available=false；不加provider、不实现完整Cove adapter、不增加“可用”标记。

## 必要文件边界、协议与验证

文件聚焦：protocol conversation/source/promotion/native-entry DTO及APITypes；store一份实际追加migration+最少 source metadata/conversation/promotion仓储；app conversation/run_messages/hosted_coordination/原source与Attention接点；mainthread pool/AgentContext仅consultation分支；api ownerCommands/read routes；cli conversation/run/source；harness SupplementalContext与Orca既有receiver/controller契约。生成schema/TS正常生成；无可见UI布局/样式/交互实现、无第二source/blob store、provider或调度器。

P8b已准备时新source/chat/promotion仍被body前owner gate拒绝，新consultationnative启动也走现prepare hook；旧receipt/facts继续。P5 来源/Attention答复以最终真实caller语义组合，不覆盖其活跃文件。

初评server/runner wire1/0可additive：复用现 RunMessage JSON/Source/投递状态、原cap，新增owner API/存储不进入旧runner必填字段，不改legacy无选择默认。native-entry/Cove合同不自动advertise runner capability。若实际实现须新增旧strict decoder拒收的字段或收紧旧有效请求，则停下报告真实breaking与N/N−1退役，不能偷改默认。

必要fixture只验证实际caller：owner直发保留身份；同primary manager成功/跨Issue和旧epoch拒绝；本机host默认变化不改run provider；重复/断线unknown/终态不转新run/不新turn；真正Issue直聊无需main与同一Attention答案回到原worker；source原件/未知作者与显式目标注入/链接更正不伪授权；咨询普通问答无task/native执行、禁止通用mutation、真实owner对话请求触发窄委派、promotion重复只一次并最终实际派task；正式验收不继承咨询实施推理；Orca查看零控制写、非owner不能takeover/handback，Cove明确不可用；prepare阻止所有新增caller。实施后才跑这些定向tests并冻结，未执行即不计通过。

## P6a 已实施边界与核验

- owner CLI/API → 原 run 消息队列/claim/worker receipt/history，以及真实 Issue manager wrapper/effect_seq/primary scope。原任务合同、run身份、legacy主Agent来源/commit合同不变。current host 默认 worker 不替换 frozen pin。
- CLI 上传原件 → 手动来源注入 → owner show/原件下载。原作者/时间可未知，上传者保留真实 Principal；message.Author=external，不把外部原文变成 owner 决策。显式 Issue 注入在同事务增加其来源审阅义务/上下文 revision，不要求主助理审批；没有新 source_key、correct 命令或分享管理层。注入的显式 recipient 就是目标 context，原件一直保留。
- 实际 hosted pool/native tool 回调：普通问答零 Issue/task/run；owner 执行请求调用唯一 consultation_promote，再由真正 manager 创建冻结任务。无额外 owner 事务/确认，真实 consultation Principal、execution/effect_seq 和选中来源版本保留；重试不重复建对象，普通咨询无通用任务/控制写权限。consultation definition 私有 pin 来自唯一 hosted 配置，公开列表只有安全身份，不上传路径或 instructions。
- owner native-entry GET/CLI 当前只读持久 workspace/provider/冻结 run 与控制 revision。无真实 terminal/session 上报时保持 unknown；查看不发控制写。沿现 takeover/resume owner CAS。Cove 为 adapter_not_implemented；没有新增 provider。精确 Orca session/terminal 由后续必要 P6b 完成。
- 新 chat/source/consultation/promotion caller 都受 P8b body 前准入；不新增归档排空声明。owner回决策仍复用最终 P5 Attention respond，普通文本和 external source 都不自动结算 decision。

实际定向检查：app/API/hosted pool/legacy run messages race 已通过；独立 Issue external source 审阅义务增量 race 已通过；生成真实 APITypes schema/TS 和 web typecheck 已通过；目标 vet/staticcheck/现有 compat checker 已通过。初期 fixture/API 类型编译失败、缺 node_modules 生成失败，以及 staticcheck 命令/旧工具链启动失败均保留日志，纠正后实际验证通过。无全包 app/full make check/规定 CLI review/远端操作。正式接入前需组合最终 P5 全片，并定向核对真实 Attention 回原 worker；P6b 仍待完成。

## 最终前件组合：P4 r6 与 P5 r3

原 P6a39/P6b12 冻结件保持不变；新副本按精确冻结前件组合，保留 P8b 的准入/只读对账 hook。P4 r6 的四项 caller 修正（owner assignee 来源、旧 rejected delivery 交接、native steered receipt、named 摘要原 selection）和完整 P5 r3 的 Attention/期限/Planner 事实均保留。共享工具列表同时包含咨询的唯一 promotion 和 P5 角色作用域；不授予 consultation 通用业务操作。schema/TS 由实际 roots 重新生成。

组合定向 race 已通过：owner CLI 回原 Attention/Decision，Main 与当前 Issue manager 中转真实 owner 原文，原 worker/run/租约继续且仍独立验收；直接补充使用冻结 run worker；咨询实际 hosted pool 从明确执行请求委派并创建任务；原生身份丢回执与物理 journal 重启仍保持原 session，仅一次 launch。合并后的具名规划器实际 Attention 工具调用亦通过。普通非 main 聊天仍不能隐式回答，手工 external 来源不成为 owner 授权。相关包编译/vet、类型生成/typecheck 通过。首次新副本缺 node_modules 的生成失败只作为 setup 诊断保留，补读既有依赖后通过。

以上仅候选的真实组合检查；完整 make check、规定 CLI review、CI、合并、部署和真实 Orca/模型验收未执行。公共 P8/P5/r6 seam 的最终一致性由同一冻结组合逐路径对照，不维护第二行为定义。
