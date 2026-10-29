# Issue 表面与主会话 UI 接入

依据 D121、D123–D125，用户已批准主会话布局及随后十二张桌面/手机图稿。沿现 React/Vite、信息侧栏、三入口和共享详情覆盖层逐组接入；整体黑白/浅灰/细边框，仅语义状态保留少量色彩。不新增导航壳、缓存、轮询或业务状态来源。

## 实施顺序与边界

1. M0：主会话/header/composer/sidebar/手机入口的已批准增量，保留真实消息、附件、模型/effort、发送队列及滚动/焦点行为。既有动态内容不能硬编码为图稿示例。
2. V1：canonical Issue 白板/递归层级/详情/定义编辑/责任交接，以及同一 Attention 的 Inbox 决策/人工动作。复用已存在的 Issues/AttentionData、OwnerCommand/DurableOp；已读/收起不结算，unknown 核实原请求。
3. V2：公有类别/Agent目录、任务每role筛选、main/Issue/独立咨询、手工来源与精确run补充/Orca原生信息。登记、观测与在线分开；已有工作区固定host/backend。普通咨询不建任务，明确执行意图经现正式委派路径。查看原生信息不接管，owner管理动作走原控制合同。
4. V3：只读历史归档/原件/缺口与所选材料新建当前Issue。历史ID不冒充当前ID，专用archive下载后按实际来源保存链引用新对象；无恢复旧业务操作。
5. 活动owner字段投影、canonical context/discovery与旧入口最终退役由配套B1实现协调。只接已经存在的接口；遇到真实合同缺口先记录并推进独立表面，不用复制DTO或客户端重算补齐。

本片只拥有可见Web组件/CSS/交互与相关UI测试、UI计划/设计记录；backend、生成类型和共享非可视controller由相应片段负责。读取统一使用已有LiveData/WorkItemRead与SSE/reload/stop；写请求使用原Envelope/revision/question version及持久化DurableOp，unknown不换ID。

## 验证与交付

按完整caller分组冻结，必要组件/数据动作测试与TypeScript/lint/format；使用已安装Playwright只读fixture验证当前桌面与手机，截图后view_image逐组对照已批图。覆盖真实交互、loading/空态/离线/错误/等待/unknown、返回/焦点/响应式与原图查看，不仅以构建通过作视觉验收。Browser插件不可用时记录Playwright fallback，不使用真实部署业务数据测试。

状态：本轮页面候选已完成桌面与手机隔离浏览器 caller 检查，准备冻结供独立预审，尚不代表全部 UI 完成。Web 类型、lint、格式、296 项测试及构建通过；共享附件独立复审已通过。整组页面独立预审、完整仓库检查、合并、部署和真实模型验收尚未完成。Attention 最晚行动时间/提醒已接固定后件，owner task canonical 关联投影已接固定后件，最终组合仍待核对，不将旧工作项 ID 当作当前 Issue ID；任务相关旧读入口保留真实标识，待后件统一切换。

## 共享附件接点（本片拥有）

复用既有 SendQueue/Outbox 的 File 持久化、上传 request_id、attempted/receipt 与核实流程；只增加明确的 `conversation.inject_source` 提交方式及可选本地队列作用域，普通 `enqueue` 仍只发送聊天消息。手动来源、Attention 处理原件、档案导入均注入真实来源，不伪装 ordinary owner message；Attention 的最终确认引用真实新 source_message_id。上传未知时保留原文件/请求，不另建上传状态机或缓存。ComposerDraft 增加卸载关闭，停止续租/监听并释放原租约；只清本实例资源，不删除未知内容或回执。

所有权：`queue/sendQueue.ts`、`queue/outbox.ts`、`conversation/draft.ts` 与对应测试，新增 UI hook/来源表单。共享controller/DTO仍由B1负责。验证真实上传丢回执→重开→同请求核实→一次来源效果，以及原聊天附件测试；不改变API、schema或后端语义。

## Owner 会话元数据接线

本片经协调负责 owner UI 路径的 Issue 类型接线与固定 issueConversationReader 注入，包括 live/data、conversation/model/queueView/transcriptBlocks/draft/draftStore/interrupt 与会话 api。引用后端冻结生成类型/薄helper，不复制 DTO、不改 LedgerEvent wire、上传或请求去重。队列与 transcript/block 的 canonical 投影由后端补齐后导入；历史 linked 分类没有当前目标时只展示历史分类，不制造 Issue 链接。共享附件 r2 独立复审已通过；其冻结原件保留，完整页面仍需浏览器及独立验收。

## 本轮预审范围与保留项

已使用隔离本机 fixture 验证 1440×1000 与 390×844：主会话实际 canonical 关联、流增量/重连、Issue 树/定义/跟进、任务要求与已保存回执、责任交接 unknown 核实及物理重载、Inbox 决策/人工动作原文与原文件、咨询身份、来源上传丢回执/拒绝后编辑恢复、精确 run 管理与补充、类别编辑、档案原文/缺口/所选来源新建。网络失败只允许测试显式注入的请求故障；无未解释页面运行异常。图稿示例数据不作为实际业务事实。

当前档案 API 提供全档案 originals，界面明确显示“全部原件”并与选中历史记录分区，不猜测历史 Issue 附件关联。任务详情/列表/lookup及Markdown已消费固定 canonical owner task 投影；旧材料 facade 仅保留原历史入口，历史 Choice 回答仍保留原问题/版本身份。Attention时间/时区/提醒读取固定后件，时间缺值明确未定，计划处理时间不冒充最晚行动时间。离线定义编辑恢复及交付刷新等未在本轮浏览器覆盖的边界需在后续验收补齐。

