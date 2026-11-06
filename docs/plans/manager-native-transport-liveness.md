# 原 manager 关闭回执与内部输出消费收敛

Refs: D179

## 已确认的故障边界

runner 可已真实关闭原生进程，持久保存成功的原 `close` 回执和最后事件序号，而 server 因尚未消费的内部 repair 文本 delta 拒绝接纳该回执。server 的 `close` 等待者超时后会再次入队，原 runner 的回执重传又先于停止证明，原责任可持续被阻塞。

现有数据库补偿只接受失租 manager。仍持有效租约、已经进入关闭流程的同一原 run 无法使用它；普通会话线程又没有内部 repair prefix 的确认入口。该差异使“原事件已经持久、内部输出不得发布、进程已经关闭、原业务结果仍未知”无法同时收敛。

## 最小修复

1. 将数据库原 prefix 消费边界从失租扩为原关闭流程：新增 active 分支严格要求唯一原 pending repair receipt，同一原 run 的未决 close、已确认 thread 元数据、冻结 pin 与 Issue/assignment epoch，以及完整的原 repair delta 序列。active 普通用户回复不进入此分支；lapsed 普通回复和 repair 保留原行为。released、歧义身份、其它未处理事件、缺失原 close 仍拒绝。
2. BackgroundPool 沿现有唯一 conversation 调度先运行该数据库消费者。只消费固定数据库 slice，不打开 harness、不申请执行租约、不恢复原生线程、不发送模型输入。完整消费后复用既有 repair prefix 的全文与 cursor 校验、不可变审计和原子 applied 更新。内部文本继续仅保存在原事件，不发布为用户答复，不生成原生完成事实，不结清业务 unknown。全空 repair delta 也须按原 raw events 的完整消费证明确认，不能因不生成用户回复而永久跳过。
3. 原 runner 成功 close 回执按既有 request ID 重传并得到确认；保留原停止证明、租约释放及只读业务回执对账流程。不能跳过未处理事件的门，不能直接将原业务结果改成成功。
4. 同一 run 的 pending、started、unknown close 复用原命令身份，不能因调用方等待超时再增加物理关闭命令。普通 completed-error close 的旧通信行为保持，已有 upgrade park 的专用错误围栏与准入保持；修改该额外状态需要另行版本化设计，不借本次修复收紧旧有效请求。相同 run、method、完整 payload 的未决 find 只读查询复用原身份，不合并有业务副作用的 turn、inject 或 tool_result。历史已积压请求不删除、不伪造回执。
5. 事件转发错误被后台消费者静默吞掉、native find 由整个 run context 无界等待的问题本次不修改，独立调查。普通 transportError 已使用原 Envelope 重传；不能在 Driver 重新调用 Emit 生成新的事件身份，也不能宣称这两条已是现场根因。

## 兼容性与验证

不修改 server/runner 字段、协议版本、旧有效请求、原 close 结果或 journal 格式。扩展数据库只读消费的可适用场景，不授予新业务写入权限。close 复用仅回收同一原未决控制身份，旧 request_id 的内容和回执保持不可变。

先构造 active+closing 的真实 App/BackgroundPool 组件回归：原 close 被未消费 repair delta 阻塞；固定 prefix 全文被消费并审计后，原相同 Envelope 得以确认，pending 原业务 receipt 保持；无第二次 close、native 打开或业务工具执行。另覆盖已停止/失租、错 pin/epoch/thread、缺 delta、非 repair、重复消费、晚到原 delta 及事务失败。

find/close 合并回归须覆盖两个 caller 先后取消、相同未决身份不增长、不同 thread/turn 或 method 不合并、旧 unknown 不被改写、原 close 结果可继续读取。重检查由唯一协调者排期；保留原现场及完整失败日志。最终候选通过完整 `make check`、独立 review、required CI 后受控部署；部署后只读核对原 close/停止/业务对账与原责任接续，不能以入队或部署成功当作主线已恢复。
