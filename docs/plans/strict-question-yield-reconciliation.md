# 严格继续后的原问答交接回执恢复

Refs: D159、D174。

## 已证问题与边界

严格附着的普通执行可以通过原 `AskOwner` 产生问答 handoff。runner 已确认原进程停止，
将同一 `runs.yield_question` 写入 releasing 后，重启会复用原严格授权；现有 Hello 门只
认可 WorkerDone/result，问答分支不会产生这两项，因此合法原回执无法继续提交。

本次只修原回执恢复，不创建新的工作、问题、native session 或 prompt。普通 give-up
没有独立业务结算许可时仍然保持 fenced。独立 Stop 与原有 unknown 对账规则不变。

## 实施

1. 先以真实 App/RPC 和持久 journal 构造原 handoff、原严格授权及停止回执；关闭并重开
   journal 后，证明 Hello 拒绝原 yield 的 RED。检查服务器 handoff 与原请求身份。
2. 只对同一已持久严格授权的 releasing 问答回执增加准入：必须有原物理停止证明、
   原 run/target、非空原 request ID、明确 `runs.yield_question`，以及非空 decision 和与
   保存 thread 完全一致的 payload。该准入只提交原信封；服务器继续验证原 handoff。
3. 保留普通 final-result 门，不伪造 WorkerDone，不泛化允许 releasing。无停止证明、
   缺失/改变的授权、错误 target/payload/thread、普通 release 都不得通过这个例外。
4. 同一真实回归验证原 request ID 和 payload 到达服务器、handoff stopped、唯一后继，
   且没有新 worker/prepare/native 启动。补边界负例并保留独立 Stop/旧严格结算回归。

## 验证与交付

使用两 CPU 的定向 race 检查，保留 RED、失败尝试和 GREEN 原件，最终源码与回执绑定。
不运行 full、新模型、PR CI 或生产操作。单提交交集成 writer，由另一位 reviewer 核验
修复；原整栈 REQUEST_CHANGES 不改写为通过。
