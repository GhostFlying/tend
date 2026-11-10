# Manager 原回执投递总预算

## 问题

正常 desired-run manager 已完成 native close 并持久化原 reply，但回执因原事件未应用
返回 unknown 时，会在原 attempt context 内无限重试。逐次 HTTP 超时与退避并不约束
整个阶段，因而无法退栈完成真实停止上报；这与恢复入口是否已有限时是不同的问题。

## 实施

1. 先复现正常 desired-run、没有外部取消时，成功 close 的原回执持续 unknown 使
   真实 stopped 无法上报。冻结 request ID、payload hash、close/event 游标及原 pin。
2. manager.native_reply 投递共用既有 RequestTimeout 作为一次阶段总预算；deadline
   只在进入回执函数时创建，不随 retry 更新。native command/model 执行在此之前，
   不受这个预算限制。不新增配置、协议、迁移、权限或业务状态。
3. 到期保留原 envelope 与 unknown，返回既有真实 close/ConfirmStopped、原事件、
   stopped、迟到原 reply 与 receipt-only 历史恢复路径；不重放 native、不变更业务
   Task/spec/generation/pin，不伪造业务结论或未确认的物理退出。
4. 复用真实 App/HTTP manager fixture 验证无外部取消的完整收尾与自动派生 reader，
   以及暂时延迟在预算内正常 ACK。定向覆盖 D210 普通/受控原事实恢复边界。
5. 定向 regular/race/vet 保存原生 JSON 与 exit，提交供协调者执行最终完整检查、
   独立 review、CI 和发布部署。不得将本地 fixture 当作生产物理验收。
