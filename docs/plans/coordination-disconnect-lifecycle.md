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
