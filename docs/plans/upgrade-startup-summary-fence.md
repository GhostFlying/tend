# 升级启动迁移的摘要围栏与健康等待超时

责任：外部维护 root 统筹摘要、审阅与更新器恢复，不建立 Tend pending Issue。

状态：主线整 PR 审阅已被工具集与角色投影缺口实质阻塞，进入外部统一实施。当前受控安装已由原摘要的真实终态、候选健康回读及原批次 CAS 控制正常接续；自然恢复不是下述系统缺陷的修复。不重跑已完成批次，不改写已部署候选。

Refs: D81（工具集交接）、D156（排空与未结账摘要历史）、D166（升级 park）。相关既有计划见 `hosted-toolset-upgrades.md`、`drained-summary-attempt-history.md` 和 `manager-upgrade-park.md`。

## 已确认的因果链

1. 候选 server 启动后，主会话旧、新 binding 的 toolset hash 不同。实际新建的隔离摘要 fork 明确引用原 source thread，两阶段摘要先后返回原生完成事件，摘要 attempt 随后自然记为 succeeded；确认注入后 binding 才切换到下一 generation。证据支持本次是工具集迁移，没有证据支持遗留原生摘要需要补造终态。
2. `mainthread.openThread` 对 archive preparation、已捕获 runner manager 的 park receipt 和 receipt-only 使用原 thread 读取路径；普通升级下的 hosted main 仍进入 `openManagedThread`，再经 `migrateThread` / `prepareSummary` 生成新摘要。
3. idle 摘要检查 `UpgradePausesMain`，但启动迁移的 `prepareSummary` 没有同一检查，`app.ClaimSummaryAttempt` 的事务也没有升级准入检查。升级业务围栏建立后仍能登记并启动新的私有模型操作。
4. 新摘要和工具交接使托管组件处于 summaryActive / maintenanceBlocked 或未完成 startup。候选健康由 `UpgradeDrainStatus`、`reconcileServerCandidate` 的 MainDrained 条件驱动；未排空就没有 ServerHealthy 累积，updater 收到未被 server 确认的健康结果。一次非必要摘要因此进入安装健康的关键路径，并消耗原固定窗口。
5. 正常 updater 的 `check` 在 authority 读取报错或 `ServerConfirmed=false` 时提前返回；底部 `HealthTimeoutSeconds` 判断未执行。即使默认健康预算已经过期，这些分支也不能给出健康等待超时的明确分类。批次阶段截止时间仍可能转 attention，已有候选仍可按原身份对账，不能把停止授权到期解释成可重新启动或可回滚。

## 必须保留的事实边界

- 默认摘要模型预算为两分钟；`retry_at` 的十五分钟是重复尝试退避，不是运行租约或成功证明。摘要清理和终态写回另有有限 cleanup context。
- `conversation_summary_attempts` 的 started 是持久尝试记录；当前表没有该 attempt 对应的 fork / turn 身份。异常进程退出或清理写失败时，它可能比内存活跃状态存活更久。仅凭时间到期、原 server 退出或观察不到回执，不得直接改成 failed / succeeded，也不得重新调用原模型操作。
- 本次摘要已真实完成并落账，原 source / fork / turn 与两代 binding 都应保留。不删除行、不延长旧 grant / overall deadline、不将本次摘要终态替代其他 unknown 的对账。
- 已有 D156 只在独立组件确已排空时接纳保留的摘要历史；真实 summaryActive 或未知业务操作仍不能被同一规则假称排空。
- 当前健康仍为零的 `checking` 状态不能直接取消或凭过期 server grant 重试；既有 D180 也没有采用当前候选并强制提交的入口。不能把“复用独立维护”写成这一状态已经具备的恢复保证。若候选始终无法取得健康，实施时须明确核验并补齐有限的外部前向恢复路径：固定原候选与未知操作，证明需要替换的实际进程退出，保留已迁移数据和原操作，再以独立授权安装真实修复产物；不得伪造 committed、修改 Current 或重置原期限。

## 最小后续修复

