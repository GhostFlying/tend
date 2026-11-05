# 启动 hold 与未知原生压缩的审查修正

依据：D150 的有界启动失败窗口与既有未知原生操作的独立对账约束。本计划不调整测试 deadline，也不将 named 工具链等待截止归因于本次两项审查发现。

## Q1：检查任务环境的独立与继承形式

当前任务环境会裁剪 GOFLAGS 的外层空格，断言却要求 ` -p=N` 前导空格，导致独立直接入口误报。先在 GOFLAGS 为空的明确环境复现现有根，再按字段解析最终包并发值，覆盖独立与继承 flags。仅修正断言，不修改 package 预算、测试根集合或实际任务环境。

## Q2：未知原生压缩独立于较新输入的启动 hold

已有 `agent_compactions` 的 starting/running/unknown 是原生事实，即便没有 main turn 或 native turn pending 行，也必须独立恢复会话读取。先用原 compact 命令、原 operation/thread 与 outbox request 创建未知事实，再耗尽较新输入预算，确认其 hold 不能阻断原压缩完成事件的发现。

在同一 runtime 选择快照中，优先于普通 serviceable input 发现该会话的未知 compact，以原 compact outbox request 作为 reconciliation attachment；不把较新输入改为 reconciliation，不清除其 Attention，不再次发送 compact 或原业务输入。已有原生回执和具名冻结执行仍优先，其他会话的未知 compact 不授予豁免，queued/已结束 compact 不授予豁免。完成事件只结算原 operation/thread 的事实，结算后较新输入仍受自己的 hold 约束。旧记录无法关联 outbox 时保留只读发现，不捏造 request 身份。

## 验证与交付

先提交本计划，再补最小 app/mainthread 回归与 Q1 环境断言，按资源所有权运行有限 RED/GREEN。保持正在执行的旧候选 full 自然收口、其源码和失败证据不变；新修正不借其结果宣称通过。修正后更新 D150，同一 PR 继续按新精确候选的 review、CI 和必要完整检查门禁处理，不另开重复 Draft。
