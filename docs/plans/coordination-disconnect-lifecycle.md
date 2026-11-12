# 协调连接断连的完整生命周期

## 原因与边界

现场原载体已物理停止并释放 lease，后继已领取但没有原生请求；原连接最后一项为空 thread 的 disconnected，且旧、新 thread 均曾附着。当前实现把该连接级结束随机路由给一个 thread，随后等待消费者关闭 Session。迁移后的旧 thread 无消费者时，当前 Manager 收不到结束，原 delivery 和 pool attachment 持续占用。源码证实这条可永久等待的路径；现场没有 goroutine stack，不能将具体 map 选择宣称为直接观察。

## 实施

1. 先以真实 coordination Session 在同连接恢复旧 thread、建立新 thread，验证连接级断连通知全部附着消费者、先前尾事件顺序和原 source 身份不变；旧实现必须确定性 RED，不依赖 map 遍历顺序。
2. 将连接级结束作为 Session 生命周期事实，交付已排队尾事件后结束事件流并取消未结调用/handler，不能依赖任选一个 thread 的消费者发起 Close。Close 保持幂等，取消不表示未知操作成功或可重发。历史 thread end 仍只表示原历史边界，不关闭当前连接。无人消费的旧 thread 即使缓冲区满，也不能拖住连接结束；保留已排队正文，读完后以 EOF 结束，未 ACK 的原断连记录仍留在持久传输中供后继读取。
3. 以真实 App、runner 与 BackgroundPool 现有替身组合覆盖旧/新 thread、外部等待物理停止、原 attachment 退出、同 Task 的 receipt-only 后继读原回执及维护 park；不调用模型、不重放原输入、不补造业务终态。
4. 运行受影响必要 regular/race/vet，保存 RED、失败与最终原生 JSON，冻结修改清单。最终完整检查、独立审阅、CI、合入和部署由协调者统一执行。

不增加权限、协议字段、schema、旁路状态表或新审批；D227 的物理 EOF、真实回执与 producer/原件边界保持。

## 状态

源码及定向回归已完成并冻结。已保存原实现的确定性 RED、首轮替身生命周期失败及修正后的原生结果。连接层 regular、coordination/mainthread 受影响 race、真实迁移与原外部等待 E2E race、既有 App park 正负链和 vet 已通过。E2E 使用真实 App/runner/pool 路径与无模型替身，另验证原回执结算后的正式 archive preparation 和物理排空；实际安装及线上恢复仍未执行。

最终完整检查、独立审阅、CI、合入与部署留待统一执行。

## 原审阅修正：先结束再附着

独立审阅用原 App/runner 传输证实两项同源缺口：事件泵在首个消费者附着前读取 live 断连后取消了整个 Session，连已确认 closing metadata 的本地附着也被拒绝；空 thread 的历史 end 在没有消费者时没有保留，游标却已前移。两项原复现均保留，不提高测试超时。

统一模型区分连接/原生执行结束、持久尾事件可读、消费者附着与新业务准入：

- 已观察连接结束时取消未结原生调用，关闭既有事件流，但不令原件和本地消费者附着失效。新附着只能取原 thread 已确认的 closing metadata 与未消费尾部，事件流按原序读完即 EOF；不重启事件泵或执行工具。
- runner 传输在事件流结束后只允许已有本地 closing metadata 和原关闭收尾；缺原元数据直接拒绝，不派发新的 resume、输入、工具或模型查询。该本地生命周期事实不是物理停机或业务成功证明。
- 连接 EOF 与账本物理结束读取可竞争；models/find/usage 的纯观测失败沿既有拒绝语义，真实成功和调用者自己的取消不改写，已排原生 mutation 的丢回复仍未知。
- 空 thread 的 live/history end 在尚无消费者时进入既有 orphan 队列，保留原 source/sequence；历史边界不关闭当前连接。
- startup 的原生观测先返回失败时，仍先消费当时已排队的原尾事件和 ACK，不以观测就绪或 Session 尚存为前提，也不吞掉原失败。
- 回归覆盖真实 App 原 close ACK guard、pre/post-attachment live/history、原双 thread/满缓冲、未知操作及整个 external-wait→history→pool 排空链。恢复不以旧 Session 恰好存活为条件。

本轮修正及定向验证已完成：原审阅四项复现保持原件不变，基线均 RED，修正后的 Session/真实 runner/启动尾部组合 race 与两条真实生命周期 E2E race 均 GREEN，受影响包 vet 通过。已保留原替身失败、启动尾部对照 RED 和首轮 race 暴露的纯观测退出分类失败；未提高预算或重跑宽整包。完整检查、原 reviewer 增量复查、CI、合入及部署仍由协调者执行，本轮没有生产操作。
