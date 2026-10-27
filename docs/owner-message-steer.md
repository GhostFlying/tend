# 主代理工作期间补充消息（D115）

托管主代理忙碌时，owner 可以直接在原对话发送补充要求，沿用普通消息、附件和 reply-to。支持原生 steer 时，当前投递循环会约每 100ms 检查待处理 owner 消息，将它送入当前 thread 的当前 turn，不创建第二个主代理。原目标及约束继续有效；澄清会调整要求，状态询问应得到状态回复，停止文字要求由代理在安全边界解释，不能自动取消 worker。

## 状态含义

| 显示 | 可证明的事实 |
| --- | --- |
| 等待处理 | 消息已持久化，尚未获准进入原生回合；仍在普通队列 |
| 正在送入当前回合 | 发送意图已落库，正在等待原生回答 |
| 已送入当前回合，等待安全边界 | `turn/steer` 返回当前 turn ID；不代表模型已读取 |
| 正在处理 | 收到与保存的完整 input、thread、turn 相匹配的原生 user-item 完成回执 |
| 已回复 | 消费回执、正常 turn 完成及关联的完成回复都存在 |
| 已处理，本回合未生成回复 | 消费与正常 turn 完成存在，但无关联完成回复 |
| 处理结果未知 | 原生接受或消费无法确认；保留正文与尝试，禁止自动重发 |
| 已停止 / 处理失败 | 消息已进入原生会话，随后回合停止或失败；保留已有内容 |

Steer 元数据随 owner 消息返回，含 thread/turn、原生 item ID、状态及原因。助手消息的 `reply_sources` 保留其开始生成时已消费的 owner 来源；同回合的最终回复可在这些来源的正文块下读取。流式消息的原 `reply_to` 在生成期间不改变。消息正文、排序 seq、request-id 去重、owner 来源和正式 decision/choice 答复路径保持独立。

托管工具的可选 `source_message_id` 指向该动作实际回答的 owner 输入：同回合先有规划/decision 答复，后有状态询问时，应选择较早的实际请求/答复，而不是把写入归因给状态询问。规划来源只可选择本 turn 已准入的 owner 输入；decision 恢复还可引用历史已准入的真实答复。系统、worker、排队/unknown 或撤回消息不能作为 owner 来源。后台来源读取与 check-in 仍绑定其原始触发事件。来源选择不改变权限或后来的约束。规划写入独立按原 turn 初始消息与 commit_seq 去重：消费补充消息、显式选择来源或恢复后换 native call ID，均不产生新的序号空间；重复序号返回 duplicate_effect 与 original_request_id，可读取既有结果。新的合法序号仍将实际 owner source 和 parent request 保存到任务/spec、工作项绑定与请求审计。

## 支持与时机

- 本轮支持 TraeX hosted app-server 的真实 `turn/steer`，验证版本为 0.211.1。其他 `harness.Thread` 实现缺少该可选能力时，消息继续排队并展示原因；外挂主代理与 worker 消息投递不在本轮范围。
- 模型生成期间可以先接受消息。原生执行器在下一次安全交互边界把它作为实际 user item 加入模型上下文；已生成的前一段回复可先出现，同一 turn 随后响应补充要求。
- 工具调用期间 Tend 继续接收并持久化消息，steer 可被原生接受。运行中的工具不会被 steer 截断或重试；长时间阻塞的工具可能使消费等待同样长的时间，界面不得承诺立即处理。
- 临近 final 时，只有原生仍接受当前 turn 的输入才实时投递；前置条件明确拒绝后，消息继续排队，由正常后续 turn 处理。同一回合拒绝后不循环尝试。
- 会话空闲、turn/start 尚未确认、维护/升级排空、摘要或 handoff 阶段不承诺 steer。Handoff 前排队的屏障不能被后来的 owner 消息跨过；后台事件及 worker 建议不会进入 owner steer 路径。
- 当前工具 schema 必须已登记，才能证明实时消息来源的语义可用。新建会话登记安装的 toolset hash，托管维护模式也使用既有 D81 绑定。0.211.1 的 resume 不接受替换 dynamic tools；历史会话没有对应登记或保留旧 schema 时，补充消息排队并展示需要更新原因，按正常 D81 交接更新工具。不能通过杀掉当前会话模拟 steer。
- 多条补充消息保持 outbox 顺序，各自有尝试和来源回执；reply-to、decision option、原始附件引用也进入原生 user 文本。

## 恢复与运维

