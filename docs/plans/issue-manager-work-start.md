# Issue 负责人领取执行时推进工作状态

## 业务目标与故障路径

用户看到负责人已经工作，Issue 却仍在“待推进”。当前普通 worker 的
`Claim → claimOwnerExecution → markIssueWorkStarted` 会推进状态；
负责人走 `Claim → claimIssueManager`，只保存责任归属和执行分配，缺少推进。
修复沿既有 D127、D218 状态和准入规则，不增加状态或迁移。

## 实施计划

1. 让普通负责人领取执行与 worker 使用相同工作开始语义：真实领取并分配
   Run/lease 后，todo 转为 in_progress。首次责任领取合并在已有 Issue 修订中，
   同事务推进允许的祖先；同 Task 后继领取复用既有状态推进函数。
2. 排队和仅选择类别不推进。领取表示已经分配、正在启动，具体启动成功、失败、
   等待和 unknown 继续由 Run 执行事实展示，不声称模型已成功运行。
3. 只补读旧回执的领取不开始新业务；保留 backlog、暂停、交接、in_review、
   终态和祖先边界。恢复和重启继续沿原 Task/Run 身份，不补历史批量迁移。
   本次已启动的负责人不因自动状态推进重复收到状态唤醒；祖先通知及原外部通知保留。
4. 扩展真实 App/SQLite 入口测试，验证准入、领取、启动、重复领取、祖先、
   暂停/恢复和重开存储，以及 receipt-only 边界。扩展现有 HTTP/runner E2E，
   从真实正常入口读回负责人开始执行时的 Issue 状态。
5. 定向回归后运行最终候选完整 make check、独立审阅与 PR CI；按既有授权
   合入并沿完整发布集受控交付，最终以实际回执说明线上验证与未覆盖范围。

## 验证边界

App 测试使用真实存储和业务命令，时钟、执行器清单为替身；HTTP E2E 使用真实
server、runner 与 journal，模型 provider 为 fake harness。不会通过测试中的
手动 set_status(in_progress) 或直接 SQL 填入目标状态证明本修复。生产当前
Issue 的人工正常状态维护不作为修复验收。已有只读状态接口与 CLI 无需新增命令，
server/runner wire 不变。

## 完整检查中的取消回归同步

完整 race 检查揭示已有 pending-read 回归在启动 Models goroutine 后立即报告
物理停止，没有证明只读请求已经入队。保留原断言的诊断捕获到失败时调用仍在
ManagerRunEnded 的 SQLite 新读连接初始化中；它最终正确返回 ErrRejected，
但混入了入队前准备时间，不能据此证明 pending 请求未被释放。

先通过既有 Changes 通知和正式 manager.native_claim 确认原 models 请求及 ID，
再报告原 Run 物理停止。保留原 3 秒业务 context、300ms 退出断言及全部失败
原件；不增加生产等待时间、不跳过检查。定向 race 验证同步后的真实入队到停止
路径，再由原 reviewer Task 核对增量；完整最终候选检查仍为合入门槛。