1. **统一新摘要准入。** 在 `ClaimSummaryAttempt` 的同一写事务内复用升级及恢复围栏判定，防止 idle、启动迁移、显式预备摘要及输入重新 claim 等路径在围栏生效后创建新私有模型操作。外层检查只用于快速返回；事务检查封闭检查与升级 start 之间的竞争。围栏前已开始的摘要仍允许按原 identity 完成清理并保存真实结果。
2. **启动交接不依赖新模型摘要。** 围栏内跳过非必要的 `prepareSummary`，使用 D81 已有的缓存摘要、当前账本事实包和原历史回查索引继续工具集交接；明确标注摘要缺失或过期。保持原生调用的既有 creating / injecting / unknown 恢复规则，只有目标创建、注入及来源 CAS 真实确认后才切 binding，不开放旧 schema callback，不重放已处理输入。围栏解除后摘要仍走原覆盖及退避规则。
3. **先计算健康截止，再处理所有读取结果。** 正常 `Supervisor.check` 的 deadline 分类必须覆盖 authority 不可达、读取报错、绑定不符、`ServerConfirmed=false` 和普通 unhealthy。到期保留原 candidate、process、backup 与 unknown，给出固定的“需对账”状态及原因；不能因早返回再次掩盖截止，也不自动刷新窗口。保留原候选迟到健康的有界只读对账与真实两次确认，不把超时改成成功，不引入自动 kill / restore。
4. **复用合法收尾，不加本次清行捷径。** 原摘要正常终态继续由原 manager 的 `FinishSummaryAttempt` 落账；原已启动候选继续由 `TickUpgrade` / `reconcilePausedCandidates` 验证真实字节、schema、process 和健康。阶段停在 attention 时按实际 revision 使用原批次 `control retry`，截止不续期；仍不确定且需要替换时复用既有独立、有界维护及原生历史隔离规则。此次不新增一类“摘要超时即已停止”的豁免。

## 聚焦回归与交付要求

- 真实升级 fence 下启动 hosted main，旧 toolset hash 不同：不新增 summary attempt，不启动 summary fork / model turn；使用保留事实完成已有工具交接，原 source 和 ancestors 可查。
- 围栏前已开始摘要与围栏提交竞争：原操作可退出并落账，围栏后各入口及输入重新 claim 均不能再创建新摘要；重复事件不重复模型调用。
- 摘要真实完成、模型超时、清理失败和进程退出分别保留原事实；`retry_at` 到期不能被解释为原生已停止。验证 D156 的独立 drained 与 unknown 历史边界仍成立。
- 健康 authority 持续失败、未确认、绑定不符及 unhealthy 均在原预算到期后明确要求对账；不安装第二次、不自动恢复旧数据库。原候选迟到两次真实健康仍可对账，不能无条件被过期 grant 拦住。
- 保留 source binding 的 creating / injecting / unknown、原 request 去重、阶段及 overall 截止、原批次 fresh-CAS retry，以及未变化 runner 的恢复条件。
- 健康零且永久不返回的候选在原期限后必须给出可执行的外部恢复责任与受控路径；不能只增加一个超时文案后继续无期限等待，也不能把本次依赖迟到健康的成功当作该故障已覆盖。
- 先最小故障回归，再走正常独立 review、完整检查和 CI；部署后分别验证真实安装与 Tend 自身接续。不得将本次自然成功当作修复验收。

## 实施准备：摘要准入与 D81 调用接点

以下为编码前的调查与实现接点；现已按本计划实施，最终组合验证与交付另行记录。调查基线为已合入候选 `d929e488`，tree `128901aa81268694562970a79bab310b161dff15`；下列产品文件在调查工作树中与该基线一致。实施时重新核对主线，保留其他维护文档的独立改动。

### 同一事务的围栏

`internal/app/upgrades.go` 的公开 `App.UpgradePausesMain` 当前在自己的读事务中依次调用 `a.recoveryStartupFenced(ctx, q)` 和 `upgradePausesMain(ctx, q)`。前者还包含 committed maintenance 及构造期 forward-recovery 注册前的围栏，不能只调用后者漏掉这些状态。后者已经包含 forward recovery、archive preparation、manager park，以及普通升级状态和阶段判定；保留它现有的 attention / runners 放行语义。

