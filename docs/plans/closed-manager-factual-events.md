# 已关闭 manager 的原工具生命周期事实收束

## 问题与范围

manager 的原工具结果已经成功投递，原关闭结果也已持久化，但服务端仍可能有
未应用的 `call`、`item_started`、`item_completed`。关闭回执事务会先根据原
`tool_result` 收束 call，再因未应用的工具生命周期事件拒绝关闭；事务回滚后，
每次原请求重试都会遇到相同阻塞。保留 Run 的正常重放本身已经有界，问题在于
服务端缺少已知事实的完整收束路径。

本计划只补齐原关闭回执事务内的事实消费。不得执行工具、发送 native 请求、
创建会话、恢复业务 turn、续活 lease，或把原业务 unknown 改成成功。原事件和
请求必须完整保留，关闭的游标覆盖与全部事件已应用检查继续生效。

首版限定原 hosted/logical execution 明确属于内部 repair 的 turn。repair 本来不
创建普通用户工具展示；普通业务 turn 仍由原消费者保存 rollout/tool use，不能
用本路径吞掉这类用户事实。边界按原 repair 身份适用于各工具，不按某次调用名
或事件序号特判。

## 实施项

1. 在现有成功 close 的事务与原工具结果收束逻辑中，识别已成功投递原
   `tool_result` 所对应的 `dynamicToolCall` 生命周期事实。原结果必须属于同一
   Run，已 completed、原 request/result 身份一致、无投递错误或 rejected；业务
   工具本身成功或失败均保留原义，不要求业务 `Success=true`。
2. 核验原 Run、thread、turn、call/item 身份、原 repair 归属、事件种类与生命周期状态，以及
   原工具调用和持久化结果的对应关系；不以缺省字段、相似 ID 或当前库存替代
   原证据。不能建立完整对应关系的事件继续 pending。
   原事件的 projection 与 raw 均须符合原 call 的 tool/arguments/身份；只接受
   明确的 started/inProgress 与 completed/completed 生命周期组合，完成结果
   仍以原持久化 `tool_result` payload 为来源。
3. 对确认属于原已回复调用的生命周期元数据记录消费事实，保留原始事件内容、
   原工具结果及错误语义。相同原 close 请求重试不得增加业务执行或改变请求身份。
4. call、生命周期事件、close 回执与请求去重在同一事务提交。任一其它事件或
   证据不满足条件时，继续拒绝关闭，不跳过游标、工具授权或业务对账门。
5. 原 close 与独立 stopped 成功归账后，重新读取真实 manager 对账结果；只有
   符合现有合同，才使用原有 proved-close receipt reader。后继业务仍由 Tend
   经正常责任链推进。

## 验证

- 复现已回复 call 与两条生命周期事件导致原关闭事务反复回滚的失败，再验证
  同一原请求成功归账，后续独立 stopped 可被接受。
- 核验零工具执行、零 native 调用、零新 Run/turn/attempt；原工具结果、原事件
  字节和未决业务事实不变。
- 为消费保存原 close 请求、事件序号与 body hash、原 tool result 请求与 payload
  hash 的事务审计；审计写入失败时同样回滚，重试不产生重复消费。
  审计区分原 native close request 与提交回执的 Envelope request；被 rejected
  的 close 不授予本路径的事实消费权限。
- 覆盖身份、调用、结果、事件种类与状态不匹配，缺少原结果、投递未确认、
  无关事件、错误游标、部分合法部分非法，以及同请求幂等。
- 保持普通事件消费、纯文本前缀恢复、真实工具失败结果和历史关闭路径的语义。
- 完成适用的定向测试、完整 `make check`、规定的独立 review 与 required CI；
  最终候选通过后线性合入，从实际合入源码构建并受控部署。

## 兼容性与设计

仅消费既有持久化事实，不新增 owner 权限、通信字段、存储迁移或旧请求校验条件。
现有关闭入口、请求查询和 manager 对账 CLI 继续提供输入及观察，不新增写入口。
实现对应 D182。设计决策与最终边界在同一变更中记录，禁止
把未知结果、普通工具生命周期或其它 item 类型泛化为已完成。
