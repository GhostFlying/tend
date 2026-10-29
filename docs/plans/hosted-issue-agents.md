# P4：具名托管 Agent 与 Issue 负责人的实际闭环

状态：P4 r18 已正式纳入。第十一轮规定 CLI 四项实际缺陷经 r17 修复并独立复审；其全检三组失败已复现，并以局部错误识别和专用 named fixture 修正，经 r18 独立复审通过。原分支取消建议按已确认合同不采纳。最终完整检查与交叉编译进行中，CI/合并、部署及完整真实模型验收尚未完成。server 迁移 58 尚未提交。Refs: D121–D123。

## 唯一配置与执行身份

复用 P2 `agentconfig` 值模型和 P3 私有 instructions，不让 app/mainthread 依赖 runner。server 本地文件包含明确 configuration revision、execution environment identity、默认 main ID 及多 runtime/agent 定义。启动按既有 app 请求事务接受不可变私有快照，同行 revision/hash 幂等、冲突拒绝；owner 只读库存是安全投影，不上传凭据。当前只支持启动/重启生效，不提供热重载或第二配置源。`RuntimeBinding.ExecutableRef` 是实际启动命令；安装来源 locator 不是覆盖值。

named 模式 definition 唯一决定 model/effort/instructions，旧会话 Settings 只投影并拒绝另写覆盖。admission 在同事务冻结具体 definition/runtime/model、Issue/assignment epoch；恢复和 native 对账取原 execution，不按当前文件重选；限流续接的 admission/confirm 也按原 execution 的配置 revision 校验，不误用旧会话 Settings 版本。已登记 named main 配置缺失、canonical manager 无配置或无冻结 selection 时明确阻塞，不退回旧 D111 native auto 路径。旧未采用 named 的历史会话保留兼容；新空账本部署必须显式配置 named 默认 main，并核对登记及真实适配能力。

托管 main/issue/consultation 的配置、角色和 category 可登记；它们不占 worker 槽位。consultation 本片仅复用 definition、runtime 与禁执行能力，独立咨询会话和 promotion 产品入口留 P6，不为普通讨论创建 Issue。

## 服务端作用域与原生工具边界

main 可按真实 owner 来源接收目标、创建/委派 Issue、管理个人规划，并转录 owner 对已有 decision/version 既定选项的回答；不获得本项自由 task 规划或执行能力。Issue manager 只推进自己的定义、context、task、child 和 completion；读子树结果不把次级成果引用变成控制权。所有实际调查也走 task。worker 仍使用 P3 冻结选择、实现和正式独立验收，不继承 manager 本轮实施推理。

具名 hosted TraeX 用仅作用于该 app-server 的固定 `PreToolUse` inline shell builtin `printf` 拒绝 native 子代理函数；保留 readonly/never 和原生命令、搜索/研究、browser 等既有关闭配置。允许理解 owner 提供的图片/有界材料，不以工具名字仍在 schema 中判定是否可执行。预检进程不创建 thread，读取 CLI 的 exact key/hash；最终进程通过 `-c` 的完整 hooks.state 映射注入局部信任，回读 enabled、errors/warnings、固定 command/matcher/source、key/hash/trusted 后才启动。每次 start/resume/turn 重查，缺失或不一致拒绝 hosted 执行，不回退 D111。worker 和 owner 全局 TRAE_HOME 不改写。当前具名 hosted adapter 仅启用已实证的 Linux CLI shell 路径；其他宿主明确 unsupported，不能以 trusted metadata 或 Windows/macOS 交叉编译通过宣告 runtime 可用。worker 既有平台支持不变。

这是可信 owner/主机下复用 CLI 的职责约束，不是 OS 沙箱。实际 CLI hook 引擎在 hook 命令失败时仍 fail-open；固定 inline 不依赖可删除外部脚本、网络、业务参数或业务 IO，并非声称原子 fail-closed。未测试的 hook timeout 行为不作承诺。服务端角色/对象控制仍独立强制；不防 owner/root 主动改库、修改本机配置或同 UID 恶意进程。

## 实际协调与交接

`hosted_executions` 冻结私有选择；`hosted_coordination_effects(execution_id,effect_seq)` 去重协调效果。execution 身份与 owner source message 分离，同一来源可有两个独立 Issue 回合；repair/限流续接沿原协调身份，不能重放工具效果或恢复旧 epoch 的写权。

owner assignment 先持久 handoff 意图并提升 epoch 围栏旧写，再核对该 Issue 的旧 manager native、发送中的控制和 unknown，最后启用新负责人。正常 primary worker 继续原合同，不要求为换负责人先停 worker。明确未发送的旧 manager repair/限流 continuation 在实际 native 已收束后随 handoff 停止；sending/unknown 不冒充未执行。只停止旧 manager 的精确 logical 责任，保留 Issue 来源义务，不停止共享 main。reparent 另按实际子树执行、授权继承与依赖图核对，不把单纯来源审阅份额当作整个 main turn 的执行 ownership。

