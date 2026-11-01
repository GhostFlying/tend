# 通信 API 与 Telegram（D131）

## Web IM 与诊断入口

新 IM 使用 `/api/v1/messaging/`；legacy 主 Agent 页面继续使用既有 conversation/rollout。通信记录引用相同消息和附件，只有输入、正式发布与面向用户的系统通知进入 IM。原生 prose、tool call、内部唤醒与静默检查保留诊断用途，不自动发送 Telegram。

`GET channels`、`channels/{channel}` 查看能力与绑定；`GET messages` 支持 `after`、`conversation_id`、`issue_id`、`channel_id`，每页最多 100 条并返回 next/more。`GET messages/{id}`、`{id}/receipts`、`{id}/trace` 查看正文、已读/投递以及原生执行和委派工作。`GET events?after=<event_seq>` 是 bearer 鉴权 SSE，事件名为 ledger；start 返回起始游标，重连沿最后事件 ID 继续。查询不产生阅读确认。

写入经 `/api/v1/ops` 的统一 Envelope，操作名为 `messaging.send/reply/notify/read/bind/unbind`，均带 request_id。重试沿用原 request_id 及原 payload；binding 还携带 expected_control_revision。正文和附件沿既有 attachment API 上传/下载，不传本地路径。

```json
{"body":"请跟进这件事","attachments":[]}
```

`send` 默认主助理；`reply` 的 target.id 是被回复消息，Tend 解析原责任。Agent 的正式 conversation_reply 提交 reply_to 和附件，不能指定渠道地址；通知用 conversation_notify，Tend 默认投 Web 和已绑定 Telegram。owner CLI 的 notify 保留 owner 作者，不能冒充 Agent。

Web 显式阅读 payload 为 `{"content_revision":1}`。投递、阅读与业务处理分别表达：API accepted 不是 Agent 已读，已读不是业务完成。Agent 阅读有原生输入或来源工具证据，提交后立即推送事件，不等最终回复。Web 阅读 Telegram 来源只产生 Web 阅读记录。

## Telegram 配置

服务端配置引用本机凭据环境变量：

```json
{"telegram":{"token_env":"TEND_TELEGRAM_BOT_TOKEN"}}
```

实际 token 放在受权限保护的本机 env file，不进入配置投影、账本或日志。Telegram adapter 使用标准 Bot API long polling；服务端须能够访问 Telegram API。已有 webhook 不自动删除，启动时明确报告冲突。

```sh
tend channel telegram bind --user-id <numeric-user-id>
tend channel show telegram
```

随后由绑定用户私聊 Bot 发送 `/start`，端点变为 ready。只支持该 owner 的 private chat，不支持群聊、Business connection 或 /issue。引用回复通过保存的外部消息关系定位事项。未知引用明确拒绝，不改投主助理。首次配置及后续解绑不会补发历史通知。

支持文本、照片和文件。标准 Bot API 下载上限为 20 MB；发送文件上限为 50 MB，照片按相应限制处理。Tend 保留下载原件及 hash，Telegram 照片是平台提供的版本；需要完整原文件时请按 document 发送。相册逐条处理，语音/视频等未支持内容明确提示。

原始更新先持久化，再确认 offset；媒体准备完成才唤醒 Agent。消息更正保存为新的输入并关联 correction_of，旧消息与既有业务效果保留。文本和附件分片逐项记录。发送结果 unknown 不自动重发，重启不会把缺失回执当作发送失败。

普通 Bot API 不提供 Bot 主动标记输入已读或用户阅读回执。Tend/Web 的 Agent 已读仍即时显示；将来支持此能力的 adapter 使用独立 MarkRead 控制路径，不排在媒体上传之后。渠道已读同步失败不回滚阅读事实。

## CLI

`channel list/show/telegram bind/telegram unbind` 管理渠道；`message send/reply/notify/list/show/receipts/trace/read/watch` 使用通信模型。发送内容用 `--file` 或 `--data`，`watch --after` 输出可恢复的 SSE JSON。写命令 stderr 打印 request ID，供未知结果后的相同请求查询/重试。binding 重试必须同时提供原 `--revision`；read 重试必须保留原 content_revision。

## 后续事件来源

未来连接器先关联业务对象，通过领域用例更新 Issue/交付事实，再在同一事务登记来源和按需 manager 唤醒。现有 Issue 状态变更与 background batch 是接点。此版本没有 PR/CI/GitHub 连接器、状态映射、通用规则引擎或公开任意状态补丁接口。

## IM 内回答（D134）

Web `/` 是正式通信入口，`/diagnostics/main` 保留原生主 Agent 页面，具名诊断会话继续使用 `/chat/:id`。两种入口复用消息与执行账本，但各自保存草稿和发送队列。新标签页可恢复此前暂存的 IM 草稿。

`GET messages?latest=1` 返回最近一页，`GET messages?before=<next>` 向前翻页；页内仍按时间升序。原有 `after` 向后游标继续供 CLI 和断线补齐使用。通信事件同时通知问题状态变化，列表读取不产生阅读确认。

消息的 `interaction` 引用原 decision、action attention 或 worker 问题，包含该版问题、依据、允许选项及处理状态。Web 控件、Telegram inline keyboard、引用文字均调用原业务用例。`messaging.respond` 的 target 为 interaction ID，payload 含 `version` 与 `option` 或 `text`；人工行动的完成说明还须显式 `confirm: true`。该操作只用于 owner，不增加 Agent 选渠道的能力。

引用问题回复完整选项或编号可明确选择；完整选项与编号相同而语义冲突时优先完整选项。原始输入正文仍按原文保存，解析出的业务选择另行记录。含新条件的控制回答交当前负责人继续协调，不能默默重试旧要求。人工事项的普通提问不会标为完成；Telegram 可引用问题回复 `已完成：具体完成说明`。

一个渠道正式受理后，其他渠道的原问题失效并显示实际处理渠道、答案和时间，原始问题版本保持不变。Web 保留已经输入的草稿；Telegram 对失效问题的迟到文字仍保存原文并解释状态，不再次执行业务动作。回调 ACK、阅读和普通输入受理均不代表已回答。

Telegram 原消息编辑走独立、持久化的控制投递，避免排在附件后面；无法编辑时追加一次关联提示。`near` 与 `due` 提醒保留各自约定时间窗口的说明，共用原问题控制。正式回答受理、后续执行和渠道展示同步继续分别记账。