最小实现是在同文件抽取接受 `store.Querier` 的私有组合 helper，公开读入口与 `ClaimSummaryAttempt` 共同调用。`internal/app/handoff.go` 在 `handoffWrite` 的同一写事务中、任何 started 写入及 summary_started 事件之前调用该 helper。不要在写事务内部调用公开 `UpgradePausesMain`，它另开读事务，不能提供该次 claim 与 upgrade start 的原子顺序。

以成功 claim 的事务提交作为私有摘要操作的准入点。先提交升级围栏则禁止新 claim；先提交 claim 则是已接纳操作，由既有 workMu / summaryActive / maintenanceBlocked 生命周期使升级等待真实退出。不能把原操作的后续摘要阶段当成新业务重发，也不能在围栏生效后仅因它仍在执行便改写为 failed。`reclaimSummaryInput` 改换 source snapshot 时必须重新经过同一事务准入；被拒绝的新 snapshot 不写 attempt。

`boundedSummaryProtected` 的原 lineage 保护继续独立存在。`FinishSummaryAttempt` 与 `SaveHandoffSummary` 不增加普通升级围栏；它们负责已接纳操作的真实终态和覆盖事实。受 bounded-force 保护的历史来源仍不能被普通写回更改 verdict。

### 跳过、失败与交接引用

不新增公共请求、状态机、能力或 schema。现有 `ClaimSummaryAttempt` 的 `claimed=false, nil` 已表示覆盖、退避、snapshot 不再匹配及历史来源保护等无新操作结果；升级暂停需要保留一个精确的内部原因，避免显式 prepare-only 把竞争产生的暂停误判为失败。最小方案为 app 内部专用 sentinel（例如 `ErrSummaryPaused`），仅表示事务已确认拒绝准入；不通过对外协议暴露新的错误码。数据库错误保持真实 error，不能被折成暂停。

`prepareSummary` 优先返回已有且覆盖当前 snapshot 的缓存，即使存在升级围栏也不需要新 claim。缺失或过期缓存时，事务暂停原因连同原缓存继续返回；实际 fork、模型及清理错误保持原有失败路径。`reclaimSummaryInput` 同样传递这个精确暂停原因，原已完成本地准备但尚未调用模型的旧 attempt 按现有规则收尾，不为新来源伪造尝试。

| 调用者 | 升级暂停时的含义 | 最小处理 |
| --- | --- | --- |
| `idleSummary` | 当前无需新摘要 | 将专用暂停原因作为正常跳过，保留已有外层快速检查；不记录模型失败或改变退避 |
| `migrateOneThread` 的初次准备、prepared retarget、来源 refresh 三处 | 缺少新摘要不影响工具交接事实包 | 显式识别暂停，不作为摘要故障报警；继续调用原 `handoffPrompt`，其他真实错误仍按既有可选摘要失败记录 |
| `deliverHandoff` 的 PrepareOnly | 原 outbox 交付还未执行，应稍后处理 | 对专用暂停返回既有 `app.Retry`；保留原 request / outbox 身份，不返回 Failed 或 Delivered。现有外层检查保留，事务原因封闭检查后的竞争 |
| `reclaimSummaryInput` | 新输入尚未获准 | 向上述调用者透传精确暂停原因；不启动 fork，不创建新 snapshot 的 started |

PrepareOnly 的其他无摘要原因仍保持当前行为；本次不把所有活动、退避或数据库错误统一解释为升级暂停。原 `reconcileHandoff` 已在 PrepareOnly 无当前缓存时返回 Retry，可继续复用；它不生成摘要。

`handoffPrompt` 已完全不调用模型，直接读取 `HandoffSnapshot`、`HandoffSummary`、当前 `context.Current` 及原 `MainThreadBinding`。现有包已经携带 source thread / rollout path、ancestors、conversation / snapshot / ledger 版本、消息水位、pending 状态、当前 ledger_facts，以及缓存 summary 的 source、覆盖水位和 `summary_covers_snapshot`；无缓存以 `summary_available=false` 表达。既有提示明确摘要为引用、缺失或过期不代表覆盖全部约束，并要求不确定时查询保留 Web 会话。只需复用，不增加一套备用摘要或授权机制。