Issue scoped context review 复用已有 fact、obligation、logical 表，允许模糊目标/baseline0 的调查 task，没有新 fact 也可如实记录不确定性；不伪造全局技术 baseline。具备持久 hosted main 身份的 main 完成接收/分类/委派给具体 agent 或 owner/user 后即可结束自己的 logical turn，不等待 manager 先做本项技术审阅。manager 只处理自己的精确来源份额；同一来源关联两项时，各自 review/commit，来源修订使过时份额不能收尾。已有 task 合同仍须显式 refresh，review 不暗改 spec。

## durable 接续与边界

复用既有 pool/outbox/logical/batch：owner 委派唤醒具名 manager；worker 正式验收后生成本项 completion review；manager 核对事实和现有承诺后可关闭；child endpoint 仅唤醒 parent manager 核对，不自动关父项、消除 attention 或替 owner 决策。manager 不能使用 `owner_decision:current_request` 伪装 owner；可引用真实 owner 原文或已有已回答 decision，沿现有 evidence resolver，不新增 request/event 真实性证明链。

P5 负责 Attention/期限/个人 next action 及完整 owner-answer 路由。尤其需把“main 转录 owner 回答后无需再唤醒当前 main”与“仍需 durable 通知当前 Issue 负责人/原等待 worker”分离；本片不声称该新增决策闭环已实现。P6 负责独立 consultation/direct chat/run supplement；P7 布局仍按已批准页面设计实施。本片无新可见 UI、无归档切换、无部署。

## 验证与交付门槛

候选已接通真实 app/pooled tool 链路：main 委派且先收尾→baseline0 manager 创建调查 task→独立 worker 实现/正式验收→manager completion review/有证据关闭→parent 持久结果唤醒；另有来源修订、独立份额、epoch/native unknown、正常 worker 持续、reparent、已结束旧 repair 与已知未发限流续接回归。它们使用 testkit/harness，不是模型能力声明。

实际安装 CLI 的本机 deterministic provider 测试覆盖 start/resume：命令调用被拒、patch 未写文件、native spawn/send/resume/wait/close 被真实 PreToolUse 阻断；随后 Tend 动态工具真实 callback 正常，owner 配置文件字节不变。曾有外部脚本失败/缺失 fail-open 诊断，因此正式候选采用已验证的进程局部固定 inline 路径。普通单元测试不联网/不调用模型；该实际 CLI 用 integration tag 和显式本机 opt-in。

下一门槛是冻结组合 artifact/文件 hash，独立 xhigh review、正式定向校验及唯一完整 `make check`，然后 PR/CI/线性合并。真实部署和多主机/实际模型 E2E 仍留总计划 P9。

开发检查记录：组合 P3 后的新 Hosted/Named 定向 race 已通过（app 69.051s、mainthread 10.409s、TraeX 1.071s），当时尚不包含随后定位的具名恢复 revision 修正；修后精确恢复及 legacy settings/stop 回归 race 12.725s 通过。owner/user 委派正例先因 baseline0 失败，正向 hosted main 身份与本项份额修正后通过；最终受影响 race 结果随固定 artifact 验证清单保存。Schema 当前性、生成 TS 当前性与类型检查通过。TS 首次生成因隔离副本无 node_modules 失败，复用既有只读依赖后通过；这不是代码通过记录。没有运行完整 make check、规定 CLI review、Git 提交或部署。


## r4：已采纳的正常入口修正

r3 固定范围独立预审完成，共三项 P2，均采纳。第一，hosted callback 的实际 agent Subject 是 binding instance，不能作 human owner；人类负责人取当前 owner principal 或已有鉴权后持久化的 owner 请求身份，main 的实际 agent principal 保留。不读外部作者字符串，不建身份注册表或 request/event 证明链；没有真实 owner 身份时明确拒绝。

第二，为 manager 注册 `tasks_cancel`、`tasks_rate`、`actions_create`，均沿相同 execution/effect_seq、expected task revision 和 primary Issue scope 进入原 App 规则。`actions_create` 仍只允许 implement/accept，不授人工 takeover/handback；跨 Issue 操作和过时控制版本拒绝，原请求重放只读取 receipt。

第三，worker 正式验收与 child endpoint 属持久事实，manager 当前配置停用、移除角色或换 execution environment 不得反向回滚。现有 Issue conversation/batch 保存原具体 assignee/epoch 的 completion review 和待接续输入；native admission 单独检查原负责人，当前不可用时保留排队、明确阻塞，不换 owner、不回落 D111，也不建第二队列。

r4 使用实际 pooled main/manager 动态工具与 worker 实现/独立验收链验证：main 创建 owner 责任并完成到 owner 的交接；manager 本项 retry/rate/cancel 与 receipt 重放；跨项、旧版本和 owner-only 控制负例；移除 manager 角色后验收事实及 child close 均持久、原 parent 负责人/承诺保持且待唤醒保留。初始三条红例和修复后检查日志随固定 artifact 保存。r4 五条受影响实际链路定向 race 通过（app 16.296s、mainthread 32.530s），app/tools/mainthread vet 与 schema 当前性检查通过；独立增量复核待完成；未运行完整 make check、规定 CLI review、部署或真实模型验收。

