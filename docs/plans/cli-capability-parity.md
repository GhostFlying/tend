# CLI 能力全集与持续覆盖（D138）

## 目标与实施顺序

CLI 覆盖全部用户业务、管理和诊断能力；Web、Telegram 提供其子集。Agent/Runner 内部执行协议继续保留角色、租约和执行上下文边界。额外纳入诊断与已有维护，不新增进程重启或配置暂存产品入口。

1. 登记全部 owner 路由、operation 与本机控制能力，建立实际命令和入口验证的绑定；写入 AGENTS.md 和设计决策。
2. 补齐读取、筛选、分页、文件、历史归档、用量及 doctor。修复 Web 已调用但服务端未注册的 artifact 详情。
3. 补齐 Issue 状态/通知、消息交互/撤回/关联、会话控制/设置、主机维护及升级控制；统一请求恢复。
4. 定向验证后冻结候选，执行完整 make check、独立 review 与 CI，提交 PR；不操作线上维护或部署。

## 命令约定

新读取提供可读输出和 --json，既有输出兼容。复杂输入支持 --data / --file / stdin。写入发送前显示 request_id，适用 CAS/epoch 不静默更新；dry-run 导出原 Envelope，重放保留身份和 payload。unknown 明确返回失败并查询原请求。文件经认证流式下载、校验后原子落盘。能力检查核对真实注册表和 CLI 分发，行为测试验证参数与结果。

## 能力对照表

下表登记候选全部实际 owner 路由、操作和纳入的本机管理入口。各行的具体 CLI 绑定、游标和输入由命令登记及对应入口测试进一步约束。Web/Telegram 按其实际使用情况覆盖子集；Telegram 消息、附件和 interaction 均复用 owner 业务能力。