### 最小文件面与恢复约束

- `internal/app/upgrades.go`：提取共享事务内暂停判定，公开行为不变。
- `internal/app/handoff.go`：Claim 增加同事务准入和内部暂停原因；不修改 Finish / Save 的原终态权限，不新增迁移。
- `internal/mainthread/handoff.go`：prepare / reclaim 透传暂停、三处可选摘要调用与 PrepareOnly 分类；`handoffPrompt` 原事实包可以直接复用。
- `internal/mainthread/idle_summary.go`：消费事务内暂停原因；外层快速判断保留。`openManagedThread` 不增加“等围栏解除才允许工具交接”的总门。
- 对应 app / mainthread 聚焦测试；updater 的全返回路径健康截止仍按本计划前述独立条目修改与验证，不把它混进摘要结果语义。

不修改 `migrateOneThread` 对 creating / injecting / unknown 的拒绝重发分支：原请求先记意图、目标 thread 真实确认、注入真实确认、source CAS 成功后才切 binding。缓存或账本事实只替代可选新摘要，不能证明未知原生请求完成。旧 compact / native receipt 未确认仍按原路径对账；围栏内不能重新接纳 owner turn 或使用旧 schema callback。

保留 `UpgradeDrainStatus` 对 workMu、真实 active、summaryActive 与 maintenanceBlocked 的判断；只去掉围栏后新摘要造成的可避免等待，不把“有缓存”解释为组件已排空。D156 接纳的是组件已独立证明排空后的遗留 started 历史；未知历史行、原终态、source binding 都不因缓存降级而清理或认证成功。

### 真正必要的故障回归

1. app fixture 控制写入顺序覆盖 fence→claim 与 claim→fence：前者不插入或刷新 attempt、不发 summary_started；后者允许原 identity 的 Finish / Save 落账。覆盖普通升级、park / archive 及恢复启动组合 helper，复用已有围栏 fixture，不另造全局开关。
2. 真实启动迁移 fixture 设置旧 toolset hash，围栏内分别无缓存、过期缓存和有效缓存：summary fork / StartTurn 计数为零，handoff prompt 带真实当前事实与保留历史索引；创建、注入和 source CAS 真实成功才推进一代 binding。沿 `TestHandoffUsesCurrentSummaryAndFreshToolset` 扩展即可。
3. 在外层检查通过后建立围栏，覆盖 idle、PrepareOnly 和 input reclaim：idle 正常跳过，PrepareOnly 保留原交付并 Retry，reclaim 不启动新 snapshot；不把暂停写成失败模型尝试。已有覆盖或退避的含义不变。
4. 复用 `TestHandoffUnknownExternalResultNeverResends` 及 creating / injecting 恢复用例，增加围栏事实降级分支：未知 StartThread / InjectDeveloper 不重复调用，原 source / target / request 不换，binding 不提前推进。
5. 复用 `TestUnknownSummaryStartRecoversExactReceiptWithoutResending`、`TestSummaryReleaseFailureDoesNotCertifyCoverage` 和 bounded-summary 保护用例：原摘要真实完成、清理失败及 legacy unknown 的区别保持；D156 的 drained-summary-history 正反例保留。无需为本次问题重跑模型或另造历史成功回执。


## 实施准备：未提交安装操作的外部接管

本节承接健康零或普通同版本重启早退的已确认恢复缺口。沿 D180 已有本机维护命令、固定窗口、force 和接管流程补足来源与收尾，不新建另一套通用工作流，也不放宽旧模式的默认合同。

### 固定健康期限和源退出事实

