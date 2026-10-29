# Owner task canonical Issue 关联（D122）

## 实际读取入口与最小实现

- `views.SummaryWithExecution/SummariesWithExecution` 汇合 snapshot.tasks、GET tasks 的未完成/finished/all分页、GET tasks/{id}.TaskSummary 与 ops stale currentTask；TaskLookup 从这些实际 summary/detail 构建，没有独立后端 lookup 端点。
- 当前 `store.TaskIssueLabel` 读取 immutable task_work_item_owners 对应当前标题；`views.Task` 用 TaskIssueResponsibilities 填 work_item_ids。canonical no-stage已有真实primary，次级成果引用不是责任。仅实际 issue_controls 行可转换成当前可打开的 Issue 身份；历史opaque ID不改名、不回填。
- 在原 TaskSummary/TaskDetail 的真实声明增加 optional primary_issue_id/primary_issue_title 与 issue_ids；同共用只读 view 按现控制行填充。优先原声明最小扩展，不复制 TaskDetail/Spec/Run 或增无消费者 alias/第二缓存。旧 work_item 字段在过渡保持原响应，最终消费者切换批退役活动旧字段/入口；历史原值继续档案语义，不把旧ID叫当前Issue。
- 同一个 summary 投影函数确保 snapshot/list/detail/ops.current 一致，现UI live/data summary→lookup复制须由UI唯一owner改为同新字段；此片只生成TS并交固定合同，不抢写UI-owned controller。
- TaskInputs只读冻结 spec.inputs/file/source，无Issue语义不改。旧Materials(WorkItemView)只属旧Details，新Issue已有材料tab，不因目录命名改合同。Workspace/run/spec/raw history与真实文件字节保持。

## 必要回归

真实 canonical no-stage create task，在 snapshot/list/detail/current stale均同primary_issue_id/title；父子与次级成果引用不变primary，新标题读取最新；真正legacy task仍原 work_item 原值但无伪canonical字段，冻结Spec/Run/输入/产物原字节不变。分页cursor/执行事实沿原路径。沿当前owner API/CLI操作，无owner证明/权限服务/新表。

## 当前活动 facade 的最终退役清单

1. UI Board 的 primary_work_item_*、shell Details 的 work_item_ids/WorkItemRef、Markdown/shell splitWorkItemIds 真正canonical解析须由UI换现Issue ref；无法确认canonical的历史字面保持文本/历史阅读，不制造可点击当前Issue。
2. main/Issue manager named工具与context现canonical分支保留；旧active work_items_*、conversation_associate、background legacy管理入口与owner /work-items、旧CLI work-item命令在现UI读取/写入切换的同批移除/明确历史只读，不提前删导致可见调用失败。
3. owner task snapshot/list/detail/ops.current/lookup的新字段齐全后，最终UI批统一去活动旧字段依赖；runner通信、task frozen spec/run、已提交SQL和immutable档案不随命名退役删除。
4. 现Raw LedgerEvent无schema改写。UI已通过真实canonical HTTP重读元数据，不能把raw旧ID就地改IssueID。是否仍有活动旧调用需在UI最终freeze后列具体caller再删，不能预先声称退役完成。

## 前件/状态

本片基于Attention19固定（独立review待完成）与先前detail10等完整union。r15/r16独立已审但未组合；正式集成必须三方保留其source/admission修复。当前先写计划，后精确实现与目标检查；不改root/P9/所有旧freeze/未确认旧活动入口，不跑全库完整检查。

本片实际实现直接扩原声明与共用view，不复制Task/Run模型：optional `primary_issue_id`、`primary_issue_title`、`issue_ids`。真实HTTP snapshot/list/all分页/detail/finished及stale current同值；owner改当前Issue标题读最新，legacy secondary link不改primary，纯历史task无伪Issue字段。owner正常取消后finished路径仍保正确关联，原spec/run字节保持。目标race、vet、staticcheck、生成一致性和TS检查通过；独立review/最终组合与正式检查仍待完成。Attention前件已独立CLEAN；最终组合仍须保r15/r16/r17，UI唯一owner接lookup拷贝和可见链接，本片没有写其controller。
