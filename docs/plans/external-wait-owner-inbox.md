# 外部等待保留已排队的负责人输入

## 原因与范围

Issue owner 的当前执行结束前，新的 owner 消息已经保存在原会话 inbox。
登记外部等待时保存的事件最大序号仅表示事件已存在，不能证明这些消息已被消费。
原唤醒判断只接收这个水位之后的消息，因而会忽略尚未投递的既有输入，直到外部
条件结束或提醒兜底才续行。

## 最小实现

1. 沿既有 owner 输入准入、消息控制与 outbox 事实判断未处理输入，覆盖登记等待
   之前和登记到真实停止之间的输入；事件水位不得充当消息消费确认。
2. 保留当前 Issue/会话及活动关联、真实 owner 授权、撤回、原生 turn/steer 准入和
   unknown 对账边界。已经处理或撤回的输入、其他 Issue 和 system 进度不误唤醒。
3. 登记等待仅保存意图。真实停止后才能续原 Task、追加 Run；每个 wait 只生成一份
   continuation，不重放 CI、合入、升级或原消息，不新增 Task、协议或用户审批。
4. 先以旧实现复现“先入队、后登记等待”的失败，再覆盖已处理/撤回/其他范围、
   停止前资源保留、停止后单次唤醒，以及普通等待和新输入已有回归。
5. 更新 D221，按仓库既有完整检查、独立审阅、CI 与受保护 rebase merge 交付。
   完整发布集部署须先核对当时业务升级与节点状态，保留原配置和未知操作；修复
   后只验证原消息的真实投递/消费，不发送替代业务消息或直接改变 Issue 状态。

本次不改 Issue 看板文案、不扩展 Agent 角色约束，不重做已经部署的原件、转发或
空回复修复。回归使用本地替身与实际存储，不调用模型或网络。

Refs: D221。

## 独立审阅发现：按接收责任区分投递

原 owner 输入在 main 已进入原生 turn 或已完成，不代表其关联 Issue 已处理。
canonical association 把原消息加入该 Issue 的 background batch，通过独立
`handle_event` 投递；它仍 pending 时不能用 main 的投递/消费事实排除唤醒。

在原谓词内合并当前待投递的直接 `handle_message` 与该 Issue 的
`handle_event`/background source，统一核对真实 owner 来源、活动关联、撤回及
取消。原生 turn、receipt 和 steer 的未知/已准入边界应按各自实际投递载体的
请求身份检查。不得将 main 已处理等同 Issue 已处理，也不把普通 system batch
当作 owner 输入。pending/frozen batch 的领取事实不单独冒充原生消费。

直接 inbox 只接受等待 Issue 自己会话的投递目的地；main 原消息即使仍 pending，
也不能绕过该 Issue 独立载体的已处理或未知回执。main 路由来源只能按接收 Issue
自己的 batch/source 判断，不能再由全局 main inbox 提供第二条准入路径。

补充 main 原输入 sending/delivered、Issue 载体仍 pending 的真实回归，以及
main 保持 pending 时 Issue 载体已处理或仍有未知原生回执的负例。验证原输入和
载体身份、真实停止后不重复续行及 unknown 不重放；保留首轮 red、冻结候选审阅
发现和旧完整门槛终止事实。只在最终增量上继续同一独立审阅与完整交付门槛。

## 协议评估

修复只改变 server 对已提交 inbox 与停止事实的读侧唤醒判断，不更改请求、
assignment、原生停止回执、存储格式或既有有效输入校验。保留协议 4/3 与原
`input_event_watermark` 诊断字段，无需新的通信适配或迁移。