## 本轮预审 r2 修正

独立预审采纳四项真实接点：定义编辑冻结进入时的 Issue snapshot/revision，远程刷新保留草稿、保存按原 CAS 拒冲突，用户可明确载入最新定义替换草稿；变更摘要显示实际新增/删除/修改内容；任务表单收紧 desktop 字段布局并固定创建/返回操作区，手机正常滚动及键盘可达；来源在提交前显示实际目标，unknown 始终按原请求目标显示。既有 DurableOp/request 语义不变，不新增缓存、草稿数据库或状态框架。

固定 r1 浏览器红例已证实：v1 草稿期间 SSE 读到 v2，原控件旧 intent/criteria 保存时却带 v2 revision，导致被接受并覆盖；r2 必须用同一实际 caller 验证拒绝旧 CAS、草稿保留、显式重新载入后的新 CAS。附加离线/投递刷新/当前和档案原图下载/手机焦点证据独立保存，r1 原件不修改。

Attention 时间固定后件已独立复审通过，本轮仅精确导入其 schema/TS：NextSummary、主会话处理卡和 Inbox 读取真实 latest_action_at/timezone/reminders。计划时间仍单列，不推导替换；缺值显示未定，不新建客户端定时器/缓存。owner task 投影仍等待固定后件。

同轮补充独立预审采纳：所选历史 run 的工作区必须读取 NativeEntry 原 run 的 workspace_id/path/provider/instance，不用当前 TaskDetail.workspace 替代。实际浏览器红例已确认 start_over 后新任务工作区与旧 session 混在同一卡。修正仅消费既有 NativeEntry 字段，无后端/权限/新provider，原补充请求和管理合同不变。

同轮收口另外两项已复现正常 caller：历史页进入时立即读取当前 Issue 的默认版本，切换版本/Issue 或离开时关闭同一原 history reader，渲染明确匹配 Issue 与版本，避免全局历史值串页；背景与输入仍合法的 images-only 来源直接复用既有 MessageImages，在 attachments 为空时显示，原图/下载沿 conversation/images scope，无新API/媒体状态机。

## r3 恢复草稿与 Task 投影

恢复的定义操作必须把真实原 Envelope payload 与 CAS/epoch/task_dispositions 保留在组件编辑状态，原请求核实为 stale 后仍保留已提交草稿；只有明确载入最新定义才替换为新 snapshot。不合成未知历史 IssueView，不改共享 DurableOp 或新增账本。验证 v1 离线提交、manager v2、物理 reload、原 v1 stale 的组合，并保留 unknown 正控。

随后仅导入固定 owner task schema/TS，使用真正 primary_issue_id/title 与 issue_ids 接任务列表、详情、lookup 和当前 Issue 入口。旧工作项字段保留历史语义，不作为当前 Issue fallback。原 spec.inputs/shared_context/文件scope不变，无新 cache/poller。固定后件仍需独立review及最终组合，不把源副本当已部署。

本片固定 Task8 后件已独立复审通过，实际 TaskLookup/任务chip/所属Issue/返回Issue/Markdown确认读及Board当前与历史区别已通过隔离浏览器两尺寸caller；任务完成后关联Issue改名同样复用原失效读取，host无关事实不新增完成任务刷新。全部UI候选仍待独立delta复审、最终业务组合与真实验收。


## r4 活动旧 facade 退役

活动工作区旧路线与专用 board/detail/history/followup/delivery controller 按 `ui-legacy-facade-retirement.md` 移除，不保留旧 query 重定向。旧材料 facade 的无 caller watchMaterials 读取集合一同移除；当前 TaskInputs 与 TaskDetails/TaskPeek 真实读取和文件 scope 不变。Choice 继续原 detail/respond/版本/回执核实，旧关联只显示原 ID，不以当前 Issue 或档案身份替代。共用 WorkItemRead 原实现保留，不做全项目术语改名。


## r5 空态待处理入口

复现desktop全部已解决/空态或摘要读取失败时无Inbox入口：NextSummary仅有条件条目触发onOpen，手机入口在desktop隐藏。保持已批主界面布局，将既有“需要你处理”标题改为常驻button并调用原onOpen()，保原文字/灰色/布局与mobile TabBar；提供明确aria-label和focus-visible。无新导航、reader/cache或后台合同。验证r4红例、r5三种desktop状态鼠标与Tab/Enter均可打开原Inbox、读取真实已解决事项/提醒；mobile仍用原tab。


## r8 既有能力与浏览器 CI 收口

按 `issue-browser-ci-migration.md` 恢复项目/已结束发现、当前与历史完整定义、通用跟进与交付unknown原请求核实，迁移实际八个CI命令到canonical合同；不恢复活动旧facade。仅原共享DurableOp表只读枚举与原LiveData失效分支扩展，未引入cache/poller/授权框架。与r7类型清理组合后提交；当前仍为隔离候选，尚无真实后端浏览器验收、CI/merge/deploy结论。


### R9：真实 Inbox 长文本换行

真实只读浏览器中，含连续文件/产物/验收标识与哈希的事项标题在手机详情及列表超出容器。仅给既有 Inbox 区域增加连续文本换行和最小宽度约束；保留完整原文、身份、布局和操作。桌面与手机使用相同原内容核对标题、列表与提出者身份宽度，不以截断/摘要掩盖。新构建的独立 fixture 回归与运行中真实部署验收分别记录；不重启活动任务或修改账本。
