# 历史声明与定义当前状态的只读投影

唯一写入者：本 Task 的指定 implementer；原指定 reviewer 独立复验。
本计划先于显示修复编码。Refs: D183。

## 来源与起点

当前候选 `df9fef5db657604d2194fa481c3e40dc99745f90` 保留 spec3 装配补丁、
九份冻结原件和 `2aa0678b` 的退役声明兼容修复。
原 Issue owner 在当前授权范围中澄清：已接受的历史声明继续保留，但查询应明确呈现
能力定义当前已退役。该项是定义/声明/查询/API/CLI 闭环的正确性，不增加准入条件或 UI 范围。

正在进行的完整检查属于澄清前候选；先对本轮核验身份的检查协调器发送 SIGINT，保留回执、
停止原因和进程收束证据。它不作为显示修复后的最终候选检查，也不记成新的产品测试失败。

## 最小方案

1. 在 `AgentCapabilityView` 增加可选、类型化 `definition_statuses`：每条明确保留声明的
   exact reference，另给定义当前 revision/state 及说明。退役说明明确历史声明和冻结任务
   仍保留、新引用被拒绝；缺失定义单独表为不可获得，不猜测 active。
2. 只在只读 views 派生该信息，Agent 当前 list/show 和 Task explanation 的候选共用 helper。
   每个查询按 Capability ID 缓存当前定义读取，数据库错误照常返回。分页签名包含当前定义，
   定义修改后旧页 cursor 明确拒绝，避免混用两版状态。
3. `declared`、原 inventory/snapshot/revision、spec、pin、required、scheduler 和
   `current` 的精确声明匹配语义保持。`current.state=eligible` 表示既有冻结需求的匹配，
   不表示可给新任务添加退役引用；新引用仍走原事务中的 active 校验。
4. API/CLI 现有 list/show/explain 输出完整对象，新增字段沿其类型化响应传递。
   使用正式生成器同步 schema 和 Web 类型。现有 Web 未消费这组业务 Capability 投影，
   无需新增 UI 或进入私有 Issue 页面；原始声明历史端点继续返回当时接受的原件。
5. 更新 D183 和 Capability 使用文档，说明声明、当前定义状态与冻结匹配三个事实。
   不新增迁移，不改生产协议、工具探测、原预算、装配测试或运行部署。

## 必要验证

- active→retired 在 Agent list/show、HTTP/CLI 及 Task candidate 的新字段明确可见，
  历史 exact 声明仍在，原匹配状态不因投影改变；定义变更使已有分页 cursor 失效。
- 原 Task 在退役后仍按冻结引用领取，投影前后 spec、pin、binding、原 inventory 不变。
- 退役后无关 Agent 更新、历史声明恢复与 request replay 仍通过；新增声明/新 Task 的
  退役引用仍拒绝，复用原有回归并实际运行。
- 针对新字段执行定向检查及生成器，再在最终提交上完成适当完整 `make check`。
  Sandbox 600 秒、fake429 90 秒、resolver 5 秒及 spec3 的 25+25/2/55 秒保持。
- 原失败、停止证明和 changes_required 保留；报告记录 owner 来源、精确 head/base、
  实际通过和未验证项。独立验收、merge/deploy 仍分开，不重启生产或联系第三方。
