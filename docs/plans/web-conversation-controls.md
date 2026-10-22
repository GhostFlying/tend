# Web 主会话设置与流式正文

依据 owner 新需求：Web 可修改托管主会话的模型及 thinking effort，并边生成边显示回复；不实现 Max mode。原阶段 F 的设备安装验收范围继续排除添加为 Web App。

## 决策与边界

- D73：模型目录来自托管 harness，展示当前有效模型及其支持的 effort；设置服务端持久化并跨设备共享。控制写入使用 request_id 去重及设置 revision 校验。设置用于下一次开始处理的回复；正在处理的轮次固定启动时配置，离线发送队列也在开始执行时取设置。改变模型时以目录支持值校验 effort，不猜测能力；worker 配置不受影响。
- D74：更新 D69 的完整消息限定。正文使用稳定 item/message 身份，按有限频率发布累计正文；读取会话可恢复部分正文，SSE 沿已有账本游标续传。结束、停止、失败和服务端重启保留已生成内容并标明状态。最终完整消息覆盖累计正文，不产生第二条消息。不得每个 token 写一条新消息。

## 实施顺序

0. C0：冻结 owner API 的设置视图、写入 payload 与流式消息状态，并生成客户端类型，前后端据此独立实现。
1. C1：核对真实 traex 的 model/list、turn/start model/effort/provider 与 agentMessage delta 协议；扩充受 schema 检查的手写类型，生成 schema 子集。
2. C2：新增只追加迁移与共享设置读写，owner API 读目录/设置，去重和 revision 校验；轮次保存生效设置并显式传给 harness。更新 D73 和协议生成物。
3. C3：接入增量正文、有限频率累计发布与持久化，稳定身份及终态恢复，更新 D69/D74 和协议生成物。
4. C4：前端由原 F7 子 agent 实现输入区模型/effort 控件、多端设置同步、流式正文和状态；只使用生成的 API 类型，不做 Max mode。
5. C5：针对目录与能力、并发设置、处理中改配置、重启、片段顺序/最终覆盖、停止保留正文和断线恢复进行测试；真实模型与生产构建浏览器联调。
6. C6：make check、独立 review、CI、rebase 合入；部署前重新构建 schema 兼容回退并验证，再做已合入版本部署与独立读回。更新验收记录，区分自动化与 owner 真机反馈。

所有 orca 测试在结束或失败后清理本次创建的 workspace 与终端，先保存产物和验证证据，并独立读回确认清理；不操作其他会话的 workspace。

## 接口契约

已核对当前 traex 的真实目录与 JSON Schema。接口冻结如下：

- `GET /api/v1/conversation/settings` 返回 `ConversationSettingsView`：`enabled`、`available`、`revision`、目录模型 ID `model`、`effort`、`models`。每个模型含 `id`、`label`、`supported_efforts`、`default_effort`。未启用隐藏设置；暂不可用禁用修改并有限退避重读，不伪造有效模型。
- `conversation.set_settings` 的 target 为 `conversation`，信封 `expected_control_revision` 对应设置 revision，payload 为 `model`、`effort`。成功或 stale revision 后重新读取设置；`conversation.settings` 事件触发重读。无可调 effort 的模型只显示其默认值。
- 正文仍用 `conversation.message` 账本事件携带累计 `ConversationItem`；新增 `assistant_state`：`streaming`、`completed`、`interrupted`、`failed`、`unknown`。稳定 `message_id` 和 `seq`，`assistant_revision` 随累计正文或状态更新单调递增，客户端按 revision 合并，避免延迟旧片段覆盖较新快照或终态；不增加 SSE 类型，不改变历史分页游标。

## C3 实现约定

- migration 22 记录 thread/turn/item 对应的唯一 message 身份、正文状态及单调 revision，正文仍保存在 conversation_messages，不改变 seq 与历史位置。
- 主会话事件循环以 100ms 为批次保存累计正文；完整 item、工具出现前及轮次结束时立即保存，保持会话先后顺序。正文先落账本再经 SSE 读取，重连与刷新读回相同内容。
- 完整 item 可用更短的最终正文替换草稿；迟到的累计前缀和终态后的片段不回退正文。轮次结束却缺少完整 item 时记录 unknown，不伪造完整回复；停止、失败和进程恢复保留部分正文。
