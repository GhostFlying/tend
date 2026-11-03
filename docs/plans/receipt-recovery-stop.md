# 原收据未知时的显式 server 崩溃恢复维护

## 设计审查与边界

旧版本归档准备可能持续恢复只读 native 收据而无法报告 HostedDrained。
正常维护仍要求真实排空；新增独立恢复动作不把未知生命周期伪装成 drained。
实现前已独立审查：优雅 SIGTERM 会发送 native close，runner 租约 fencing 也会产生
真实物理停止。因此仅停旧 server 不足以保留原业务 Run，必须同时安装精确持久保护。

1. 固定新的 scope/request/CAS/窗口；真实 archive prepared、owner 零、所有 host 新鲜
   确认维护并停止新 claim。旧版本已知的 hosted-effect 检查除 pending receipt 外均须零；
   未知 blocker 拒绝。保留原 false drain 观测，不调用 unavailable 新 API。
2. owner 完整 task 分页、每 task 完整 runs 捕获所有 open Run（包括 terminal task 上的
   unknown Run），按 host 对齐 archive count。prepare 下两次完整捕获必须一致且匹配
   固定计划，否则拒绝。旧投影不含 lease；新 server 注册同事务检查实际完整集合并冻结
   原 lease、hosted execution 与原 unknown receipt 请求身份。已 terminal 只留事实，不复活。
3. 显式恢复停止仅以 pidfd 绑定旧 server 完整身份，保存 crash 意图及原 unknown。
   不发送 native business close、不信号 runner 或共享 native 进程组。确认原进程实际 gone，
   原控制连接失效后才允许备份与前滚。未确认的外部执行保留 unknown 和 pin，
   禁止沿原身份重新派发业务动作，不声称所有 native 子进程已经退出。
4. 注册保护先于任何 runner RPC、dispatcher 或 hosted 启动。原 manager 保护绑定原 lease、
   原 unknown 收据、仍合法 desired-run；普通 captured Run 的同 lease 物理 give-up 也须
   留存未分类原事实而非推断部署原因，不能因旧协议无法区分原因就结束保留 Run。保存真实 physical close/Stopped/lastseq
   与原请求结果，不因这次维护物理停止自动 endRun、actiondone 或 Task waiting。
   明确用户 stop/cancel/epoch变化优先正常业务控制；正式业务结果和未保护完成不吞。
5. unknown 资格在注册时冻结。即使收据后来读清，晚到旧 lease 的 physical stop 仍只记
   原事实；link 不能删除保护。真正业务完成需合法的新独立动作，旧请求永久幂等。
6. prelink 延后 native 收据和模型启动，并拒绝新 native business claim；允许原事实和
   heartbeat 保存。健康证明真实组件静止、scope/保护已注册及数据库可读，不返回常真。
   两真实 health 与独立 link 后恢复原身份对账，archive 仍须显式 finish。

## 实施与验证

先实现协议/CLI/更新器独立动作及精确 crash 观测，再实现迁移 0082 的物理事实/Run 保护
和 serverkit 启动 fence。保留 normal/D154 行为，不修改已提交迁移，不升级 runner wire。
实际旧 binary 隔离故障用例需覆盖 parent crash 后真实 runner fencing/Stopped、原 Run/pin/
决策不变、晚到回执、用户取消/不同 lease/普通完成负例、WAL 及 request 原身份对账，
再统一独立 review、完整检查和 CI。旧 Hello 对受保护原 lease 明确让整台 host 保持 pending，不伪造 pause/stop/retain；
独立 owner 控制使用已持久物理事实结算，旧请求不修改。此变更不承诺清理所有历史
unknown 或 runner drain。

Refs: D155、D156。
