# Owner → 主代理当前回合 steer 调查与验证

## 当前链路与原生接口

本次从 task workspace 的既定基线调查。owner API / `SubmitMessage` 已有持久原文、来源、reply-to、choice 与 request 去重。阻塞发生在主会话 dispatcher：`Deliver` 持有串行投递直到原生 turn 结束。新增 inbox 保存本身不能解除这个边界。

主会话使用 TraeX app-server。0.211.1 的 `--experimental` 生成 schema 确认 `turn/steer` 要求 `threadId`、`expectedTurnId`、`input`，只返回 `turnId`；没有 Tend request-id 去重字段。适配必须区分明确参数/方法拒绝与内部错误、连接未知，不把 internal RPC 失败当作安全重试。接口按原 turn 接受实际 user 输入，无需另启主代理。

## 隔离事实

- 真实 hosted 端到端使用本任务新建账本、Manager、dispatcher、Tend tool service、原生 app-server 与真实模型。原回合第一次 `repos_list` 未返回时发送补充；原生接受发生于原 turn 未结束，匹配 user-item 回执在工具安全边界后出现。最终回复使用补充要求的新结束标记，并保留原目标标记及 owner message ID。
- 澄清、状态询问和停止文字分别复现：澄清调整要求，状态询问保留原工作目标并完成原请求，停止要求等待工具安全完成后结束指定工作。每个独立场景均保持一个 native session 与一个原 turn。
- 全过程只打开一个原生会话，原 turn ID 保持不变，只调用一次 `repos_list`，没有 task 写入、worker 运行或测试授权扩大。空闲后的显式 owner 消息在同一 thread 创建第二个 turn。
- 真实原生事件循环接入确定性本机 Responses 端点，在回复流式 delta 期间及输出 item 临近 final 时接受连续两条 steer。两条输入按顺序成为独立 user item；下一次真实原生模型请求包含两条原文，仍属于同一 turn。原 turn 已结束后的 steer 明确拒绝。
- 原生接受而尚未消费时结束测试自身进程，再恢复同一 thread 并显式开始新 turn。后续模型请求与 user item 中未出现前次未消费补充；Tend 的恢复策略仍保留 unknown，不依赖此版本的瞬态队列行为猜测。
- 故障测试覆盖 outbox/withdraw 互斥、handoff 屏障、其他 thread/turn 不能消费、accepted 不等于 replied、未知不重发、工具 native replay 固定 source/request，正式 decision option/reply-to 与回答 source 只解析一次。事件循环单独推进回复焦点，工具 callback 可先保存原生回执而不会把较早的回复关联到较晚输入。
- 连续 decision 答复与状态询问需要显式工具来源选择，避免后来的问题成为较早答复的审计来源。未消费、撤回和系统来源不能选择；原生迟到 item 则从已保存记录恢复 reply-to。停止回复请求可以通过补充消息定位其原始回合。

## 验证边界

真实模型端到端通过 `SubmitMessage` 用例入口驱动完整 hosted 链路；HTTP 认证与消息信封入口沿既有 API 回归检查覆盖。生成中、临近 final、连续输入和恢复使用真实 app-server 的确定性模型端点以控制时序；工具期间的方向调整另有真实模型验证。它们共同证明接收、消费和行为变化，不能把确定性端点单独作为模型理解或生产上线证明。

本轮没有生产发布、服务重启、生产消息注入、维护模式变更或 owner 凭据获取；也没有修改并行 worker 消息任务的资源或 runner/worker 投递模块。后续独立验收应使用自己的任务环境和会话复现，并检查最终提交与上传日志；本文件是实现方调查记录，不代替独立 verdict。

## 已入账验收的修正

原验收证明相同规划 commit 在消费状态询问后换 native call ID 可再次建任务，不能仅以同 key 冻结来源证明业务去重。现 hosted 回调在切换实际来源之前，从持久 turn 投递或尚在 turn/start 的 active 初始投递固定 planning source；事务仍使用既有 plan_commits，但键的来源保持初始消息。实际 owner payload、parent request、任务/spec 和工作项绑定不改为初始来源。无原 turn 证据时工具拒绝执行。回归模拟丢失成功结果、消费状态消息且事件循环尚未推进、新 call ID、显式选择两种来源和新 Manager 恢复，检查原请求引用、任务次数、新序号实际来源及跨工具序号去重。

准备失败发生在 native 调用之前；Web 现区分准备失败、明确原生拒绝、不支持、schema 未确认和原因未识别的等待。隔离 PNG 原文件不可读的回归检查原生调用为零、消息原文及 pending outbox 仍在。

D107/D108 正式依赖已线性整合，steer 决策改为 D115；没有吸收并行 worker 投递任务的未交付代码。新验证日志与报告记录本轮实际验证，不以原实现记录替代独立验收。
