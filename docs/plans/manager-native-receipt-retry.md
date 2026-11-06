# 原 manager 关闭回执的有界退避

Refs: D179

## 故障边界

原关闭流程消费内部 repair prefix 的修复已在主干；本切片补齐 runner 的回执发送行为。成功 close 仍必须覆盖全部已应用事件，原业务结果的 unknown 不因物理关闭而解除。

`runnerapi` 对未确认结果可返回 HTTP 500 与 `unknown_outcome`。共享客户端将该响应包装为可重传的 `transportError`；`sendManagerReceipt` 调用通用 `send` 后会陷在其固定间隔重试内，无法自行控制关闭回执的重试频率。缺少事件确认时，原回执可以每两秒重复请求与记录日志，且无进展不会降低频率。

## 实施

1. 原生 reply 的回执循环改为每轮一次 wire 尝试。统一处理 `unknown_outcome` 和可重传的传输失败，包括 HTTP 500 包装；其它明确拒绝立即返回。非 reply 的停止证明继续使用既有发送路径。
2. 第一次重传保留已有 RetryDelay，默认两秒，之后逐次加倍至默认 30 秒上限；已经显式配置更长 RetryDelay 时保持该间隔，不将其缩短。每次失败都等待，context 取消立即终止，避免引入忙循环。
3. 重传始终使用同一原 Envelope、request_id、payload 和冻结 LastSequence。回执与游标继续保留在 journal，取消后恢复先重传原 close，再完成收尾；不打开新 native 会话，不生成关闭边界之后的新事件。
4. 失败日志携带 run/request/trace 身份及下一等待时间，重试频率与日志频率一并降低。没有新协议字段、迁移、业务权限或成功判定；不调整其他 RPC 的通用错误分类及重试规则。

本切片不提供事件进度通知：原事件处理完成后，回执在下一次有界重传时被确认，默认最多再等约 30 秒，不含单次请求耗时。退避只降低等待成本；原缺失事件仍依赖既有消费及对账流程。

## 验证

- 通过共享 HTTP 客户端解析 HTTP 500 unknown、普通 unknown 与传输故障，验证持续失败时的请求预算、短暂竞争的首次重试和恢复后的成功。
- 构造已完成 close 的 durable journal，验证等待取消后原回执和最后事件序号不变；重新打开 journal 后只发送原事件/close，不开 native，不重复关闭或追加事件。
- 明确协议拒绝不重试，显式长 RetryDelay 不缩短，取消退避立即退出。
- 运行 runner 的 race 回归，并复核主干已有的 active/closing repair prefix 与原 close 同请求对账用例。最终候选完成完整 make check、独立 review 与 PR CI。

运行版本替换及现场原积压解除另行验收，源码与测试通过不代表已经部署或证明现场恢复。
