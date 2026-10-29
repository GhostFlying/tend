# Issue definition fields and owner read adapters（B1 首片）

Refs: D121、D122。此片基于已冻结的 canonical Issue、Attention、P6 对话、archive、named runtime 与 B0 类型，补原定义字段和非可见读取；不代表 owner canonical 元数据投影、旧公开入口退役或最终 UI 已完成。无新存储关系或永久双模式。

## 定义与冻结合同

现 IssuePlan 增加 optional scope:string 与 completion_criteria:string[]；constraints 保持 background/user_constraint。create/update/view/history 沿原聚合和不可变修订。新 task 冻结将范围和 Issue 级责任收尾条件映射到现有带来源/版本的 Facts；后者明确不是 task Acceptance。祖先相关语义变化要求明确刷新合同，已开始 run 和已有 spec 保持冻结；标题变更不加门槛。manager 沿同一现定义/继承只读路径读取，不扩大祖先控制或来源审核份额。

字段均 optional，旧有效 JSON/历史可读。已有 worker/runner wire 没有新必填或默认语义变化；新增定义经既有 Facts 字段交付，不复制领域 spec 或要求协议升代。关闭沿现有证据和未结责任，不新增 checkbox/proof 状态。

## 非可见适配

Issues controller 增加 context、followups、followupHistory、deliveries 独立 WorkItemRead，沿既有 refresh/stop。followup 创建使用 Issue revision 与 assignment epoch，规则控制使用 rule revision 与 assignment epoch；不在重试时刷新并覆盖原 Envelope。category 写沿现 CAS；archive 只读/原件/资源下载走归档专用 URL，旧 ID 不当成当前业务 ID。全部复用 ApiClient/WorkItemRead，不新增 cache、timer 或 SSE。

本片不做 archive 导入业务编排、owner 消息 canonical DTO 或新可见页面，不修改共享上传、draft、useDurableOp。显式导入仍需后续复用上传→当前 source→新 Issue 的真实返回身份。

## 验证与状态

必要验证为定义 current/history/冻结原 spec、祖先语义修改与显式刷新后的实现/独立验收链、读取切换迟到和关闭、生成 schema/TS。此片独立候选实现中；正式 review、完整检查、CI、合并、部署和最终线上验收未完成。


## 后继 canonical owner 元数据片

从现 ConversationItem 提取正文/媒体公共内容，旧 facade 和 typed Issue owner 投影共用。GET conversation 的过渡 projection=issues 读取与关联 payload 使用 canonical ID；content/association CAS、作者时间、原件、已存在问题身份保持。最终 UI 消费者同批切换并退役旧默认活动入口，不保留永久模式；raw 历史和 runner wire 不变。独立验收仍由原 caller 选择上下文，此展示共享不扩大执行/验收读取。


## 元数据片实际边界与进度

owner conversation/agent-conversations/message associations 增加明确 projection=issues，canonical IssueContext.sources 使用同一 typed IssueConversationItem；正文、媒体、作者、顺序、时间与旧有效 DTO 的 schema 属性保持，关联/background 使用 issue_id。owner conversation.set_issue_associations 复用已实现实际命令、原 content/association CAS、owner更正锁和去重。历史 Choice 原样只读，新 Attention 不复制。非可见 issueConversationReader/issueAssociationCommand 复用既有 reader 和 durable Envelope，无新 cache/queue。

此片组合已独立 clean 的 r14 actual-source/repair 依赖，原 B1 stable definition/controller 首片也已独立 clean；本片新 owner DTO 尚待独立复核。实际 HTTP 上传/owner 关联 CAS 与重放/旧新消息内容相同/原件下载 exact bytes 的定向 race 和 TS 检查已完成，完整检查/CI/合并/部署未完成。活跃 named contextbuilder 的旧提示与 owner task primary 专用投影仍为后继收口，不据此宣称 B1 全部完成；最终旧活动入口退役须与 UI 消费者同批切换。


canonical owner 投影只将实际 control row 对应关系呈现为可打开 Issue；旧关联的原 disposition/revision 保留，过滤后空 Items 不虚称已关联当前 Issue，legacy chat/background 不填伪 IssueID。旧默认/raw 历史响应和正文媒体不改，不回填或新增兼容证明框架。UI 按真实 Items 是否存在决定当前 Issue 链接。