P4 正式纳入补记：固定候选 r4 patch SHA-256 `3ac992986f6ba8dc380072e8418c3b5fc4d572fa893b63df7717fdc40db292d4`，59 文件；三项修正已独立复审通过。正式前件为 `c69c66b2be9b49aa1fbe824d9ea6283bec5dc880`；本次保留 P3 完整检查、上游 CI/合并记录及其 tools schema 修正，仅合并最新状态文档，不改变已预审的运行代码。正式组合检查与规定 CLI review 待完成。


## r5：正式 review 的六项可达接续修正

具名工具先识别 metadata repair，再按实际角色限制为读取、分类、事实审阅和 commit；repair 不创建 Issue/task 或执行业务控制。manager 仍仅审自己 Issue 的份额，原执行身份、effect_seq 和 epoch 围栏不变。真实 pooled main/manager callback 在旧代码上分别能创建额外 Issue/task，修后拒绝业务写，合法 review/commit 仍可结束回合。

普通 manager 断线的 pending native receipt 也由既有 background pool 发现，独立于新回合准入和 assignment fence。迁移 58 在原 receipt 上追加 next_reconcile_at，复用既有 30 秒持久读回退避，不新增队列、不重发 native 执行。恢复先读取原冻结 executor；已明确 rejected 且仍 pending 的普通投递也保留原 selection，不因配置更新换 runtime。交接仍等真实 receipt 对账，receipt-only discovery 不允许新业务准入。

最终 settle 的 context 未就绪查询与本项 read/commit 使用相同 scope；另一个 Issue 对共享来源写自己的 fact，不使已审核的本项重新等待兄弟项。background repair 确定未准入失败后也沿已有次数和退避进入下一次 repair 或 needs_review；unknown 不当作确定失败重试。

来源审阅允许逐批持久处理最多 100 条：有剩余份额时返回 complete=false，不写完整 context review，不允许 turn commit。后续精确审完剩余份额才返回 complete=true。新来源、更正和事实失效仍保留真实待审义务，不制造全局 baseline。实际 101 条 owner 来源使用 100+1 审阅验证，不要求一次超过接口上限。

r5 实际 caller 定向 race 已通过（app 50.824s、mainthread 16.864s、API 4.070s），包含七条新回归、旧 unknown receipt/source correction 和原 assignment HTTP 链。工具 schema 当前性和预算检查通过；vet 无诊断。旧正式源码上的同组七条新回归均失败，其中真实 main/manager repair callback 分别返回额外 Issue/task 创建成功；修后角色业务写拒绝，合法 review/commit 完成。正式独立增量复核和完整检查仍是后续门槛；r1 完整检查不能作为 r5 通过证据。

## 正式 E2E 后继修正：后台工具的真实身份

正式全检暴露旧后台完成通知与 Web 决策来源读取失败。实际 callback 保留 BackgroundAgent 身份，但公共工具封装错误地套用仅 MainAgent 可使用的 execution wrapper；具名 manager 的 background_sources 同样受影响。工具封装现只为实际 MainAgent 增加该 wrapper，App 原 main-only 检查不改，后台 command 仍使用自己的 delivery/thread/turn/Issue authorizer；不冒 main、不扩大业务角色。

原完成通知/重启 E2E 在 r5 前件上复现超时，新增具名 manager 实际 pooled 来源读取也复现相同 forbidden。Web 测试的异步 sourceResults 原可在首次 callback 之前被零次读取而漏检；现沿原 10 秒预算等待首次实际读取结果，旧实现稳定复现同一错误。修后两条正式 E2E、具名 manager 读取和既有 main execution/旧 epoch 回归的定向 race 通过（E2E 18.779s、mainthread 6.244s、App 7.329s），相关 vet 通过。新的独立增量复审和完整检查仍待进行，无部署/真实模型验收声明。

正式第二轮冻结补记：r1 完整检查自然结束为 exit 2，App、mainthread、tools、store 通过；API 的过时 assignment 断言及两项后台身份适配 E2E 失败已分别修正，Web 因 Go 阶段失败未运行。r5 六项修复和 E2E 四文件增量独立复审均无新增 findings。最新前件 `3ec40b016a28f2e0ae1a7c2b2528698520d58c9c` 与原前件的源码树一致；本轮仅更新前件提交身份和进度记录，不覆盖 r1 失败事实。第二轮规定 CLI review 和完整检查结果另行补记，不将定向通过写成全检通过。

## r6：第二轮正式 review 的四项实际调用修正

第二轮规定 CLI review 提出四项正常入口缺陷，均采纳；该轮完整检查仍由协调者独立运行，以下隔离修正不改其冻结前件或提前声明通过。

