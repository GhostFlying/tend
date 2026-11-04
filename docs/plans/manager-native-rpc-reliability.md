# Manager native RPC 请求与关闭可靠性

基线：`d47aaba37ace8630774b4760467ec7d22337ac74`。Refs: D166、D169。

## 问题与范围

同一 manager 的 close 请求可能在独立进程关闭或事件排空时尚未结束。协调 attachment 的等待超时不会撤回已持久化的请求，但下一次 cleanup/find 又创建新身份，导致相同未决动作不断增加。runner 按顺序处理命令；不能通过并发执行所有 native 操作来绕过它，也不能把等待超时当成物理停止。

故障对账进一步确认：原 close 已在 runner 完成，成功回复冻结了最后事件游标；仅剩未应用的回复 delta 时，server 的 close guard 返回 unknown，runner 会沿用原信封重试。回复 delta 只有在 item/turn 结束或断连边界持久化后才确认，但关闭过程可能没有这些末事件。现有 shutdown 已将累计回复持久化为 interrupted，却没有确认这段已保存的原事件前缀，形成循环。

本次仅修复持久化回复的确认边界，不修改 UI、角色模型、任务合同或生产账本，不启动新的模型执行。未确定的原业务动作保留 unknown，不伪造停止证明、原生 turn 终结或 item completed，不删除事件或原请求。请求合并和 Driver.Close 等待是独立潜在缺陷，定向 RED 证据私下保留，不混入此补丁。

## 最小实施

1. 先添加故障回归：17、18、19 三段 delta 的真实累计文本和最后消费游标；streaming 定时保存不确认；terminal 保存失败不确认；确认失败可重试。
2. reply draft 保留最后消费事件的原协调游标。只有 attachment shutdown 缺少原生终结事件时，在 interrupted/unknown 等 terminal 累计文本成功持久化后，调用显式的内部回复前缀确认接口，确认同一原 run/thread/turn/item 的已消费前缀。真实原生终结事件保留既有处理及确认通路，不插入可见消息检查阻断 repair 抑制或后台暂存输出的正常结算。app 在同一确认事务里验证真实原 delta 身份与可见终态正文完全相等，不能把旧终态不可覆写产生的成功 no-op 当作保存新文本。repair 抑制输出不伪造成可见消息。不能通过伪造 item completed 来复用通路。
3. 保留现有 close 的全部 applied guard、最后游标、信封和请求身份。验证冻结旧 close 在三段 delta 未应用时仍返回 unknown，真正保存 terminal 回复并确认前缀后，同一原 close 信封才能成功；不得把原业务 receipt 变成成功。
4. 不改变 server/runner wire 或请求默认语义，不新增用户能力或 CLI 入口。内部可选接口只在协调 attachment 使用，普通本机 harness 维持现有行为。

## 验证和交付

先记录未修改产品代码时的定向失败，再实现最小变更并跑相关 mainthread/app/coordination/runner 回归与 race。保留物理关闭成功、最后事件顺序、错误/取消未知、原权限 fence、升级 park 及原业务 receipt 不被结清的对照。

在明确 scope 与其他候选的交叉风险后才启动完整 make check、规定模型与 effort 的独立 review，并准备可审阅 PR。当前并行角色候选修改 mainthread.go 与 harness 接口；本补丁在 mainthread.go 仅接入既有 shutdown flush，不改变角色合同或 harness 公共接口。若接口或基线变化，重新核对实际补丁。生产恢复由唯一操作方另行执行，不作为测试副作用。
