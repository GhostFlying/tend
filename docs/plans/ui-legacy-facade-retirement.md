# 活动旧工作项界面退役（UI r4）

Refs: D121、D123。前件为已冻结 UI r3；新部署的历史账本通过专用只读档案访问。本片只删除旧活动业务入口，不把旧身份映射为当前 Issue，不新增重定向、兼容别名或第二套状态。

## 最小范围

1. Shell 仅保留真实 issue/task 详情路线；移除 work_item query 恢复、WorkItemDetails 与 openWorkItem/discussWorkItem 服务。旧 query 不触发业务读取，也不自动打开当前 Issue。当前 task 输入/文件、canonical Issue 和 archive 不变。
2. 历史 Choice 继续使用原 choice_id/version、原 respond 请求与核实/原 ID retry、原答案效果；没有真实 canonical 关联时只显示旧原 ID，不编造 archive 或当前 Issue 链接。把既有 ChoiceResponse 实例 Map 从退役 facade 移至 LiveData，保持同生命周期与原读刷新。
3. 删除仅旧 facade 的 Board/Details/WorkItemRef/Followups 及其专用读/action controller，Materials 仅保留仍实际使用的 TaskInputs。保留共用 WorkItemRead 原实现与当前页面现有生命周期，不因类名做全项目改名。
4. LiveData/connector 去旧 facade 的 refresh/stop/invalidation，canonical Issue/Attention/Choice 原 reader 仍工作。移除无调用的旧 composer workItem 提案入口。
5. 测试仅退役随旧功能失效的用例，共享 read、Choice unknown 同请求、当前 canonical followup/任务文件行为继续验证。旧活动页面的浏览器脚本与对应命令同时退役，保留当前 Issue 的真实浏览器验证。

## 验证与边界

定向测试覆盖 Choice 问题版本/原请求与迟到读取、共享 reader 中止/合并读取/失败重试、canonical followup 失效、当前任务关联与输入。隔离浏览器覆盖旧 work_item query 不读取当前旧 API、Choice 仍真实回答和同 ID unknown 核实，以及当前 Issue/task/archive 不受影响。类型、lint、格式、构建必要检查；不跑整仓 fullcheck、不使用真实账本/模型/部署。r3 原件不改，独立 r4 delta 冻结后评审。

后端公开旧路由/tool 注册退役由配套后端片段负责；`work_item_choices.respond` 与原 Choice detail 必须保留。本片不修改 raw LedgerEvent、协议、生成类型或历史迁移。


## r6 退役生成类型前的独立清理

实际rg核对：WorkItemView仅残留于无活动caller的concern/stageNeedsReview纯函数及其旧测试fixture、ExecutionSummary测试取得undefined execution的中间对象。移除这两个deadhelper与两个旧断言；splitID用原合法ID常量，ExecutionSummary直接接受undefined counts，保留实际ExecutionCounts行为。保留轻量阶段计算、原Choice/TaskInputs与历史wire。不复制DTO/别名、不改当前渲染/布局/交互，generated由后台最终根合同正常生成，当前小片不接moving源码。只做相关model/execution测试、type/lint/format及构建字节对照，不重复浏览器/全仓检查。


## r7 固定 owner 合同组合

后台正常生成移除旧owner roots后，实际类型检查发现background.ts仍联合旧TranscriptBlock、其测试声明旧类型、queueView测试仍声明旧ConversationQueueEntry。showTranscriptBlock唯一产品caller为canonical transcriptBlocks，无raw/archive调用；本片直接改用IssueTranscriptBlock/IssueConversationQueueEntry，无alias、字段/断言/运行行为改变。先保留真实失败日志，再执行固定schema生成一致性、最终Web type/test/lint/format/build；不重复已通过浏览器，无服务/账本操作。r6原件不改，2生成依赖与3路径修正分开可审。