- main 创建人类 owner 负责的 Issue 时，同样绑定本次精确来源，使 main 能提交已接收/分类/委派的份额；native manager 会话及唤醒仍仅用于 agent assignee，不要求人类先执行本项。
- 负责人交接完成时，仅退役已知 native rejection 且退回 pending 的旧 epoch 普通 delivery；sending/unknown 保持待对账，来源与本项待审义务留给替换负责人，不重发旧管理回合或停止独立 worker。
- owner 补充已进入 native 但其 user-item 通知晚于工具 callback 时，在 named Issue 分派前沿既有 ConsumeMainSteer 持久精确回执；协调 execution/effect_seq 不被来源身份替换。
- idle/owner 维护摘要的 model/provider/effort 与隔离配置沿当前 native generation 冻结 named selection；不会被保留的 legacy settings 覆盖，也不为摘要改选新的 current definition。legacy 设置路径保留。

真实 caller 红例分别为 owner-only 委派后 commit 拒绝、交接后旧 rejected delivery 仍 pending、补充 callback 的 admitted-source 拒绝、named 摘要误选旧 catalog model unavailable。修后定向 race 通过（mainthread 30.796s、app 13.796s），覆盖实际 named 动态工具、原协调身份、替换负责人继续和来源保留，以及 sending/unknown、worker 持续、steer 去重、legacy settings/摘要隔离回归；vet 与工具 schema/预算检查通过。正式独立增量 review、组合完整检查、CI/合并和部署仍须分别完成。

正式第三轮准备补记：r2 规定 CLI review 正常退出但提出四项 P2，均采纳并形成 r6 九文件修正。独立复审确认 owner assignee 来源绑定、交接时已明确未发送旧回合退役、named 工具前的 steer 来源回执以及维护摘要冻结模型均按实际 caller 修复；四条旧代码红例与新代码定向 race 保留。r2 全检在运行中主动中止以检查修正版，属于未完成，不作为产品失败或通过证据；当时 API 已通过，Web 未执行。r2 Windows/macOS 交叉编译通过仅归属于其冻结候选。r6 完整检查与规定 CLI review 另行记录。


## r7：第三轮正式 review 的两项实际调用修正

第三轮规定 CLI review 的第二次调用自然退出并提出两项 P2，均采纳；协调者的完整检查仍运行，不能把本次定向结果写成全检结论。

重挂核对子树 manager 的实际 logical/outbox：普通 native 明确拒绝后 Retry，再被 NextDelivery 领取时，即使 main_turn 仍是 rejected、尚未记 TurnStarting，sending/unknown 也阻止改变继承上下文。未领取 pending 不新增阻塞；已明确失败的领取沿原 delivery 收束后可正常重挂，不清来源义务。

manager 的 tasks_read、artifacts_read、background_evidence_read 允许读取 canonical descendant 的 primary 成果，并复用既有明确链接的单任务成果读取范围；背景证据读取使用同一范围。任务控制仍只限当前 Issue 的 immutable primary，消息与附件的原来源边界、worker 正式验收 contextbuilder 均不变。canonical 新计划不接受外部 primary task 作为 stage 成员，旧 LinkIssueTask 也拒绝 canonical target，本片不借这些入口扩展业务关系或伪造其正例。

真实 owner/普通投递回归先复现 sending、unknown 下子树重挂误成功，named tools.Handle 回归先复现子项及孙项三种读取被拒。读取用例通过实际 owner Issue/task 创建与 named admission；run/artifact 是明确的持久事实读取 fixture，不冒称此测试完成 worker 执行或验收。当前增量仅进入隔离候选，独立复审与正式组合完整检查仍须完成。

本次隔离定向 race 通过（App 33.453s、tools 8.330s、mainthread 6.899s），覆盖两条新回归、旧 native/unknown 重挂与交接、receipt 重启退避、实际 worker 验收到 parent wake、无 stage 验收上下文隔离和原 primary 控制。App/tools vet 与工具 schema 当前性/预算检查通过。r7 独立复审和正式完整检查尚未执行；未做 Git 提交、部署或真实模型验收。


## r8：首次普通投递的实际重挂窗口

r7 独立复审进一步指出首次 Manager.Deliver 在 NextDelivery 领取和 AgentContext/InjectDeveloper 后、首次 PutMainTurn 之前尚无 hosted/main/logical execution 行。重挂查询因此还要按实际 outbox destination 与 Issue 专属 agent_conversation 纳入子树普通 sending/unknown；不从共享 source 推断整个 main 的执行归属。pending 不新增阻塞，明确 failed 仍能正常重挂；不新建状态、表或准入系统。

真实 App 回归对 handle_message 和 handle_event 各自验证此首次窗口：领取并读取 AgentContext 后显式确认三种 execution 表均无该请求，再尝试 owner 子树重挂。旧代码四条 sending/unknown 分支均错误成功，pending/failed 正控正常。该修正复用原 r7 查询结果集，成果读取与 primary 写边界保持不变。独立 r8 候选保留 r7 原件，正式全检由协调者单独安排。

