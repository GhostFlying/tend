# 集中 TraeX worker 的原生恢复边界

本轮使用 TraeX 0.213.1、真实 exec-server 和独立 Orca runtime 验证；模型响应使用确定性 HTTP 替身。原生 TUI 在 Orca PTY 内启动，实际键盘打断、输入及工具结果显示均已验证；不把该结果当作桌面或移动端渲染验收，也未使用真实模型账号完成业务任务。

## 会话与执行

- app-server、模型认证和原生历史可以留在中心，runner 保留真实工作区及 exec-server；原生 TUI 是同一 thread 的客户端。
- 原生 `thread/start` 后、首次历史写入前，空 thread 可能尚无可恢复 rollout。启动暂停后应重新绑定原来的活跃未启动 registration，保留其原生设置并更新工具 handler；不能把它当成磁盘历史恢复，也不能新建替代 thread。该路径拒绝已启动、已释放或缺失的 registration。
- 初始任务 brief 以 user 消息写入，独立记录 pending/confirmed。明确拒绝可以重试；发送不明必须对账。服务端反馈的暂停或 held 在 runner 中按控制状态处理，不记为启动失败或停止证明。
- 暂停必须确认原生 turn 空闲。拒绝或未确认的 interrupt 先停止执行资源，保留待对账标记，不能凭暂停原因继续保留活动命令。人工 held 单独处理，不自动中断人工 turn。
- 冷恢复要求原 run/thread/workspace 绑定、执行停止证明及原生历史。缺失历史或待对账状态不自动创建替代会话。

## 请求计量的原生限制

已测版本的实验 schema 仅在 `thread/start` 提供 `experimentalRawEvents`，`thread/resume` 没有对应字段。冷恢复的原生连接在本轮观察中仍发送 `thread/tokenUsage/updated`，但不再提供相同的逐请求原始事件。Tend 保存实际收到的 request usage；不从 turn 累计量补造请求记录，也不能把可观测请求数说成全部模型 API 调用数。恢复该订阅的完整性需要原生协议后续支持。

配置与实现边界见 [D140 实施计划](../plans/central-traex.md)。本轮不切换生产配置。