- 在进入 checking 时固定健康预算与可证明的时钟锚点，重开或重复 Step 不以当前时间续期。Health 调用前、调用返回后及其他返回分支统一计算是否到期；外部读取的 context 受剩余期限及已有读取预算约束。
- 健康到期保留原原因、candidate、request、备份及一次固定的 timeout 事实。不可达、绑定不符、未确认、Inspect 未知及配置读取失败均不能掩盖到期。保存失败仍是持久化未知。
- 原候选迟到健康继续按原身份、有限单次读取和两次真实序列对账；不续安装 grant，不把 timeout 擦成未发生。永久不能确认时允许进入下面的独立外部路径。
- 复用已有 Phase、CandidateProcess、CandidateNotStarted 和 owned Wait 退出证明；仅补足原 attempt 的退出证明持久化及晚到重读。证明须绑定原 launch intent 与真正由该调用拥有的 child，不把预期 artifact hash 冒充已观察 executable。
- Find 返回零条本身不证明原启动未发生。只有精确 owned child 已退出，或原先就完整覆盖该组件启动的独占执行范围已闭合并且原 launcher 无法晚到生成 writer，才允许独立的新 launch。原业务 unknown 仍保留。普通 child 退出也不自动证明外部或原生子工作已经完成。

### 复用 D180 的来源和两端责任移交

完整检查发现固定健康截止的返回汇总误把已保存精确回滚授权、正常进入 `rollback_stopping` 的成功状态转换改成超时错误，令既有配置恢复与持久 admission fence 回归尚未执行其断言就提前失败。最小修正仍保存一次原 deadline 到期事实，但对本次已成功保存且满足原 binding、server/control revision、admission fence、rollback-safe、未接纳新业务条件的回滚转换返回成功；实际停止和恢复继续执行既有 fresh authority 与持久围栏校验，不以 timeout 授予新回滚权限。无授权、authority 未确认及普通健康零继续返回超时。同步删除 bounded runner 在持久化前只用于检查错误却未使用值的 `remaining` 局部赋值，持久化后仍重新读取原剩余期限再发 Start。复用全部原失败叶子并追加“已授权转换保留到期事实、缺失或失效围栏不获成功”的聚焦回归，不改变原截止或删除安全断言。forward-recovery 真实进程 fixture 的可读格式正例扩到 5，未来未知格式拒绝例相应使用 6，格式 1 的旧负例保留。

1. 可选的 source operation 模式固定原未提交操作的完整快照、摘要、阶段、binding、原请求、launch、实际 process 或真实退出隔离证据，并沿现有 plan 固定 generation、state/config 摘要、数据身份和主机控制 CAS。健康零的 checking 不要求先伪造旧 ledger completed 或 irreversible。
2. successor 兼容性依据实际已迁移的候选格式，不依据仍指向旧已提交版本的 Current。保留原数据，不从旧备份降级 schema。正常修复使用真实修复产物；同 artifact 只在显式同版本恢复模式下允许独立新请求和新 launch。
3. 新窗口复用现有固定总体与阶段预算。相同请求对账不重复 Start、不续时。force 只扩大明确授予的物理停止与安装范围，不把 active/unknown 业务伪造为 drained 或已完成。
4. 接管时原操作原样归档并移出 active，不能在维护 link 后再次执行旧 starting/checking。新 replacement 真实提交和关联后，以原 batch 的 fresh CAS 和 replacement receipt 结清旧安装的继续执行责任；原健康零、未知与冻结记录保持原事实。
5. 后续取消和 staged 对账识别被取代来源，不能因为旧 active 指针移走就合成“原候选尚未启动”的取消回执。原 runner 与主机责任按各自身份处理，server replacement 不替它们宣告成功。既有准入释放和逐 Run 隔离仍独立生效。

### 格式、命令与实际部署

- 复用现有本机 operator 身份、组件与进程范围、请求去重、generation/phase/state CAS；在现有 CLI 命令族同步完整 typed 输入、输出、dry-run 和原请求恢复。普通 runner grant 不获得外部 force 权限。
- 新可选模式保持旧有效 wire 请求和默认语义；若实际实施需要收紧或改变旧有效语义，按仓库 N/N−1 规则升级，不能借可选字段回避评估。
- 关键退出与所有权事实必须有旧 writer 拒绝的持久格式边界或等价证明，保留历史格式的专用读取；不能只新增会被旧 writer 忽略的 JSON 字段。确切格式与迁移范围在编码前依最终最小状态模型确认。
- 实际固定 updater 必须经适用的受控 handoff 或 updater-only continuation 更新。普通升级 committed 本身不等于已满足后者要求的 linked maintenance head；不为适配部署入口伪造 head、Current 或完成证明。仅更新 server/runner 不能作为 updater 修复已在线生效的证据。
- 未来维护发布须核对实际源码祖先和包含的业务变化；Issue 自动合入策略本身不授予业务部署。部署验收分别记录 server、runner、固定 updater 的实际字节及物理身份。

