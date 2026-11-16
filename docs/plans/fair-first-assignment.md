# 新责任首次分配的公平选择

## 目标与边界

修复 runner.claim 中首次选择 Issue owner 和普通 Task 执行器时按 local_id
固定抢先的问题。工作分类、能力声明、显式指派、runtime/profile、host/workspace、
slot、租约与共享账号并发仍是准入条件；Agent 可以并发，不增加每 Agent busy 锁。
不修改通用工具接口、分类模型或生产 Agent 配置。本轮只合入，不部署。

## 调用路径与方案

- runner.claim 先按原 run_id 恢复已提交 assignment；只有新的 claim 才收集候选。
- 在 app 的外部依赖边界注入随机排列函数，默认标准库 Fisher–Yates shuffle。
  在 claim 事务里对当前 host 的新执行器列表排列一次。scheduler 保持纯函数，
  按输入顺序选择第一个实际合格的执行器，取消内部 local_id 排序。
- 同一次 claim 的 Pick/Skip 与最终 SelectExecution 共用排列；run、完整 pin、
  责任与 lease 沿现有事务原子持久化。事务失败未产生责任；提交后重试先恢复原结果。
- 核对并保留已有 manager/worker/reviewer、问题/暂停交接及 Task 续接的真实 pin。
  发现没有持久 pin 的旧普通 Task 时，以原角色和 spec 的实际 run 原件恢复身份，
  不把旧责任当成新目标抽签。真实不可用时沿原准入原因等待，不换 Agent。
  已授权的 workspace start-over 保留原语义：丢弃的 host 不作为续接来源。
- 在一个 host 上的合格新执行器等概率选择。runner 拉取竞争仍决定哪个 host 先领取；
  本次不承诺全局 Agent 等概率，也不加入 runner slot 权重。全局权重需另一层
  分配协议；不能用展示任务计数代替真实资源准入。
- 只改变 server 内部选择策略，无新 wire 字段或 runner 通信约束；保留协议兼容。

## 验证与故障契约

从真实库存登记、hello、创建/启动 Issue 或 Task 到 claim、落库、重放和继续执行，
用可控排列验证每个候选可被选中及重试不抽签；覆盖资源过滤、显式指派、原 pin、
单 Agent 并发和未结 unknown 占用。纯 scheduler 的资格解释不消耗随机状态。
运行相应 race 回归与 make check，完整 PR CI；实现收敛后在原 Issue 安排独立
whole-PR 审阅。最终候选更新时据真实 delta 复用或补查证据，fresh-main rebase merge。

## 并行文件边界

本责任涉及 scheduler/agent_execution、app/runner、真实首次/续接选择事实及相应测试。
并行配置清理责任涉及 instructions 模板/加载组合与私有待应用差异。共享设计文档
只添加本决策和链接；实际交叉改动据 diff 协调。不得为此等待而停止独立调查。
