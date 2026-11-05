# Runner maintenance probe quiescence

## 范围与边界

旧 runner 的维护 ACK 只证明 claim 和 workspace 入口收敛；reconciler 仍会调用 execution readiness refresh，并在 retained manager 解析时启动短暂 native probe。暂停整个父线程组后，子进程即使自然退出也需要父恢复运行才能 reap。此次不改变历史 Run、pin、lease、assignment、unknown 或已冻结的维护证据。

本计划分为两个独立产物：本机受控升级使用的私有有限冻结 helper，以及未来 runner 的产品修复。产品修复不能使正在运行的旧二进制新增入口，也不能替代旧进程切换时的最终全线程冻结与严格 no-child 校验。

## Q1：单窗口有限冻结 helper

- 在独有私有目录实现新 helper，保留原 helper 原件及 hash。
- 复用 identity-bound pidfd、独立 supervisor、worker 完全退出并 reap 后才能由 supervisor 恢复父的顺序。
- 一次调用最多三轮，物理总预算不超过二十秒，使用 caller 原始 not-before 与 deadline，永不刷新六十秒的上层窗口。
- 每轮失败仅在明确 child-HOLD、原父已恢复运行、该轮 worker 已确认退出并被 reap 后才允许下一轮；超时、身份变化、worker unknown 一律整体 HOLD。
- child-HOLD 在恢复父前采集有界的 pid、start、ppid、exe、cgroup、cwd 与 argv 指纹/允许词摘要；不读取环境，不保存完整参数或凭据。
- 只有同一 pidfd/start/exe、稳定全线程停止、严格 children 为空、最终同一快照复核及预算尚有效时可切断旧父。不得杀 child、忽略 zombie、伪造 stopped 或改变业务维护身份。
- 有限本地 fixture 验证短 child 的原失败与新成功、持久 child 的 HOLD、多线程冻结、worker crash/hang、caller 消失、身份拒绝、截止时间拒绝及无 late kill。验证只操作自有 fixture。

## Q2：产品最小修复

优先仅在 runner 包增加 readiness refresh admission：与 control revision 同锁检查，维护模式关闭新 refresh，已有 refresh 由独立计数覆盖其完整返回生命周期；runtime observation 在已有 refresh 尚未结束时保持 blocker，不能将其误报为 quiesced。所有新 readiness refresh 必须通过此入口，正常控制恢复后由既有 reconciler 唤醒，不新增 timer。

该最小修复先覆盖后台 Refresh 路径，不能宣称是 process-wide fork fence。业务执行、retained/native reconciliation 等允许在维护期继续的路径仍由原 Run/resource blocker 约束。若确认这些路径也有未登记 probe，后续需要 Pool 全部 probe admission 生命周期接口，并与执行入口的唯一整合作者顺序合入。

先输出具体最小 patch 与定向测试；不改公共协议、生成代码、Pool 或历史迁移。与单入口整合作者协作，由其唯一顺序整合 runner 文件。完整检查、独立 review、CI 与合入权限仍按仓库约定处理；此次不运行完整检查，不部署产品 patch。

## Q3：与库存观测隔离顺序整合

本次独立候选在 Q2 维护准入之上共同验证 D143 的逐 snapshot 库存观测隔离，
不引入单入口或 Capability 业务。先独立提交计划，再按原 Q2 产品补丁整合 runner
控制与后台刷新入口；保留既有进行中 Refresh 的全部返回和清理生命周期。
维护期间的心跳继续读取同一精确库存状态，但当前控制模式与 readiness blocker
每次重新计算，不能借用先前 normal 心跳报告 ready。

定向验证覆盖维护拒绝新刷新、bootstrap maintenance、取消、normal 恢复唤醒既有
reconciler、进行中探活退出前 blocker 保留，以及当前/retained 原 pin 的并发观测。
最终精确 head 仅运行一次规定的独立 review，完整本地检查与 PR CI 由统一交付调度。
