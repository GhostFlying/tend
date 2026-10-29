# Issue 活动入口最终收口

本计划先固定已经独立审查的 P5/P6/P8、A23、B0、B1 owner/queue/message/Task 及 Attention 最晚行动时间，再三方纳入 P4 r15–r18。源消费规则、旧 epoch 原 pin 对账、receipt-only 退避、prepare 未启动退出通知及普通队头优先不因组合丢失；历史迁移、原 Task/Run/Spec 和 runner JSON 不改。

## 当前最小可构建切片

1. 按真实调用核验新 canonical context tool 的正文投影：共享 `views.IssueContext` 已调用 `HideUndeliveredOwner`，typed owner 映射只复制既有正文，不重复加过滤。真实 named 读取断言 queued/withdrawn 不作为已准入原文，owner/store 原文仍可读。
2. Attention 的无 Issue 规则经原 Sweep 产生真实 followup.changed；main context delta 必须按现 followup target 读取实际 Attention，不能用空 WorkItemID 查询假 Issue。保留同 occurrence 和主 Agent 路由，读取不回答，不增加服务/定时器。
3. owner CLI 的 update-task 初次读取只使用真实 primary_issue_id，不再把原 primary_work_item_id 当当前 Issue；缺 canonical 责任明确拒绝，显式原请求/revision/epoch恢复不额外刷新。
4. 新 named manager 只发现 canonical `issues_context_read`，移除该角色遗留 `work_item_context_read`；上下文包为 canonical Issue 使用真实定义/状态/树和实际 `issues_*` 工具提示，legacy 历史保留历史身份，不能把所有 opaque ID 变成可控制的 Issue。

## 最终共同退役清单

- owner HTTP 旧 work-items/context/followup 活动路由（原 Choice read/respond 保留）、旧 owner mutation 注册与 stale CurrentWorkItem；需在固定 UI 全部切换同批退役，不在新消费者仍未接通时先删。
- default tools 的旧 work_items 定义/生命周期/link、context prepare/fact、choice/followup/refresh 注册和旧主规划职责；最终只由实际 canonical main/manager 调用。原 domain/app 历史 fixture 不以别名伪装成新业务，旧 worker 通信适配单独保留。
- owner CLI 已以 issue 子命令调用 canonical 用例，不存在另一个 work-items CLI 可虚构退役。task frozen wire 中旧字段属于不同通信边界。
- UI 由独立候选拥有，后端不编辑其 controllers、队列/上传/草稿或 JSX/CSS。按固定 UI caller 清单核对后才完成最终删除；不保留永久双写产品模式。

## 通信与验证

owner 接口/工具切换不自动改变 server/runner 协议；当前保留原 wire N/N−1、历史事件和迁移。若真实 peer 字段、必填条件或旧有效语义改变，必须完成升代、适配与退役登记，不能借空业务账本跳过。

验证先从实际 App/工具/contextbuilder 调用复现，再定向 race/vet/schema 与旧 peer 字节边界；不重复全检，不操作真实服务/P9。组合候选独立复审与 root 正式完整检查分别记录，不将候选编译或字段生成冒充最终产品验收。


最小片当前实际结果：r15–r18 与 B1/Attention 前件已三方组合，定向 race/vet/静态检查通过。无 Issue Attention 的真实跨到期 Sweep 首先复现空 ID 查询错误，按原 target 修正；typed context 正负断言证实共享 view 已正确保护 queued/withdrawn，无重复生产过滤。CLI 合同刷新仅使用真实 primary Issue；named manager 目录与实际直接调用均退出旧 context 入口。canonical full/delta 显示实际定义/生命周期，并沿原事件订阅收取 owner 更正，legacy 历史回归不变。旧 owner/default-MCP 整面退役与固定 UI 后片仍待完成，本片不冒称全部产品入口已经退役。最小组合独立 review 已通过，后片退役独立 review 与最终正式全检待执行，无部署或真实服务操作。


正式最终 review 四项 caller 缺口另以窄修正收口：owner 原子创建已登记负责人并沿原manager会话/wake启动；归档来源创建保留实际content_revision；information不计行动义务；任务首run到达即可查看，显式历史选择不变。原请求身份、业务权限和布局不改，见 final-ui-contract-review-fixes.md；旧检查与此修正的验证分别记录。