`main_steers` 独立于 `main_turns`：后者仍表示一个原生 turn 的初始投递，不会为了追加消息制造多个 turn。发送前先记录 thread/turn 和精确 input；消息/附件准备失败尚未调用原生，界面显示准备失败并保留原文排队；原生明确拒绝单独显示拒绝，其他等待原因使用中性提示。接收失败只有确定未生效时才回到队列。超时、连接断开或发送中进程退出会留下 unknown，不能因安静、重启或未见最终回复盲目重发。

若同进程稍后收到匹配 user-item 回执，它可以核对掉接收 unknown；回合结束时仍无消费证据的已接受输入转为 unknown。恢复进程把已消费但未完成的输入记为 interrupted，把发送/接受而消费不明的输入记为 unknown，保留原生来源。未获准或 unknown 的正文不作为新的 context 指令回灌；只有明确拒绝的消息按普通队列重新准入。原生接口没有请求去重键，跨进程无法仅凭一次超时安全证明原生消费，应查看已有账本和原生记录核对，不自动复制消息到下一回合。

工具回调在原生连接读到请求时固定当时的 user-item 快照，并将首次 source/request 保存到 `main_tool_sources`。迟到/重放的回调不会改用另一条 owner 消息；相同 native key 重放返回原请求结果；即使更换 key，原 turn 初始投递仍固定规划 effect 身份，commit_seq 在同一 turn 内共用。原 turn 查询失败或无法确定时不执行工具，不以补充消息建立替代去重身份。工具结果未知时沿原有账本核对语义恢复，steer 不提供更大权限。

“停止回复”按钮可通过本回合已接受或已消费的补充消息定位原始 turn，仍走 D69 的明确 interrupt 路径。文字中的停止要求由模型在安全边界处理；按钮主动中断时，工具可能留下未知结果，必须按已有账本核对规则恢复，不能盲目重试或自动取消 worker。

本轮没有生产发布或重启。上线前须由部署职责确认运行二进制、迁移与原生执行器支持，独立验收先复现下列隔离链路；不向当前生产主会话发送测试消息。

## 隔离验证

临时目录设为任务自己的 scratch，所有原生会话由测试创建并在退出时关闭；无需查找或复制 owner 凭据。D107 的 app-server 启动会在合并环境覆盖后清除大小写不敏感的全部 ORCA_ 项；外层 TUI 设置、模型 home 与鉴权不变。独立模型测试还应在测试启动脚本的子进程环境清除这些项，避免其他启动路径继承外层身份。检查旧代码必须从外部构造干净环境，不修改冻结验收对象。使用已有允许的原生模型环境：

```sh
TMPDIR="$TASK_SCRATCH" go test -tags integration ./internal/mainthread \
  -run TestIntegrationHostedOwnerSteerRealModel -v -count=1
TMPDIR="$TASK_SCRATCH" go test -tags integration ./internal/harness/traex \
  -run TestIntegrationNativeSteerBoundary -v -count=1
go test -race ./internal/mainthread ./internal/app -run 'TestSteer|TestFault_Steer|TestMainSteer'
make check
```

若任务 scratch 的绝对路径很长，现有 Unix socket 测试可能因路径长度限制失败。可为该 scratch 创建足够短且独占的临时目录别名，把测试 `TMPDIR` 指向别名；实际数据仍落在原 scratch，检查退出时移除本任务自己的别名。交付的 `check-run.py` 接受验证者自己的 scratch 路径作为参数并负责清理，不修改 socket 测试或生产配置。本机完整 race 测试较慢时，使用 `TEST_TIMEOUT=30m` 运行同一个 `make check`，不减少任何检查。

真实模型验证默认 `TEND_TRAEX_MODEL=GPT-5.6-Sol`，使用隔离 Tend 账本、实际 Manager/dispatcher/tool 服务和真实 app-server。测试只延迟第一次只读工具返回，检验原生接受发生于工具未完成、消费发生于安全边界、同一 thread/turn 最终保留原目标并响应补充要求；还检查会话空闲后在同一 thread 开始下一 turn。确定性本机模型端点验证生成中、临近 final、连续输入顺序、模型实际请求正文、已结束 turn 的明确拒绝及未消费输入的原生进程恢复；它不能替代真实模型验证。故障单元验证覆盖拒绝/未知与恢复、withdraw/claim 互斥、工具重放、foreign-turn 回执、回复来源、回调先于事件循环及 accepted-without-consumption。