| 服务端能力 | CLI 命令 | 参数与分页 | Web / Telegram | 验收 |
|---|---|---|---|---|
| `GET /api/v1/agent-conversations` | `conversation list` | search status after | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/agent-conversations/{id}` | `conversation info` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/agent-conversations/{id}/compactions` | `conversation compactions` | before | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/agent-conversations/{id}/rollout` | `conversation rollout` | after turn=turn_id | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/agent-conversations/{id}/rollout/{seq}` | `conversation window` | offset | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/agent-conversations/{id}/usage` | `conversation usage` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/archives` | `archive list` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入；真实归档/原件 |
| `GET /api/v1/archives/{archive}` | `archive show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入；真实归档/原件 |
| `GET /api/v1/archives/{archive}/originals/{kind}` | `archive originals` | after limit | Web | CLI/HTTP；读取无写入；真实归档/原件 |
| `GET /api/v1/archives/{archive}/originals/{kind}/{id}` | `archive download` | 原文件、--out；SHA256 校验 | Web | CLI/HTTP；读取无写入；真实归档/原件 |
| `GET /api/v1/archives/{archive}/originals/{kind}/{id}/download-url` | `archive original-url` | inline | Web | CLI/HTTP；读取无写入；真实归档/原件 |
| `GET /api/v1/archives/{archive}/originals/{kind}/{id}/metadata` | `archive original` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入；真实归档/原件 |
| `GET /api/v1/archives/{archive}/records/{kind}` | `archive records` | after limit | Web | CLI/HTTP；读取无写入；真实归档/原件 |
| `GET /api/v1/archives/{archive}/records/{kind}/{id}` | `archive record` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入；真实归档/原件 |
| `GET /api/v1/archives/{archive}/resources` | `archive resources` | after limit | Web | CLI/HTTP；读取无写入；真实归档/原件 |
| `GET /api/v1/archives/{archive}/resources/{id}/files/{index}` | `archive resource-download` | 原文件、--out；SHA256 校验 | Web | CLI/HTTP；读取无写入；真实归档/原件 |
| `GET /api/v1/archives/{archive}/resources/{id}/files/{index}/download-url` | `archive resource-url` | inline | Web | CLI/HTTP；读取无写入；真实归档/原件 |
| `GET /api/v1/artifacts/{id}` | `artifact show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入；真实 artifact 与权限 |
| `GET /api/v1/attentions` | `attention list` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/attentions/{id}` | `attention show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/background/{id}/sources` | `conversation sources` | after | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/categories` | `category list` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/categories/{id}` | `category show` | 按命令帮助选择身份；已有参数保留 | API | CLI/HTTP；读取无写入 |
| `GET /api/v1/conversation` | `conversation history` | before after limit | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/conversation/attachments/{id}` | `attachment download` | 原文件、--out；SHA256 校验 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/conversation/attachments/{id}/download-url` | `attachment url` | inline | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/conversation/attachments/{id}/metadata` | `attachment metadata` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/conversation/blocks/{id}` | `conversation block` | after | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/conversation/handoff` | `conversation handoff` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/conversation/images/{id}` | `image show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/conversation/images/{id}/content` | `image download` | 原文件、--out；SHA256 校验 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/conversation/images/{id}/download-url` | `image url` | inline | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/conversation/queue` | `conversation queue` | conversation=conversation_id category after | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/conversation/settings` | `conversation settings show` | conversation=conversation_id | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/conversation/transcript` | `conversation transcript` | conversation=conversation_id before | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/coordination/metrics` | `coordination metrics` | 按命令帮助选择身份；已有参数保留 | API | CLI/HTTP；读取无写入 |
| `GET /api/v1/deadlines` | `issue deadline list` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/deadlines/{id}` | `issue deadline show` | 按命令帮助选择身份；已有参数保留 | API | CLI/HTTP；读取无写入 |
| `GET /api/v1/deployment` | `deployment show` | 按命令帮助选择身份；已有参数保留 | API | CLI/HTTP；读取无写入 |
| `GET /api/v1/diagnostics/events/{id}/sources` | `event sources` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/diagnostics/{kind}` | `event list` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/diagnostics/{kind}/{id}` | `event show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/events` | `ledger watch` | after | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/files/{id}` | `file download` | 原文件、--out；SHA256 校验 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/files/{id}/download-url` | `file url` | inline | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/files/{id}/metadata` | `file metadata` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/hosted-agents` | `agents hosted` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/hosts/{id}/agents` | `agents show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/inbox` | `inbox list` | issue=issue_id kind status include-archived=include_archived after limit | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/issue-notification-settings` | `issue notifications show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/issues` | `issue list` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/issues/{id}` | `issue show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/issues/{id}/activity` | `issue activity` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/issues/{id}/context` | `issue context` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/issues/{id}/deliveries` | `issue deliveries` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/issues/{id}/followups` | `issue followups` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/issues/{id}/followups/{rule}/history` | `issue followup-history` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/issues/{id}/results` | `issue results` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/messages/{id}` | `message original` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/messages/{id}/associations` | `message associations` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/messaging/channels` | `channel list` | 按命令帮助选择身份；已有参数保留 | API；Telegram 共用业务 | CLI/HTTP；读取无写入 |
| `GET /api/v1/messaging/channels/{channel}` | `channel show` | 按命令帮助选择身份；已有参数保留 | API；Telegram 共用业务 | CLI/HTTP；读取无写入 |
| `GET /api/v1/messaging/events` | `message watch` | 按命令帮助选择身份；已有参数保留 | Web；Telegram 共用业务 | CLI/HTTP；读取无写入 |
| `GET /api/v1/messaging/messages` | `message list` | after before latest conversation=conversation_id issue=issue_id channel=channel_id | Web；Telegram 共用业务 | CLI/HTTP；读取无写入 |
| `GET /api/v1/messaging/messages/{id}` | `message show` | 按命令帮助选择身份；已有参数保留 | Web；Telegram 共用业务 | CLI/HTTP；读取无写入 |
| `GET /api/v1/messaging/messages/{id}/{view}` | `message receipts` | 按命令帮助选择身份；已有参数保留 | Web；Telegram 共用业务 | CLI/HTTP；读取无写入 |
| `GET /api/v1/next-actions` | `next-action list` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/planner-entries/{id}` | `next-action show` | 按命令帮助选择身份；已有参数保留 | API | CLI/HTTP；读取无写入 |
| `GET /api/v1/repos` | `repo list` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/repos/{id}` | `repo show` | 按命令帮助选择身份；已有参数保留 | API | CLI/HTTP；读取无写入 |
| `GET /api/v1/requests/{id}` | `request show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/runs/{id}/logs` | `run logs` | after wait | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/runs/{id}/messages` | `run messages` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/runs/{id}/native-entry` | `run native-entry` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/runs/{id}/rollout` | `run rollout` | after before | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/session` | `session show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/snapshot` | `status` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/sources/{id}` | `source show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/tasks` | `task list` | all finished project lifecycle sort before limit | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/tasks/{id}` | `task show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/token-usage` | `usage` | window offset model subject before | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/upgrades/{id}` | `upgrade show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `GET /api/v1/work-item-choices/{id}` | `choice show` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `POST /api/v1/conversation/attachments` | `attachment upload` | 文件或 stdin；--name、--media-type、原 request_id、dry-run 摘要 | Web；Telegram 共用业务 | CLI/HTTP；读取无写入 |
| `POST /api/v1/ops` | `issue create` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `POST /api/v1/session` | `login` | 按命令帮助选择身份；已有参数保留 | Web | CLI/HTTP；读取无写入 |
| `PUT /api/v1/webhook-keys/{id}` | `webhook create` | 按命令帮助选择身份；已有参数保留 | API | CLI/HTTP；读取无写入 |
| `actions.create` | `retry` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `attentions.confirm` | `attention confirm` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `attentions.create` | `attention create` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `attentions.present` | `attention present` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `attentions.respond` | `attention respond` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `attentions.update` | `attention update` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `attentions.withdraw` | `attention withdraw` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `categories.put` | `category put` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `control.set_mode` | `pause` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `conversation.compact` | `conversation compact` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `conversation.consult` | `conversation consult` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `conversation.handoff` | `conversation handoff` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `conversation.inject_source` | `source inject` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `conversation.interrupt_reply` | `conversation interrupt` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `conversation.promote` | `conversation promote` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `conversation.resume_processing` | `conversation resume-processing` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `conversation.set_handoff_settings` | `conversation handoff-settings set` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `conversation.set_issue_associations` | `message associate` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `conversation.set_settings` | `conversation settings set` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `conversation.submit_message` | `conversation send` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `conversation.withdraw_message` | `message withdraw` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `decisions.respond` | `decide` | 完整 JSON；原 request_id | Web | CLI 分发与 Envelope |
| `hosts.maintenance` | `host maintenance` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.assign` | `issue assign` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `issues.cancel` | `issue cancel` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.close` | `issue close` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.create` | `issue create` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `issues.create_task` | `issue create-task` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.deadlines_cancel` | `issue deadline cancel` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `issues.deadlines_put` | `issue deadline put` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `issues.dependencies` | `issue dependencies` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.finish_assignment` | `issue finish-assignment` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.finish_cancel` | `issue finish-cancel` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.followups_cancel` | `issue followup cancel` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.followups_create` | `issue followup create` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `issues.followups_pause` | `issue followup pause` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.followups_resume` | `issue followup resume` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.followups_trigger` | `issue followup trigger` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.followups_update` | `issue followup update` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `issues.manager_migration` | `issue migrate-manager` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `issues.pause` | `issue pause` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.refresh_delivery` | `issue refresh-delivery` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.reopen` | `issue reopen` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.reparent` | `issue reparent` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.request_manager` | `issue request-manager` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `issues.request_owner` | `issue request-owner` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.resume` | `issue resume` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.review_completions` | `issue review-completions` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.review_sources` | `issue review-sources` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.set_notification_settings` | `issue notifications set` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `issues.set_status` | `issue set-status` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `issues.update` | `issue update` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `issues.update_progress` | `issue update-progress` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `issues.update_task` | `issue update-task` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `messaging.bind` | `channel bind` | 完整 JSON；原 request_id | API；Telegram 共用业务 | CLI 分发与 Envelope |
| `messaging.notify` | `message notify` | 完整 JSON；原 request_id | API；Telegram 共用业务 | CLI 分发与 Envelope |
| `messaging.read` | `message read` | 完整 JSON；原 request_id | Web；Telegram 共用业务 | CLI 分发与 Envelope |
| `messaging.reply` | `message reply` | 完整 JSON；原 request_id | API；Telegram 共用业务 | CLI 分发与 Envelope |
| `messaging.respond` | `message respond` | 完整 JSON；原 request_id | Web；Telegram 共用业务 | CLI 分发与 Envelope |
| `messaging.send` | `message send` | 完整 JSON；原 request_id | Web；Telegram 共用业务 | CLI 分发与 Envelope |
| `messaging.unbind` | `channel unbind` | 完整 JSON；原 request_id | API；Telegram 共用业务 | CLI 分发与 Envelope |
| `planner.entries_put` | `next-action put` | 完整 JSON；原 request_id | Web | CLI 分发与 Envelope |
| `repos.register` | `repo register` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `runs.send_message` | `run message` | 完整 JSON；原 request_id | Web | CLI 分发与 Envelope |
| `tasks.cancel` | `cancel` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `tasks.rate` | `rate` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `upgrades.control` | `upgrade control` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | API | CLI 分发与 Envelope |
| `webhooks.cancel` | `webhook cancel` | 完整 JSON；原 request_id | API | CLI 分发与 Envelope |
| `webhooks.create` | `webhook create` | 完整 JSON；原 request_id | API | CLI 分发与 Envelope |
| `webhooks.pause` | `webhook pause` | 完整 JSON；原 request_id | API | CLI 分发与 Envelope |
| `webhooks.resume` | `webhook resume` | 完整 JSON；原 request_id | API | CLI 分发与 Envelope |
| `webhooks.rotate` | `webhook rotate` | 完整 JSON；原 request_id | API | CLI 分发与 Envelope |
| `webhooks.update` | `webhook update` | 完整 JSON；原 request_id | API | CLI 分发与 Envelope |
| `work_item_choices.respond` | `choice respond` | 完整 JSON；原 request_id；revision/epoch 见业务合同 | Web | CLI 分发与 Envelope |
| `local GET /agents/status` | `agents local status` | --socket；原 request_id/revision | 本机管理 | CLI/真实 Unix socket |
| `local GET /archive/status` | `archive status` | --socket；原 request_id/revision | 本机管理 | CLI/真实 Unix socket |
| `local GET /upgrades/status` | `upgrade status` | --socket；原 request_id/revision | 本机管理 | CLI/真实 Unix socket |
| `local POST /agents/reconcile` | `agents local reconcile` | --socket；原 request_id/revision | 本机管理 | CLI/真实 Unix socket |
| `local POST /agents/reload` | `agents local reload` | --socket；原 request_id/revision | 本机管理 | CLI/真实 Unix socket |
| `local POST /agents/validate` | `agents local validate` | --socket；原 request_id/revision | 本机管理 | CLI/真实 Unix socket |
| `local POST /archive/cancel` | `archive cancel` | --socket；原 request_id/revision | 本机管理 | CLI/真实 Unix socket |
| `local POST /archive/host` | `archive host` | --socket；原 request_id/revision | 本机管理 | CLI/真实 Unix socket |
| `local POST /archive/prepare` | `archive prepare` | --socket；原 request_id/revision | 本机管理 | CLI/真实 Unix socket |
| `local POST /upgrades/cancel` | `upgrade cancel` | --socket；原 request_id/revision | 本机管理 | CLI/真实 Unix socket |
| `local POST /upgrades/retry` | `upgrade retry` | --socket；原 request_id/revision | 本机管理 | CLI/真实 Unix socket |
| `local POST /upgrades/start` | `upgrade start` | --socket；原 request_id/revision | 本机管理 | CLI/真实 Unix socket |

| `local POST /agents/register` | `agents local register` | --socket；稳定注册身份由 runner 保管 | 本机管理 | CLI/真实 Unix socket |
| `local GET /agents/registration` | `agents local registration` | --socket；只读注册/待对账状态 | 本机管理 | CLI/真实 Unix socket |
| `local GET /repositories` | `repo local list` | --socket；本机绑定与 control_revision | 本机管理 | CLI/真实 Unix socket |
| `local POST /repositories/bind` | `repo local bind` | --socket；完整 JSON 或绑定 flags；原 request_id/revision；dry-run | 本机管理 | CLI/真实 Unix socket、原身份恢复 |

### 业务命令分组与已确认缺口

| 分组 | CLI 入口与参数 | 缺口及验收 |
|---|---|---|
| Issue | issue list/show/生命周期/分配/来源/完成核对/任务合同/进展/deadline/followup；set-status、notifications show/set | board、board-status、assignee-kind/ref；原 CAS/epoch；多页与历史 |
| 会话 | conversation list/info/show/queue/transcript/block/rollout/window/usage/compactions；interrupt/resume-processing/compact/settings/handoff-settings/handoff | 搜索、状态、conversation、before/after/limit/turn；精确会话与设置版本 |
| 消息与交互 | message list/show/send/reply/notify/read/receipts/trace/watch/respond/original/associations/associate/withdraw；choice show/respond；attention 全生命周期 | 选项与文字回答、重复与过期版本、历史分页、原文 |
| 任务与运行 | task list/show 与既有 task ID；logs/run logs/rollout/message/messages/native-entry；artifact show | 生命周期与完成时间排序、日志及 rollout 游标、独立验收事实 |
| 文件 | attachment upload/download/metadata/url；file metadata/download/url；image show/download/url | 原件和交付文件区分、SHA256、完整内容及下载失败不覆盖目标 |
| 归档 | archive list/show/records/record/originals/original/resources/download/url；原 prepare/status/cancel/host | kind、after/limit、资源文件 index、只读及 unavailable |
| 目录 | repo list/show/register、category list/show/put、agents show/hosted/local | 全局目录、retired、分页、实际 socket 请求 |
| 调度与诊断 | schedule/webhook/event/observation/request、usage、deployment、host、queue、coordination、doctor | 完整筛选、事件流、原身份恢复、unknown 与不可用明确区分 |
| 本机管理 | agents local status/validate/reload/reconcile；upgrade start/status/retry/cancel；archive prepare/status/cancel/host | 现有私有 socket、原请求/CAS、不绕过升级授权 |

### operation 与持续检查

全部 ownerCommands、hostedCommands 和动态 conversation.set_settings 操作由 API 实际注册表枚举，与 CLI 命令绑定及入口样例逐项核对。新增操作或路由没有对应绑定时 make check 失败。各 operation 的 target、完整 JSON payload、适用 revision/epoch 由同一命令登记驱动帮助和调用；不得靠通用 RPC 声明覆盖。别名登记到同一能力，已退役的外部 coordinator MCP/hook/setup 与 Agent/Runner 租约协议不属于 owner 入口，不恢复。

## 验证与验收

每项能力执行 CLI 分发并核对真实 HTTP/socket 路径或 Envelope；复杂业务复用 app/API 回归。重点覆盖多页、文件完整性、归档、跨端 interaction 以及丢响应后同 ID 去重、不同 payload 冲突、stale 无隐藏刷新、unknown 不启动新动作及部署身份围栏。覆盖检查自身通过缺失绑定/新增未覆盖能力的反例验证。正式交付须 make check、独立 review、CI 通过。

## 候选覆盖与验收进展

当前目录登记 161 项实际 owner 接口/操作，以及 16 项本机管理接口。每项绑定都通过 Main 执行后验证实际请求；目录中的操作和路由由能力检查对照服务端实际注册表。查询参数另由定向行为用例核对，界面专用 projection 与历史下载别名归并同一业务能力，不引入新的执行权限。

新增 GET /api/v1/artifacts/{id} 供原 Web 调用和 CLI 共同读取；HostView 增加可选 control_revision 以读取主机维护 CAS。新增命名的 HostMaintenancePayload、UpgradeControlPayload 并生成 schema/types；runner wire 和持久 schema 不变。

真实 HTTP 场景覆盖 Issue 状态/筛选、通知设置导出和重放、原 ID 改 payload 冲突、interaction 回答及过期版本、队列撤回、原附件/交付文件完整字节、artifact owner 权限、归档续页与原件/资源下载。既有丢响应去重、部署身份围栏、Telegram interaction 测试继续纳入回归。


### 使用与恢复示例

```sh
tend help conversation
tend issue list --board --board-status in_progress --json
tend issue set-status "$ISSUE_ID" --data '{"status":"in_review"}'
tend issue notifications set --data '{"delay_minutes":5}' --dry-run > original.json
tend request replay --file original.json
tend request show "$REQUEST_ID"
tend conversation list --search 'review' --status waiting --after "$CURSOR"
tend conversation history main --before 100 --limit 20 --json
tend conversation settings show --conversation main --json
tend conversation resume-processing "$LOGICAL_TURN_ID" --revision "$PROCESSING_REVISION"
tend message respond "$INTERACTION_ID" --data '{"version":1,"option":"report"}'
tend attachment upload - --name original.bin --media-type application/octet-stream < original.bin
tend file download "$FILE_ID" --out delivery.bin
tend artifact show "$ARTIFACT_ID" --json
tend archive records history message --limit 20 --after "$CURSOR" --json
tend usage --window 7d --json
tend doctor --json
tend host maintenance "$HOST_ID" --data '{"mode":"maintenance"}' --dry-run
tend agents local reload --socket "$RUNNER_CONTROL_SOCKET" --dry-run
```

旧 pause/resume/cancel/rate/retry/decide 快捷命令仍支持原交互；完整原 payload 可通过 --data/--file 指定，重试同时提供原 request_id 和适用 revision。新动作才能取得新身份。repo register 仅提供原 request_id 时继续查询原结果；需要发送时必须提供原完整 JSON 和 revision，禁止从当前默认值重建未知请求。上传是独立流式接口，dry-run 输出请求身份、文件名、媒体类型、长度与摘要；重试使用原身份和相同原字节/元数据。

任务列表默认提供简明表格，--json 保留完整响应与游标；消息、关注事项、历史和复杂诊断资料保留完整缩进结构，避免通用列选择丢失原文或对象身份。既有 JSON 默认的命令保持输出兼容。Doctor 分开报告 disabled、unavailable、abnormal，异常返回非零；不会做自动维护。


### 独立 review 修正

首轮独立 review 的三项 P2 均采纳：task list 统一使用已有 all=1 分页模式，按 all/finished/lifecycle/默认未完成语义选择生命周期，确保 project、limit 和 before 实际生效；消息历史、队列和关注事项不再使用通用字段表格，完整保留原文、嵌套身份和控制值；doctor 将存在 PendingRequestID 的本机 reload 标记 abnormal，保留原对账身份并返回非零。定向回归覆盖实际服务端筛选续页、原文和嵌套身份，以及真实 Unix socket 上 pending reload 诊断。


第二轮独立 review 的两项 P2 均采纳：recent message 的 before/latest 是现有服务端的全局历史视图，CLI 明确拒绝与 channel/conversation/issue/after 的不受支持组合，避免无声忽略筛选；完整筛选继续使用 after 前向读取。分组帮助由命令登记生成，叶命令 --help 由原 FlagSet 展示完整参数并成功退出；tend help <command> 同样进入实际解析器。主机目录是全局目录，--project 仅用于队列，避免宣称未生效的目录过滤。


第三轮独立 review 的两项 P2 均采纳：Issue 的 board-status/assignee 筛选要求显式 --board，assignee-kind/ref 必须成对提供，避免现有 API 模式无声忽略过滤；通用 JSON 客户端取消会截断有效内联图片的固定 16 MiB 响应上限，在原 context/HTTP 超时内读取完整认证响应。新增组合校验和超过旧上限的图片响应回归。


跟进操作的目录绑定使用实际的 issue followup <action> 入口：create 选择 Issue，其他控制选择该 Issue 内的精确 followup；原低层 operation 名称继续用于原信封恢复。入口验证检查真实 target 类型、身份和 assignment epoch，避免只断言 operation 名称造成错误覆盖。


第四轮独立 review 的两项 P2 均采纳：global board 与 parent 筛选互斥，根项筛选仍可显式使用 roots；补齐 runner AgentInventoryHandler 的 register/registration 和仓库控制的 list/bind，共 16 项本机管理接口。覆盖检查扫描全部 runner、runner 入口和升级协调器的实际 HTTP 注册/handler 条件，不再依赖三文件白名单。共享本机仓库 DTO 进入 protocol，服务端使用类型别名保持原 JSON 和持久回执格式。CLI 不导入 runner 或 gitws。

本轮不统一进程 bootstrap/restart/stage-config 等额外运维（用户已选择诊断与已有维护层级），既有 tend-updater 专用 CLI 保留；updater authorize/health 是内部执行回调，明确分类，不转成 owner 操作。请求接收、租约、worker MCP 和执行上报同样保留原角色边界。

补充 stdin 取消回归：HTTP 上传和请求前 JSON 读取均在 context 取消时关闭可中断输入，避免等待未结束的生产者；不把中断或丢失响应判为业务成功。
