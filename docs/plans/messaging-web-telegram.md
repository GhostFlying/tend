# Web / Telegram 通信模型（D131）

## 目标与边界

新 IM 与 CLI 共用通信账本，legacy conversation/rollout 继续诊断。实现单 owner Telegram Bot 私聊的文本、引用、文件和图片；不改前端组件，不增加 /issue，不实现 PR/CI/GitHub 或 Business connection，不迁移 Agent 承载位置。runner 协议保持 2/1。

## 批次

1. 在现有消息、原件、逻辑回合和 outbox 上追加来源端点、正式发布、投递分片、阅读证据及持久 Telegram inbox；不复制正文，不伪造历史已读。
2. 扩展 conversation_reply 的显式输入目标和附件；Tend 固定来源渠道。主动通知默认 Web 和已配置 Telegram，原生文本不自动发布。消费证据立即提交、推送事件并独立投递可选 MarkRead。
3. 增加 /api/v1/messaging/ 的消息、渠道、回执、追踪及事件接口；CLI 提供 channel 管理和 message send/reply/notify/list/show/receipts/trace/read/watch。旧接口语义不变。
4. 封装 go-telegram/bot v1.27.0；Tend 持有 polling offset、先持久化再确认、owner 绑定及媒体准备。上传用标准库流式 multipart，未知发送不重发。
5. 定向测试后冻结候选，集中 make check、独立 CLI review、交叉编译及 PR CI。部署和真实 Bot 启用独立执行。

## 不变量

- Agent 只选择输入、内容和业务关联，不接受渠道/地址参数；回复只回输入端点，Web 统一查询不等于转发。
- 已读、投递、处理分别投影；claim/start/accepted 不等于消费。消费证据匹配消息版本、执行与原生输入或来源读取；提交后立即唤醒 SSE/独立 read 控制投递，不等最终回复。
- Web 显式确认阅读；普通 Telegram Bot 不提供 Bot 标记已读或用户阅读证据，能力明确为不支持。可选 MarkRead 仅推进连续已消费位置。
- 外部副作用经 outbox，发布/来源/业务变化与请求身份同事务。确定未发送才重试，unknown 保留。
- 责任路由按引用、Issue 和当前 assignment epoch，未领取才交接；不因负责人暂不可用自动升级到 main。
- 附件先暂存原件再准入；接收上限遵循 Bot API，编辑保留更正版本，历史消费和既有业务效果不覆盖。
- 未来事件只预留领域更新与按需唤醒接点，使用现有 Issue 状态变更验证，不建设通用事件规则引擎。

## 验收

覆盖来源路由、双渠道通知、正式/诊断分离、消费时即时回执、MarkRead 独立投递、引用负责人、消息更正、媒体/分片、非 owner、重复 update/工具回放、崩溃恢复、unknown 不重发及 legacy 回归。默认 fake harness/Telegram HTTP 与注入时钟/ID；真实 Bot 验收需要已配置凭据和 owner 私聊，不将模拟结果记为真实验收。

## 实施记录

- 已追加通信迁移、来源映射、正式发布、逐项投递、显式阅读与处理追踪；协议保持 2/1，前端只生成协议类型。
- 已接通 named main、Issue manager、consultation 的正式回复/通知与消费证据；独立控制 dispatcher 提供可选渠道已读同步。原生输入在调用前冻结版本与摘要，重连只用精确匹配的证据恢复已读；修复提示和空继续不补造原输入消费。
- 已提供 Web API/SSE、CLI、Telegram 持久 polling、绑定、文本/媒体、引用、更正和 unknown 保留。已取消提醒退役未发送项，发送中与 unknown 仍保留对账责任。
- 独立 review 复现的问题均纳入回归：canonical 发布、附件单独回复、媒体拒绝与队列恢复、提醒改期、逻辑恢复状态、修复误读、来源准入、附件作用域、重连消费、更正追加、named 角色提示词、consultation 通知权限、编辑后的引用、发起执行追踪、启动前失败状态、CLI 长连接、永久媒体失败、manager 发布后的回合提交、decision 通知、系统通知责任、planner 展示通知、终止状态优先级、处理/关联事件投影、面板回答、长 worker 证据及正式回复输入方向。
- 真实 Bot 的文本、图片、文件、引用、主动通知与重启恢复需在另行提供本机凭据后验收；模拟服务结果不替代真实渠道验收。部署和 Bot 启用不属于本轮自动交付。
