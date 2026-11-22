# 同一 Task 的独立后续交付

原案例中，#407 已按原授权合入 main 并结清。随后 #405 仍需按它自己的候选、审阅和 integration 授权执行。现有 prepare 对已结清责任正确拒绝写入，绕行方案是建立独立 Task。本方案保留该入口，并新增同一仍有效 Task 的显式整笔后续交付。以两个真实的 complete 回执和历史读回为验收终点。生产对象的原候选、授权和结算均为只读案例；本系统修复的源码交付终点是 integration。

1. 在 Issue delivery 命令加入 start_next。调用者提供已结清自执行的原 Run 和绑定修订、当前 Issue 和 Task 控制、准确且独立的候选。沿现有事务、权限校验、候选准备和审批链路运行。要求原结算可核对且相关执行、provider 和升级无未决结果。候选身份与历史重叠时拒绝。
2. 原交付复用 execution history 的不可变归档。继续使用相同 Task，单调推进 duty revision。新交付用新的原始事件作为稳定身份。后续修改显式绑定该身份，原请求不作用于新交付。原请求 ID 仍按 envelope 回执处理未知结果。
3. 同步现有 CLI、协调工具、生成 API 契约和历史读回。Web 当前只展示 provider 观测和 Review，无 update-delivery 写入入口。
4. 用真实应用命令走 prepare、review、approve、bind、submit_result、complete，随后 start_next 再结清，保留两个原始候选和结算。覆盖重复/并发、权限/控制过期、候选重复、unknown、未结清和旧入口。
