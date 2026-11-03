# 跨 Issue 的显式不可变产物共享

## 背景和范围

已验收调查产物可能属于独立 Issue；另一个协调者当前读取其 artifact 或 file 会被正确的
Issue 边界拒绝。不能以重新调查、复制正文到伪来源或全局放开 artifacts_read 代替授权。
本批增加对精确产物的显式只读授权，不授予源任务控制、源 Issue 上下文或外部执行权限。

## 权限与持久化

- owner 可指定 source artifact、目标 Issue 当前 revision/assignment epoch；主协调者仅能
  从真实已接纳 owner 来源及实际托管主会话发出同一命令。普通 Issue manager 不得为自己
  任意获取源产物，不允许转授权或通配符。
- 在同事务登记 artifact ID、源 Task/Issue、原 ref 原文 SHA256、文件元数据及目标 Issue
  assignment epoch。共享不修改 artifact、acceptance、task spec 或历史文件字节。
- grant 有独立 revision；创建校验目标 Issue CAS，撤销按 grant CAS 并保留历史。目标
  owner 换代后旧 grant 自动不适用，需要新显式 grant，不静默继承到新身份。
- 活跃 grant 只开放该 artifact 的只读详情和其固定文件集合。源 Task 全量读取、兄弟
  artifact、非选中文件及 Issue 控制仍拒绝。manager download 使用真实当前运行/Issue
  身份核验。撤销阻止后续新读取；不声称撤销已经下载的内容，也不改写已经冻结的 Task
  inputs，输入快照属于当时合法派发的事实。

## 入口与并发

提供具体 CLI grant/revoke/list/show 命令与完整结构化输出、筛选/分页、request 恢复说明，
同 PR 登记 owner API、能力目录及主协调工具。所有变更经 app 的去重/单事务/原请求重试。
公共类型与路由放新文件，现有登记仅最小插入，提前与 Agent UI 和 messaging writer 协调。
新增迁移0086与其它 writer 的0085连续；不改已提交迁移。Refs: D164。

## 验证

真实 App/Store/工具/RPC fixture 覆盖：原 forbidden→显式 grant→精确 artifact/file 可读；
未选中 sibling/task/source 仍拒；错误 hash、目标 epoch/CAS、撤销/换代、重复请求、同请求
不同 payload、owner 来源未接纳与普通 manager 自授权拒。固定输入的实现/验收只读文件
仍按原合法 spec，原 acceptances 和源字节不变。先 target race 与独立审查，再正式 full/CI；
生产保持停机，全部修复合入后统一 config-only fresh 部署。

## 依赖与具体合同

迁移0085已通过 PR221 合入。最终分支真实依赖 PR234 的冻结父
55a8815987ed463a29a232fefe2f86979836c058，再追加0086；不复制父代码或迁移。
该父集中修正 Main source 准入为真实 native input 身份。本分支在同一共享 gate
保留父全部操作并增加精确 grant/revoke，用实际分类历史来源的反例同时验证这两个
入口；当前 native 来源仍可成功，原成功 request 回放不改变。旧独立 full 仅证明旧
冻结组合，不代表新 helper 组合已完整检查。

artifact_shares.grant/revoke 以目标 Issue 为 Envelope target，要求 Issue CAS 与当前
assignment epoch；revoke 还要求 share revision。grant 冻结 source artifact 的 ref SHA、
digest、source Task/Issue 与精确 file manifest；新的 grant 不改变源 artifact 或授权读
源 Task。每次共享变更推进目标 Issue aggregate revision，事件与请求结果同事务。
独立 share_id/revision 保留历史，list/show 返回原来源、状态及当前 epoch 适用性。
主 Agent 仅可 relay 当前 turn 准入的未撤回 owner 原消息/内容版本；Issue manager 不
可调用 grant/revoke。已有读取仅增加 exact artifact grant 检查，不按源 Task 泛放行。
附件仍沿消息来源授权，不在本批把附件或目录伪作 artifact。

独立审查补全不存在引用的失败合同：缺失目标 Issue、source artifact 或 revoke share
必须映射为稳定的 not_found 并在原事务登记失败请求；原 ID 重试回放同一失败，不能
退化为 internal/无收据。真实数据库/IO 错误仍向上返回，不误报资源不存在。

第二轮独立审查补全文件的双重身份：已发布为会话附件的 artifact 文件仍是同一不可变
file。manager 下载先校验该文件元数据是否命中当前精确 artifact grant；没有 grant 时
继续原附件消息来源授权。发布不会扩大其它附件/目录权限，也不能使已授权报告变得
不可下载。真实发布回归覆盖未授权、grant、撤销及已冻结 worker input 的原权限。

授权消息仅为精确 share 的审批 provenance，不自动授予该消息及其它附件的一般材料权限；source/revision 保留在 share 与控制 revision 的 reason 原文，不能借一般 source_message_id 关联绕过精确授权或撤销。回归包含 admitted Main 授权消息中的其它私人附件，grant 前后及 revoke 后均拒读；原冻结 input 的独立权限不变。

## 新任务输入的撤销边界

第五轮实测发现：只限制直接读取仍可由 manager 在撤销后把 file_id 首次写进 spec.inputs，随后 worker 以新 spec 读取。生产输入冻结仅有 CreateTask 与 UpdateIntent 两个调用点（canonical/托管 manager 入口复用它们）。统一在首次绑定输入时按实际 coordination execution 的 Issue/assignment epoch 验证精确当前文件权限，与 ManagerFile 使用同一只读事务 helper；owner/system 的原显式权限保持，当前 manager 的同 Issue 文件/有效跨 Issue grant 仍允许。已由同 Task 旧 spec 冻结的输入不重复授权，不追溯撤销已合法冻结事实；新 Task、首次加入现有 Task 或删除后重新加入均须当前权限。拒绝与 request record 同事务，不能残留 Task/spec/inputs；原失败 ID 重放稳定。

回归覆盖原真实撤销绕过、有效 share 的 manager 新建与改 spec、撤销后新建/首次加入拒绝、同请求回放、撤销前 snapshot 仍下载原字节，保 owner 普通附件与同 Issue 输入，以及跨 epoch 拒绝。该产品 guard 不由旧完整检查代替。

## 发布文件的独立权限并集

第六轮真实复现显示：同 Issue 的已完成交付文件发布为 Main 会话附件后，附件来源拒绝会遮蔽原 Task primary Issue 的独立权限。共享 resolver 必须分别保留 own finalized artifact、合法附件来源与 exact live share 三种授权；一种权限不适用不代表另外两种也被撤销。先核真实 finalized deliverable 及 Task primary Issue，属于当前 Issue 时直接沿其原文件身份授权；否则继续附件来源/精确共享，真实数据库错误仍传播。发布不重写 frozen input metadata，不使 foreign revoked 文件成为 own artifact。加入真实同一 manager 发布前后建 Task 与直接下载正例，并与原跨 Issue 撤销/新绑定拒绝/旧 snapshot 字节用例同族验证。
