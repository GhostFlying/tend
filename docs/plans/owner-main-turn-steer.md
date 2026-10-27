# Owner 消息送入主代理当前回合（D115）

## 调查结论

- `SubmitMessage` 在同一事务持久化正文、来源、reply-to、decision choice 和主会话 outbox。主会话 dispatcher 每次 `Deliver` 等待原生 turn 完成，故仅有持久化并不代表当前回合收到消息。
- 主会话原生适配为 TraeX app-server；已安装版本 0.211.1 的生成 schema 有 `turn/steer`，必须携带 `threadId`、`expectedTurnId` 和真正的 user input，回执仅返回 `turnId`。没有 Tend request-id 去重字段，不能把超时当拒绝重试。
- 工具回调在独立 goroutine 执行，现有 planning 的 native identity、turn commit_seq 和原始 source 不能因新消息而重置。steer 不需要修改 runner 或 worker 投递路径。
- 临近 final 可能发生前置条件拒绝；仅明确拒绝可以回到队列。不能以完成通知推断所有已接受输入都已消费，须核对原生 user item 回执。

## 实施计划

1. 增加可选原生 `TurnSteerer` 能力和 schema 校验；实测原生接受、user item 和工具期间的边界。仅 TraeX hosted 路径支持，其他执行器保留排队。
2. 增加独立持久 steer 尝试记录。由当前 `Deliver` 的单一循环领取待处理 owner 消息，不开启另一协调者，不越过 handoff、不领取系统事件。领取、消息控制修订和投递 identity 同事务；先写发送意图再调用原生。
3. 接受、原生消费、失败/未知分别记录；未知不重发、不误投后续 turn。重启先对账稳态记录，原生无足够证据时保持 unknown。保留原消息和引用，不伪造业务完成。
4. 原生 user item 消费后更新当前消息上下文，但已开始的工具保留原 source/request identity；assistant item 在开始记录时固定 reply-to，最终回复须覆盖当前回合已消费的 owner 消息。decision/choice 审计沿现有工具路径。
5. 增加 Web 准确状态和原因、使用/运维限制；更新设计 D115。新增迁移，不修改历史迁移；不改变 server/runner 协议。
6. 验证持久化、连续输入、reply-to、重复投递、final 竞争、明确拒绝、未知与恢复、工具次数及既有后台/decision 机制。隔离真实 app-server 的确定性模型端点验证边界，再运行真实模型的主会话链路；mock 只作为故障测试。
7. 运行 `make check`，完成本地用户身份提交，提交报告和可复现原生证据。不发布、不重启生产、不推送、不建 PR；独立验收由 Tend 后续运行承担。

## 文件边界

主会话、owner 消息账本和 Web 投影为本任务所有；原生通用适配增加单独 steer 文件及可选 interface。禁止修改 worker/runner 消息模块，也不读取相关任务工作树。共用适配需要后续集成时按交付提交协调。

## 连续输入的来源补充

一个回合可能先收到 decision 答复，再收到状态询问。工具只默认绑定最新 native user item 会把正式回答指向错误消息。因此 hosted 工具 schema 增加可选 `source_message_id`，由主会话在调用既有工具前选择并冻结实际来源；仅当前输入或账本已准入的 owner 消息可选，排队/unknown、撤回、系统及 worker 来源不能作为 owner 授权。既有业务工具、权限和 worker schema 保持原约束。停止回复的消息 ID 可以是同回合已接受/已消费的补充消息，控制仍投向原 turn。
