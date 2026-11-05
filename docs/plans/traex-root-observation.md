# TraeX 原生 rollout 终态补证

## 问题与范围

原生只读历史连接可能把另一连接仍活动的 turn 投影为 interrupted，却不提供真实结束时间。现有 ending_unconfirmed 守卫必须保留。实际 TraeX 0.213.1 rollout 能提供同 session/turn 的显式 turn_aborted 及原生时间，但 Tend 的本地 rollout 观察仅覆盖 Codex。

本变更只在 TraeX 的同原身份 native-api 观测为 ending_unconfirmed 时，使用已由 providerSession 指定的原 rollout 补证。不创建会话或发送 prompt，不改 AnswerDecision、控制协议、租约、物理停止或严格 retained-run 接线，不扫描会话目录，不增存储迁移。

## 实施

1. 先用真实格式的隔离 fixture 重现 API ending_unconfirmed 与显式原 turn_aborted 并存时仍未知；补身份不匹配、缺失/未完成 rollout、未结工具操作和既有 Codex 路径对照。
2. 复用既有 native transcript 解析与 operation settlement，校验 TraeX originator、session、原 managed launch marker/input、当前 turn、原生 timestamp。只接纳原始显式结束，拒绝 EOF/观察时间合成终态与身份漂移。未知操作不当 settled。
3. 补证保留原 daemon runtime/launch 归属；原生事件时间用于 completedAt。取消仅是业务 turn 的 cancelled 事实，不证明进程已停或全部后台工作已结束。
4. 复用现有 root-observation、恢复与事件账本，不引入 wire 或默认旧请求语义变化。实际部署必须具备新 runner，不能只更新 server 或重启共享 Orca daemon 来冒交付。

## 验证与交付

必要故障回归先 RED 后 GREEN，有限并发 race 覆盖 TraeX 补证与既有 Codex/恢复路径。真实 rollout 的工具操作收敛不足时，给出 pending/unknown，不能仅凭 abort 清除 pending 或重发原输入。目标验证后冻结源，由既有唯一 operator 协调 runner 安全更新；完整本地检查、规定独立 review、required CI 按仓库门槛完成后方可合入。


## 独立审查与原件验证后的收敛

原候选的真实格式验证失败：TraeX 0.213.1 把 managed input 和外层工具调用保存在 history_mutation append/items；只读 response_item 无法绑定原输入，也会漏掉未结外层操作。补齐 append 中已有 message/function/custom-tool 语义，不把 snapshot/rewrite 或未来格式当空操作库存。未知工具名称即使有 output 也保留 unknown，不用 opaque code-mode 返回值证明全部效果已结。

独立审查同时指出真实 Workers.Start/begin 与 acceptance 缺 verdict 路径仍可发送 continuation/reminder，以及补证 failed/settled 无轮询延时。回归须走真实 Start、begin 和 run，跨 serialized Thread 重启后仍无隐式 Send/Launch/Close；补证保持有界可取消的 Poll，真实原生和 Codex 旧路径不变。原候选失败与 review finding 保留，修后一批完整目标回归和同一冻结原件离线验证再更新候选。

## 恢复阶段的最后分流校验

补证阻止已持久化 closing 恢复时会进入既有 unknown 对账循环。该循环此前只处理 continuing；不能因观测从补证转为强原生事实而丢失原 closing 策略。最小修复按已有 durable Recovery.Phase 重入原 begin/close/launch 路径，不增加恢复授权、不修改错误重试或关闭身份。回归通过真实 Workers.Start 从弱观测转强观测，分别验证 closing 只关闭原 terminal 后重建、关闭成功但回执丢失后先读取原 terminal 消失事实、continuing 仍只续原 session，三者不得互串。原 close CLI 没有独立 request ID，验证它已有 terminal/session/launch 身份，不虚构新的去重能力。
