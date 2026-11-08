# 新 session 索引交接与按需上下文加载

Refs: D196（替代 D81 的摘要交接与自动摘要，调整 D17/D18 的托管注入方式）。

## 目标

Main、Issue 管理和咨询会话的交接及正常 turn 自动上下文只发送索引，分别限制在 2 KiB UTF-8；原文由已有只读工具按需读取。当前消息、身份与权限、准入、版本绑定、审阅提交及 unknown 对账保持原语义。独立手动 compact 保留，不改变 impl/acceptance 的 session 边界。

## 实施

1. 新增 conversation_context_index、owner context-index API 与 CLI；state/sources/facts/threads 分区分页，默认 20、最多 50 条；不返回正文、不确认审阅。rollout 支持 thread_id 精确筛选。
2. handoff 只保留前一 thread、可选 rollout 位置、conversation/Issue、generation、水位及查询入口；不携带摘要、账本或祖先链。移除无关全局事件导致的重复刷新，保留创建/注入/切换状态机和原生未知结果对账。
3. Main packet 与 AgentContext 的自动注入替换为有界索引；后者仍执行准入和 context revision 绑定。索引水位只表示通知，不代表已读或已审阅。compact 后仍恢复索引。
4. 删除摘要定时器、模型 fork 和合并流水线；退役摘要准备及设置 UI/CLI，旧写请求明确拒绝，旧配置不能重新启用摘要。保留历史记录及只读审计；旧未知交接先对账，已确认注入的目标按原记录恢复。
5. 同步生成协议类型、能力目录、设计与使用说明。旧持久化数据保留，迁移仅追加；不改变 server/runner 线上信封。

## 验证

覆盖大正文与多代 thread 的 2 KiB 上限、无 summary/implicit compact 模型调用、按需恢复原件、Issue/咨询/未准入消息隔离、原文更正与撤回、未知原生请求和崩溃恢复、审阅与 CAS 不被索引交付绕过。运行 Go/Web/API/CLI 专项测试、完整 make check 和独立 review，交付 PR；不部署。

## 静态收益

历史样本首次输入：Main 28.6–30.9k，Issue 209.4–218.7k tokens。两个入口精简后的预算分别为 18–20k 和 28–31k；通过离线 tokenizer 对实际索引作差量核验。按需查询成本与首次节省分别报告，不将首次下降比例当作账单收益。Tend 摘要请求归零单独统计。固定 runtime/memory/skills 不在本次裁剪范围。