必要回归覆盖：所有健康提前返回及调用跨期限；晚到真实健康与重复序列；真实 child 早退及 proof 落盘各断点；Find0 缺证拒绝；健康零及迁移后数据上的完整 source-operation 接管、提交和责任收尾；同版本显式恢复与默认拒绝；每个物理副作用的原请求重读；旧 writer 拒绝和历史读取；真实固定 updater 受控更新。优先复用已有 fixture，不增加生产故障演练、全机空闲要求或新的 Capability 业务门槛。


## 已触发实施：共享审阅工具集与 acceptance 接续

候选完整本地检查和精确 head CI 均已成功，但原 reviewer 只能完成绑定前置核对：旧 Issue 原生线程缺新工具，共享审阅读取与 acceptance 角色的绑定投影未连通，无法开始真实整 PR 审阅。该实际平台阻塞触发本批实施；合法 pending 绑定可经既有正式接口交给原 reviewer，保留原 Task、真实 verdict、原预算和全部未知。

1. **正常 Issue 启动执行工具集迁移。** 具名 Issue manager 的业务启动与已有工具集 hash / D81 handoff 共用同一流程，不能因禁用空闲摘要而绕过。空闲摘要仍按原配置关闭。receipt-only、park、compact recovery 及原生未知保留既有原线程对账分支；旧 parser 不接新 callback。配合前述摘要事务围栏，在升级期间不创建新模型摘要。
2. **协调读取真正的共享审阅视图。** 在已有 Main / Issue manager 的具名读取工具加入共享 review 查询，复用 owner API / CLI 的规范只读视图、筛选与分页，并保持 Issue 范围校验。provider delivery 观察与共享 review 分别如实返回，不能以缺少投影冒充不存在。Main 不因此得到 Issue 的业务写入权限。
3. **角色对应的冻结绑定与提交指引。** implementation / report reviewer 使用既有 report.shared_review；acceptance 使用真实 submit_verdict 的 evidence，并明确既有 evidence.delivery_review 导入路径。普通 acceptance 的受限观察只暴露其已关联交付的实际 review、候选、来源需求与状态，不授予 owner 凭据或其他 Task 的执行权。
4. **可选 acceptance 预绑定。** 若需要在 verdict 前建立真实 round，沿已有 review start 显式选择 acceptance 角色，固定原 Task/spec、不可变产物、实际要求的原 reviewer / profile 和候选 / 需求；不再将同 Task 的实现者误当 reviewer。record 只读取实际 acceptance 的不可变 verdict/evidence，不接受调用者声称 passed。现有先 pending link、真实 acceptance 完成后再导入的合法路径保持兼容，不新增统一强制门。不得为预检失败补造正式审阅结果或重置轮次。
5. **回归真实路径。** 覆盖具名 Issue 的旧工具集恢复、新工具可调用和原未知不重放；共享读取的范围、分页与 CLI 对应；原 acceptance 的绑定读取、真实结论导入、错误模型/角色/候选/需求拒绝及陈旧结果保存；一个真实 reviewer 的初审和最多一次聚焦复核。产品修改后只做必要聚焦回归，最终一次完整检查与独立整 PR review / CI，实际部署再验旧责任接续。

实施保持旧 wire 有效请求与默认语义，若出现实际不兼容变化则按仓库 N/N−1 规则处理。源码合入、固定 updater 安装、server/runner 更新、真实 Tend 审阅与业务合入分别记录；本段不授予业务部署。

### Acceptance round 的实际结果与聚焦复核

