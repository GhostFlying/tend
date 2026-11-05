# TraeX 根观测的语义去重补正

Refs: D175。

## 已证问题与范围

同一原生 session/turn 已记录 running、phase=known 后，真实 rollout 补证的终态与
settled operations 仍可能映射到相同 phase/identity，导致完成时钟与终态事实被旧去重
版本吞掉。本次仅修观测发布的持久语义版本，不使补证成为生命周期权限。

## 最小实现

- 在现有 `recordObservedRoot` 局部 copy 边界计算稳定的 TraeX 观测指纹；保留原生
  session/turn/launch/时钟/状态/operations 等事实，排除每次采样变化的 observedAt。
- 指纹仅作为可选 JSON 观测元数据持久化。语义事实变化时使既有 canonical 记录入口
  产生一次新 revision；相同事实重复观察与重启重放沿用同一 revision/event identity。
- 既有 phase/identity、revision 与指纹在同一次保存中提交后再发布；保存失败只恢复
  内存观测字段，使后续同事实仍重试持久保存。不伪造原生 ID，
  不改已有原生绑定、turn、prompt 或授权。
- `client.go`、`recovery.go`、`historical.go`、`root_turn.go` 保持基线逐字不变；
  worker 仅新增观测状态字段。无 Send/Launch/Close、恢复策略、协议或 DB schema 变化。
  旧 JSON 缺失字段允许读取，首次新语义记录可产生一次可去重的新事实。

## 验证

先用相同 source 的真实观测记录/序列化恢复回归证明旧实现会吞掉 running 后的终态，
再检查 completed/cancelled/failed 终态的新 revision、原生时钟与 event；重复相同补证
不刷版本，重启后的事件 ID 稳定。既有真实 Start/recovery 与取消回归继续验证局部
copy 不回流执行控制。只跑有限 Orca race selector，不开 full、模型或生产操作。

本次不修原生 turn 中断原因、不自动继续模型输入，也不把 operations unknown
转换为 settled 或把终态事实转换为 physical-stop/业务完成。
