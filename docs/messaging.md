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