隔离 r8 定向 race 已通过（App 57.813s、tools 8.191s、mainthread 6.845s），包含首次两个普通入口的八个状态分支、原 r7 retry/读取和已有 native/unknown/worker 验收回归。r7 成果读取与其余前件代码保持原字节；r8 独立复审、正式完整检查及交付状态另行记录。


## r9：交接回归的实际投递收束

第三轮完整检查的两条失败来自旧测试准备顺序：native terminal 已落库，原普通 outbox 仍 sending；实际 dispatcher 要在 Manager.Deliver 返回后另记投递回执。测试仅在完成原 request 的真实 FinishDelivery 后启用新负责人，并保留 sending 时拒绝交接的负控。原来源责任、未发送 repair/限流恢复退役、新负责人接续断言保留，生产围栏与 unknown 语义未修改。

隔离旧例重现后，修正的两例及相关交接/unknown/receipt/重挂/冻结恢复定向 race 通过（App 60.343 秒），vet 与格式通过；r9 单文件独立复核通过；正式第四轮完整检查另行记录。第三轮失败日志仍保留，Web 因 Go 阶段失败未运行。

## r10：统一真实请求的管理准入与恢复

第四轮独立 CLI 复审发现一项交接围栏缺口和三项正常配置更新/重启缺陷，均采纳。首次普通 delivery 在 sending/unknown、尚无 execution 行时，交接也按 exact Issue conversation/outbox 围栏；运行时在注入上下文后始终提交实际打开的 named selection，由同事务 freeze 比较，不能把新负责人快照贴到旧 executor。

Runtime selection、队列领取和会话发现复用同一 serviceable head。原 outbox owner 优先及组内顺序保留；当前定义不可用的新请求保留 pending，不挡已注册同 epoch 的 repair/recovery。普通 native receipt pending 在无 recovery 行时仍挡本 Issue 新业务；receipt-only discovery、持久退避和精确对账保持，其他 Issue 与 worker 不随之停派。AgentContext 和最终原生准入沿请求冻结快照，不因当前定义停用/移除而抛弃已确定需接续的责任。没有新服务、队列、表或授权票据。

定向回归先以原生产代码复现四类 caller：首次交接错误成功、v1 runtime 与实际 v2 owner 队头冲突、普通 receipt 下错误领取新业务，以及合法停用/移除 manager 后 frozen repair/recovery 不可发现。真实 BackgroundPool/Manager 两例另验证 v2 owner 与原 v1 repair 各自提交，以及新 owner 输入保持 pending 时原 pin 修复仍能提交；旧原生 thread 不绑定新配置。合法停用测试保留有效 main，避免把无效配置的 Prepare 拒绝误计产品回归。r10 隔离定向验证及独立复审结果以固定 artifact 证据收口；正式完整检查由协调者另行安排。

r10 隔离回归已完成：原实现五条 App caller 和两个真实池用例均红；修正后的相关 App 定向 race 99.205 秒、mainthread 32.441 秒。实际 runtime pin 在上下文注入前捕获后的最终 native/legacy settings/恢复注入定向 race 32.128 秒通过，相关包 vet 与格式通过。store 包该轮测试筛选无匹配，不作为独立 store 单测通过声明；队列排序由真实 App/池 caller 覆盖。独立复审及正式完整检查尚未执行。


## r11：继承输入与确定启动失败的后台进展

第五轮正式 review 的两项 P2 均采纳。子 manager 的 AgentContext 复用 worker 冻结合同已经使用的祖先只读目标、约束、有效事实及版本投影；重挂后按当前祖先重新读取。它不授予祖先控制、来源审核或无关任务历史读取权限，也不改变独立验收上下文。

后台首次本机启动在 ready 之前确定失败时，向现有 pool 返回失败并释放槽位；原 serviceable pending outbox 保留请求身份、正文和 attempts，仅持久延后 next_attempt_at。发现沿同一队头到期时间，避免两个不可用模型每次扫描重新占满两槽。已启动 native、sending/unknown 和 receipt-only 发现/退避仍走既有对账，不把没有回应视为未发送，也不引入队列、表或调度服务。前台原有 session restart 保留。

实际红例分别复现 manager 缺少 worker 已继承的父级约束，以及两个正常登记但 catalog 不含其模型的负责人阻塞第三个健康 Issue。修复后真实 limit=2 BackgroundPool 中健康 manager 经动态工具审核并提交原 logical turn，坏模型输入仍 pending、attempts=0 且没有 native turn，发现不提前重新接纳；重挂后 manager 输入与 worker 的当前祖先投影一致。隔离定向检查和固定 artifact 另记，正式完整检查、规定 review、CI/合并及部署尚须完成；第五轮被替代的完整检查为未完成，不作产品失败或通过。


r11 定向 race 已通过（App 70.634 秒、mainthread 24.638 秒；原正文/队头退避/作用域回归 mainthread 7.254 秒、tools 5.482 秒），vet 与格式通过。独立 reviewer 核对固定七文件增量、72 文件完整候选和真实 caller 回归后无新增 findings。第四轮全检通过、第五轮规定 review 两项缺陷及第五轮全检主动未完成分别保留；第六轮正式检查结果另记，不用定向检查替代全检。