可选 `reviewer_role=accept` 复用该 Task 已存在的实际 acceptance Run，冻结 Task/spec/产物、真实 reviewer identity 和精确 profile。无需伪造 report 或更换 reviewer。`submit_verdict.evidence.delivery_review` 保留 provider/repository/number、head/base、需求摘要、仓库检查和具体 findings；record 按真实不可变 verdict 区分通过、需修改、blocked 与 inconclusive，后两者保留为等待证据，不冒充质量通过或失败。需修改后，同 Task 的真实新产物和新的 acceptance Run 可使用唯一聚焦复核；等待证据后，真实新 acceptance Run 可继续核对同一产物，不能复用旧 verdict。旧 Run / 旧 acceptance 均不能重复计轮。普通未预绑定的 pending→真实 acceptance→导入路径保持兼容。只读投影按当前 Run 的 Task/spec/产物及原 observation scope 过滤，不泄露其他 Issue links。

## 实施与验证状态

已实现摘要事务准入、正常 Issue 工具集交接、共享审阅受限读取与真实 acceptance 结论接续、固定健康截止，以及原未提交安装的独立前向接管。关键启动身份和截止使用 format 5，普通历史读取与默认协议语义保留。聚焦故障和 race 回归已覆盖真实结论、超过一页的无关审阅历史、健康提前返回、晚到原健康、owned Wait 和原安装责任移交；最终完整检查、独立整 PR 审阅与 CI 由唯一协调入口完成，结果记在 PR。源码交付、固定 updater 安装及业务实际接续分别验收。

### 完整检查暴露的 Issue 启动兼容回归

正常 Issue 启用 D81 工具集迁移后，后续成功恢复无条件调用 history-location 写入口；runner coordination adapter 和合法无文件历史的 harness 返回空 rollout path，而该写入口正确地拒绝空索引。结果是已恢复的原线程仍无法 ready，阻断后续 completion owner turn 和原配置冻结 repair。另旧 `ReceiptOnly=true` / 未启用严格 receipt reader 的路径被错误归入正常 Issue，改变了原 resume / fallback 语义。

修正仅在实际返回非空 history index 时登记 location；空索引仍作为不可用历史保留，不清除已存索引、不伪造来源。正常 Issue 的新工具迁移条件排除旧 receipt-only，严格 receipt reader、park、compact 及 native unknown 继续先走原对账分支。App 的 location 校验、原超时、source CAS 和未知不重放边界保持。新增当前 hash + 空 history index 的实际恢复回归，并复跑原 named repair、legacy receipt-only、manager E2E 故障叶子；不扩大阶段预算或重跑完整检查。

同一入口的权限核对进一步确认，既有 main `handoffPrompt` 与 `HandoffSummaryInput` 是全局范围；直接用于 Issue 会把无关 sibling 事实及 main 摘要注入 Issue。新增的正常 Issue 迁移只复用 D81 原生 create/inject/CAS/unknown 流程，事实包改读已注册 conversation 对应 Issue 的规范只读视图、实际继承约束与 source 分页；不调用全局摘要准备，也不注入该实例曾经由 main 输入生成的缓存。main 路径不改变，不通过扩大工具权限解决。用真实 canonical Issue 与注册 conversation、无关 sibling 内容以及旧缓存验证交接包和摘要准入均不越界。

正式独立审阅确认 acceptance 等待恢复的同根死路：两个真实 blocked/inconclusive 耗尽后，外部授权将协调状态置为 changes_requested，但同产物复核仍按 mutable status 判断而拒绝。修正以最后记录的真实 acceptance verdict（及同 Task/产物）识别等待证据资格，不用协调状态猜测。两轮默认预算保持，第三轮仍必须真实外部一次授权与新的实际 acceptance Run；旧 Run/verdict 不能重计，质量 changes_required 不得到此例外。回归两次真实等待→拒绝未授权→外部授权→同产物新 Run→真实 passed。

### 实际接续中的状态回执分类

正式审阅导入后，负责人将仅说明导入完成、原候选及范围未变的回执保存成 constraint，导致原 passed 结果的当前需求摘要失效。原 Issue 计划、Task spec、候选和检查均未变化，负责人也已明确确认语义不变。沿既有 assess 留下当前适用性确认，保原结论、轮次与历史事实；不通过忽略所有 constraint 或重新审阅解决。工具指引明确普通进展、安装和审阅回执使用 observation，真实业务约束仍进入需求；单独摘要变化由负责人判断，必要时 assess，不机械修订合同。已有业务约束变化→原结果失效→显式适用确认的回归继续保留。
