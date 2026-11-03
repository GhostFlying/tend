# 条件恢复与飞书 channel

Refs: D162。复用已有条件能力与飞书范围报告，保留其未验证边界。

1. 基于现通信账本实现显式 owner 绑定的飞书文本收发 adapter；外部来源只能映射到固定绑定 owner/chat，不能用消息字段创建权限。凭据仅由私有配置引用。
2. 按官方协议校验原始请求签名、时间与 verification token，支持加密回调、URL challenge 和消息事件；在事务持久化和去重后确认，重复 event/message 不重复创建业务消息。
3. 复用现通信 ReplyTo/Issue 路由与 outbox。冻结目的地及绑定 revision；绑定变化不向新地址补发旧消息。外部结果 unknown 不盲重发，保留原操作身份。
4. 配置开关默认关闭；提供正式 CLI 的绑定/解绑/状态和帮助、请求 ID/CAS/恢复语义、能力目录及回归。缺少附件/交互能力时不能虚报支持。
5. 条件唤醒作为后续独立范围核验，不捆绑到本 channel PR；不将正文、closed 或轮询脚本冒充自动触发，调查报告不冒实现。与控制批次协调共享 app 文件和迁移编号。
6. 所有外部 HTTP 用离线替身验证：签名/加密/错误/重放、权限与路由、同 ID 幂等、超时 unknown、绑定更换。正式联网激活需要真实 app 与租户、回调可达性、事件权限和 owner 授权身份，未具备则列明未验，不发送真实飞书消息。
7. 全部修复与规定验证合入后才集中 fresh 部署。本计划不启动旧生产、timer 或任何模型。

8. 第二轮独立审查收口：已关闭问题的飞书补充文字作为原始 owner 输入保留；回答来源在同事务同步确认发布前登记；transport 与 owner API 使用相同的有效 owner 列表。补覆盖回放幂等、原回答不改写和未授权 owner 拒绝的回归。

9. 当前部署入口为私有网络，不假定已有公网回调。使用固定官方 Go SDK v3.12.1 增加出站 WebSocket 接收模式，配置启用时默认选择该模式；HTTP 回调保留为显式可选。复用同一来源校验、持久输入、请求身份和 outbox，SDK 仅承担长连接鉴权、帧协议及重连，不手写 protobuf。服务端 context 拥有连接及退出/join；连接错误只降级 channel 状态，不停止 owner API。SDK 日志不输出原始事件或凭据，configured 与 connected 分开。用本地 SDK 网关替身覆盖连接、断线重推、错误凭据、取消/退出，不访问真实飞书。

10. 第三轮审查修复：URL verification 按官方协议允许无事件签名，经解密和 verification token 鉴权；普通事件仍验证原始 body 签名。父输入只有未提交或 accepted/unknown 才等待，明确 failed 父项不能使子输入永久 pending。官方 SDK 私有 cache cleaner 的退出缺口单独以正常依赖补丁解决，不用 unsafe 访问或虚报 worker 已全部退出。

11. 上游 v3.12.2 核验仍无 cache cleaner 显式释放，使用基于 v3.12.1 的最小 SDK fork，固定合入 commit `16ab071ae1eb786f9d8843b933c067e80917d8b4`（保留上游许可/协议与原 Start/CloseAndWait 行为）。构造后立即 defer terminal Dispose，覆盖未启动、预取消、鉴权失败与正常重连关闭。fork 只新增 cache Close 与 terminal Dispose，经独立审查和 Go 1.17/stable 生命周期检查；Tend 另核真实本地网关及 cleaner 数不增长，不访问真实应用。

12. 第四轮复查发现选择题只冻结原始 question，遗漏交互选项与引用回复说明。复用现有 MessagingInteractionText，在同事务、分片与 outbox 冻结前渲染文本；保留原额外通知正文，普通非交互消息不改变。验证多段通知完整、原消息引用与数字选项回复去重，不新增交互卡片或权限入口。