## r12：未发送启动、原生回执与终态发现

第六轮正式 review 三项正常 caller 问题均采纳。原生 hook preflight 在实际 turn/start 之前失败属于已知未发送，保留原始错误原因但不伪造 unknown；真正发送后的回执不明继续原对账。已冻结的普通业务重试保留原 executor pin，仍校验本 Issue 的未核清 native receipt；合法 repair/recovery 及 receipt-only 发现不借此改身份或取消事实。canonical 终态仍发现并处理原有待投递 batch，legacy closed 继续旧禁派规则；不删除责任事件。当前仅隔离修正/定向回归，原完整检查被替代未完成，正式检查与交付另记。


r12 定向 race 与相关 vet 已通过；独立 reviewer 核对固定十文件增量、74 文件组合、实际 caller 红绿及原未知/恢复/历史边界后无新增 findings。第六轮规定 CLI 的正常退出不表示通过，其三项 findings 均采纳；第六轮全检主动中止为未完成。第七轮正式完整检查、规定 CLI、CI/合并和部署仍分别记录。

## P4 r13：主会话来源分类与延期队头的冻结续接

真实模型链路证实：主会话转既有 canonical Issue 的新材料时，旧分类工具被 legacy guard 拒绝，metadata repair 无法完成。named Main 使用 typed conversation_associate_issues，经原持久协调执行/effect_seq、消息 CAS/content revision、owner纠正锁写分类；只认领本轮分类份额，不清本Issue技术待审义务。metadata repair 只允许该精确分类及原读取/commit，不开放任务规划。named Main 职责提示独立于旧 dispatcher 执行提示；旧unnamed保留原兼容行为。owner新公开API/完整字段投影、可见caller、Issue scope/criteria仍属B1，不混入本修正。

已知未发的新普通输入因模型不可用持久延期时，只允许已注册且到期的冻结 repair/recovery 越过它，使用原pin；任意后到普通输入不能绕头，正常owner优先、sending/unknown/receipt门槛保持。复用同serviceablehead选择供runtime/discovery/claim/context，不新增队列/定时器。两项真实caller红例及定向验证见本批证据；独立复审/完整检查未完成。

r13 来源唤醒窄修正：本协调 logical 的已持久主会话回复（包括原执行回复由 repair 分类）只补分类/审阅元数据，不再制造新负责人输入；用既有 message→native turn→logical 归属判定。实际 owner 新材料或当前 owner 显式选择的来源仍精确唤醒。named Main 提示仅列本片实际广告的工具，后继 catalog/planner 能力以实际工具集合为准。

第八轮正式组合补记：r13 修复及五路径来源唤醒增量均已独立复核。原生和 metadata repair 回合使用同一 logical 归属识别自身叙述，仅抑制额外唤醒；owner 新输入及显式引用仍保留原效果。提示词按本片实际工具面表达，后继能力以已提供工具为条件。第七轮全检未完成和两项 review findings 保留，不以窄审代替本轮完整检查。


## r14：实际来源准入、后代证据与 repair 工具合同

formal r8 的规定独立 review 找到四项实际 caller 缺口，本片逐项修正：queued owner 原文/版本不再由 issues_read 旁路泄入当前 agent；review/fact/task 来源沿同一既有 native 消费事实，owner 当前命令和已准入历史资料仍可直接使用，不新增身份证明。未准入来源的 durable 审阅义务保留给后继输入，当前 logical 可只完成已准入份额，不把后到消息塞入原回合或删除义务。withdrawn 仍是独立来源更正，agent 展示不当成指令。

父项可引用真实后代的当前已完成 task/passed acceptance/当前 artifact，但不获得后代控制、兄弟范围或实现者推理。named main 复用原 hosted source selector schema，允许实际较早已消费的 owner 答案；自动 repair 依冻结 admitted role 指向真实 main/manager 工具，main 不被要求技术 baseline，manager 不被提示 legacy conversation/global baseline。原 metadata-only 权限和原事务/effect 身份不变。

验证包括实际 SubmitMessage queued→拒绝提前 review/task、已准入原份额 commit→后继来源责任保留→后继正常 review/commit，真实 worker 实现/独立验收→child close→parent 引其验收关闭，实际 named callback 的 repair 指令及 earlier owner selector。原 101 条分批 fixture 曾把 100 条 queued、未消费材料当可审来源；已按原目标改为真实 Claim/ConsumeMainSteer 后再审，保留 100+1、commit 负控及新 queued 负控。formal r8 全检因采纳缺陷主动停止，未完成，不作通过/产品失败；本片候选定向验证与独立 review/正式全检分别记录，尚未合并或部署。

r14 固定十八路径增量及 84 文件完整候选经独立复核无新增 findings。原 101 条分批回归通过真实准入保留原目标，queued 来源负控与后继责任均保留；当前完整候选的规定 CLI review 和 make check 仍须分别完成。生产版本及原隔离真实模型运行均未热替换。

## r15：具体来源消费一致性与 owner 分类通知

formal r9 规定 review 的四项实际 caller 已采纳：named main 分类 target、新增嵌套 owner 消息证据、agent 会话预览沿既有准入/撤回边界；旧 owner 分类操作与新 typed 分类共用 canonical old∪new 负责人通知。当前 owner 命令、已保存精确证据、已回答 decision/choice 及来源更正审核保持原语义，不建额外证明或队列。先用真实 SubmitMessage/native turn/owner correction 重现，再局部修正；根第九轮全检因这些缺陷主动停止，未完成，不作通过或产品失败。本候选独立审查、正式全检仍待完成。

r15 定向回归已完成：真实 queued 分类、queued/withdrawn 嵌套完成证据、named main preview 及 owner old→new 更正均先复现再修正。负责人原批次实际 review/commit 后回到空闲，再由 owner 更正产生两项可领取通知；同请求重放不重复。App/tools 定向 race、vet 通过，API schema 再生成无变化。原 main original/repair 回复分类不额外唤醒、owner 当前命令、跨 Issue 显式资料及旧 queued successor 收尾回归保留。候选尚待独立 review/正式完整检查，未合并或部署。

## r16 准入续接修正候选

formal r10 独立review已确认的两处真实caller：冻结manager续接仍受本项实际native receipt围栏；named main的当前配置不可用不应饿死原冻结repair。沿原serviceable head、snapshot和receipt事实最小修正，不重发未知或回落legacy。先真实回归，独立review/正式全检仍待。已确认的child.close业务效果与原生收尾回执分别记录，父项不增加子树物理native静止门槛；实际业务/worker unknown和迟到激活仍沿原规则。

r16 边界结论：manager 不同 ordinary native receipt 先于冻结 continuation 豁免检查；named main 当前配置缺失时，只让已注册到期的原 frozen repair/recovery越过无法准入的新输入，普通foreground仍保持原优先/延后。新named main准入与native冻结同样保留真实receipt-only对账，未核清不借fallback发新turn；runtime读取原pin以便对账，legacy未选named原路径不改。root与独立评审不采纳原blanket子树native endpoint门槛：已经确认的child.close不是业务unknown，父项关闭不删除原回执；迟到child激活仍被terminal/ancestor guards拒绝，worker实际unknown仍被现任务事实阻塞。关闭事务已有parent wake，不能再用纯native尾部阻塞父项并要求不存在的二次wake。

r16 真实回归已通过：不同ordinary B pending receipt不让A repair抢先进入，B正式对账后原A可领取；main当前配置缺失时原冻结repair可实际PutMainTurn starting/started，新foreground仍pending。main fallback同样在普通receipt未核时仅恢复原pin读回执，核清后继续。原assignment batch真实review/commit后再验证repair次序，不改变正常优先。App/mainthread定向race与vet通过，原三个pooled generation/disabled/deferred回归及现parent关闭/child激活矩阵保留；独立review与正式完整检查待执行。


# P4 r17 实际来源/对账接点修正

以 formal r11 固定86file（base bfb0ffff；其固定manifest/index/patch记录在BASE）为前件。root正式源码/旧freeze/live P9不动；Attention和Task B1保持各自独立source，不混入本片。

当前root要求先做实际caller红例的四项：

1. 已围栏旧epoch的普通delivery仍sending，native事实已completed/rejected时，runtime只读选择须能恢复原冻结执行作Reconcile，不重新准入native业务；finish_assignment仍等精确原sending/unknown回执。沿原pool/reconcile而非新队列。
2. manager background_sources读原batch材料也须复用OwnerInputAdmitted/withdrawn，与现issues_read/context/task source同规则；保留owner原文/原历史、receipt与实际read/checkin身份，不新建资料授权证明链。
3. main给原logical自己的叙述分类不制造无wake的Issue source duty；仅抑制wake不足。复用实际message→native→logical归属；后续owner显式选择相同材料仍产生真实review duty/wake。保持资料关联/分类事实，不把原narration当owner授权。
4. 原ordinary turn/start回执丢失、starting还无native turn ID、outbox unknown时不得把owner原文当已消费；真实消费receipt确认的历史/steer仍可用，owner直接修正不受agent准入限制。

原CLI第一项关于父manager issues.cancel的建议经root/独立reviewer核定不采纳：原用户合同明确branch cancellation先阻新派发并收束子项执行。manager对本人Issue显式cancel沿domain.Cancel/applyDecision正式级联是此合同的窄特例，并非任意跨Issue task控制；不改child assignee/epoch，unknown保留，finish_cancel仍等真实收束。只澄清文档并保留原branch取消回归，不增加子manager确认/权限工作流。

先实际caller正常正负例证明，再最小复用既有source predicate、runtime snapshot/outbox和事务；不扩恶意owner、本机管理员、多租户/proof系统。测试仅受影响定向race/vet/生成边界，不fullcheck/CLIreview/model或服务操作。当前只写计划，代码/测试尚未实施。第一次建副本时Python tarfile不支持filter参数，在提取前报错；改用已逐路径核验的本仓库git archive提取，非产品测试失败。


r17四项正常caller已真实复现后窄修：保原sending snapshot只读对账、补background_sources源投影、同logical叙述不新造technical duty、ordinary unknown starting无消费回执不准入；原owner命令/原pending duty/消费确认历史不变。本人Issue branch cancel的原领域级联符合用户合同，原CLI对该项建议不采纳，仅文档明确不等于任意跨Issue task控制。定向验证与独立复审仍进行，未正式纳入/部署。


r17隔离目标检查已通过：App/mainthread定向race及vet/目标staticcheck；四真实红例均已绿，原branch取消、unknown和已确认历史保持。当前仅待固定artifact独立复审与root正式组合检查，不作为CI/合并/部署结果。有限来源目录核对保留单独审阅证据，未扩展未来服务或owner证明机制。

r17 正式纳入补记：独立 reviewer 已核对八文件增量及八十八文件完整候选，四项正常 caller 修复无新增 findings；固定第十一轮规定 CLI 与本次增量复审共同关闭其已采纳意见，不将旧 CLI 称为检查了新字节。第十一轮完整检查自然结束，App 的两组未知后台回执发现用例失败，mainthread 的具名主会话完成等待超时；原失败日志保留，先在 r17 上复现并判断原因，再运行最终完整检查。旧交叉编译通过仅属于其冻结候选。


## r18：receipt-only 空队头与 named transport fixture

formal r11 全检已自然结束并报告两组 receipt discovery 失败及 named main 完成超时。r17 隔离原样定向执行重现三组失败；不是扩大 timeout，也不把单次重跑绿色视为修复。

receipt-only 会话已经没有普通 outbox 队头，legacy store.HeadOutboxPrioritized 返回包装的 ErrNotFound；PendingAgentConversations 直接比较导致提前返回错误，未到原 dueReceipts 分支。仅在此接点使用 errors.Is 保留原收据发现、持久 30 秒退避及重启语义。不改变请求/receipt 或其他队列读取入口。

named fixture 的实际工具回执明确拒绝 conversation_associate：该工具已不在 named main 提供目录。专用 named fixture 改为调用 conversation_associate_issues，再读取精确原消息版本并 commit；不恢复退役工具、不修改 legacy fixture、不添加全局技术 baseline。模型/effort、start/resume、维护摘要和原冻结 snapshot 断言保持。

先完成上述真实定向回归/race/vet，再冻结供独立复审。原正式全检失败、原诊断与本候选验证分别保留；当前不声明正式全检、CI、合并或部署完成。


r18 隔离验证已完成：三组原始失败先原样重现；两组 receipt-only 原回归在局部包装错误修正后通过，named main 的旧工具拒绝有实际回执，专用 fixture 沿 canonical effect wrapper 后 start/resume 正常完成。受影响 App/mainthread 定向 race、vet、按仓库版本运行的 staticcheck 通过；原普通 receipt 阻新业务、旧 epoch 只读对账、配置代次与失败退避回归保留。协议/生成类型及迁移无变更。当前固定候选待独立复审和正式完整检查，未合并或部署。

r18 正式纳入：独立 reviewer 确认局部错误判断和真实 named 工具测试修正，无新 findings；原模型/effort、恢复及维护摘要断言保留。规定第十一轮 CLI、r17 与 r18 独立增量共同关闭代码 review，最终完整检查单独记录，不重复无变更的全量 review，也不沿用旧失败检查作为通过证据。


## r19：维护摘要保留未知历史而不准入新来源

正式完整检查发现两个同源失败：普通 owner lost-start 的 turn 仍 starting、outbox 已 unknown，r17 正确不把其视为已确认消费，但维护摘要将隐藏正文后的整条记录删除，连真实 message ID/unknown 状态都丢失。仅修私有 handoff 摘要投影：真正 unknown 的原请求正文作为 `unconfirmed_owner_input` 历史引用保留，明确未确认消费、不能授权或重放；普通 queued 仍不进入摘要，withdrawn 仍脱敏。当前 agent read/source、Issue 作用域、原 turn/outbox/receipt 与派发状态均不修改；没有新增公共协议或持久状态。两个原例继续验证未知原文/状态、手动/idle 摘要和原请求不重放，另核普通来源仍拒绝及撤回脱敏。当前隔离定向验证中，正式整检与交付另记。


r19 正式验证完成：私有摘要修正和实际 MessageControl revision 撤回测试经独立审查通过；完整 mainthread race 及定向 App race 先行通过，随后正式 r13 完整检查自然退出 0（App 2802.092 秒，Web 45 文件/281 测试及全部工程检查通过）。Windows/macOS 交叉编译通过。完整检查日志 SHA-256 `4fc97ee4fd4606fefddc5984fa562e8efecc1f38ffece4d8e975d114f92001df`，源码冻结保持一致；此后只更新状态文档，PR/CI 与部署分别记录。
