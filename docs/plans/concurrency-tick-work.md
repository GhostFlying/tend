# 并发控制 tick 的局部重复工作修复

状态：局部候选已实现，完整发布门尚未完成；基线为观测候选
`9aba28f69c26a9e624b073d960ae93412a6133a5`。

## 证据与目标

一批有边界、截断的慢写摘要中，7 次 `tickConcurrency` 的 callback 为
0.951–4.157 秒，占这批已选 callback 累计时间约 83.5%。部分调用几乎没有连接池
等待，事务体仍超过两秒。相同样本的 usage 写入主要在排队，不支持先扩大 usage
或 attention 查询优化。该样本不能代表全部事务，尚未证明某条 SQL 是唯一原因。

目标是用合成账本确认 tick 内重复工作的主导子段，再只修一项。保留已有慢写
观测，不增加新的诊断系统。Refs: D11。

## 首个有界复现

- 同一业务时钟、两个合法配置的并发组、有效执行身份，分别构造 N=1/16/64 个
  active execution；有限数量的已终态历史单独变化。
- 运行真实 `tickConcurrency`；在等价的重置 fixture 上分别测 execution sync、
  waiting 调度事实读取、最终 group sample 和 retention 的局部成本。
- 用测试专用 SQL 计数和等价快照确认重复工作，不把机器耗时阈值作为正确性断言。
- 快照覆盖 group current/revision、占用、健康、观察区间、未知与 pending receipt、
  原执行身份和真正的终态关闭依据；正常调度结果必须相同。

## 最小修复选择

若 execution sync 的逐项前后 group sample 占优，优先消除没有事实变化时重复的
读取/写入，或只在同一事务内复用相同事实。不得简单合并所有最终状态：同一时间
的中间 unhealthy transition 仍可能重置观察证明，必须保留其语义。

若 waiting/scheduler facts 占优，只优化该路径已证明的重复查询；不得跳过
scheduler、host/owner/executor/dependency 条件，也不得把过时读移到事务外。
若需要 schema/index，先报告 query-plan 与计数依据，再决定追加迁移，不修改旧迁移。

## 范围与验证

先在原 PR 分支实施。候选文件仅为选中的 concurrency 路径及其定向测试；不改
controller 周期、单 writer、事务边界、请求去重、业务时钟、预算配置/当前值、
unknown/pending 账本或历史身份，不引入持久缓存。

先实际 RED，再最小 GREEN 与有限 race；复核不健康中间状态、绑定变化、历史
terminal、pending receipt 保留与 waiting 等价。独立 review 和 PR CI 随新 exact
head 并行；本阶段不运行新的完整本地检查，不进行生产读取或流量。先交有界复现
的真实结果，未确定占优子段时不为赶进度扩大产品改动。

## 有界复现与实现收口

合成数据通过注册 executor、配置组、正式 Claim 与模拟的原生启动事实建立 active
执行；两个测试组的 current 固定为 128，不在采样过程中改预算。历史对照使用
已完成的合法旧任务。测试专用 TEMP trigger 计数仅存在于测试 writer 连接。

| active / 历史 | 原 sync / waiting | 原同组更新次数 | 修后 sync / waiting | 修后同组更新次数 |
| --- | --- | --- | --- | --- |
| 1 / 0 | 35 / 55 ms | 2 | 26 / 59 ms | 1 |
| 16 / 0 | 455 / 47 ms | 32 | 184 / 58 ms | 1 |
| 64 / 0 | 5093 / 106 ms | 128 | 718 / 65 ms | 1 |
| 16 / 16 | 616 / 63 ms | 32 | 153 / 62 ms | 1 |

耗时含测试计数开销，仅为这组合成样本的阶段对照，不设置机器秒级门槛，不承诺
生产延迟。首轮 sweep 在末尾读 TEMP 计数时误用读池并失败，已修正测试连接；
失败前在 writer 内取得的阶段计数与耗时保留，另一个小型回归稳定证明原实现对
4 个未变执行重复更新同一组 8 次。

唯一产品改动位于 execution sync：一次 sync 内重用已采样组，完全相同的有效
执行事实不再 UPSERT。比较保留 `MAX(last_seen)`、stopped 的原 group 归属，
以及合并原 native/event 后的 source、state、parent。真实 mutation 发生时清空
整个局部采样缓存并立即后采样，因为 child 变化可以影响不同组的 parent 叶负载。
缓存不跨 sync，也不覆盖 feedback、Increase、waiting 或最终组采样。

逐步快照与原无缓存路径对照，覆盖同时间健康→unknown→健康的证明失效、跨组
parent/child、重新绑定后的 stopped 归属、旧 lastSeen、native/event 合并。大型
诊断 sweep 留作调查原件；常规回归只保留小型确定性计数及这些状态等价断言。
不增加迁移、索引、持久缓存或新观测框架。

## 真实 heartbeat 推进场景

候选运行后的第二批有界慢写摘要仍有5次 tick callback 2.651–5.056秒，archive 控制原回执重放主要等待 writer 连接约9.817秒。两窗负载不同，不能归因退化或承诺修复收益；稳定执行事实的首轮合成结果未覆盖 host heartbeat 持续推进。源码显示 sync 从 host.last_seen_at 取每个 run 的 liveness，任何前进都会使现有 no-op 条件失败并清空整个局部采样缓存。

先以同一合法 active fixture 比较稳定轮与推进业务时钟和 host heartbeat 的轮次，记录 group sampling/UPDATE 数及局部 callback 耗时；秒级耗时仅作诊断，不作正确性门槛。保持有限历史条件一致，逐步与无缓存路径对照完整 group/execution 快照，覆盖 freshness 阈值与健康→unknown→健康中间重置。

如 RED 证实只更新 lastSeen 造成重复采样，唯一候选范围是 execution sync 的事务内采样缓存：只有所有其他有效事实相同、且在本轮固定 now 下 liveness 健康分类不变时，更新并持久化 lastSeen 后保留已采样组。每个组每轮至少实际采样一次；真实 state、group、parent、source 或健康变化仍清空全部局部缓存并按原前后顺序采样，避免跨组 parent 叶关系与中间不健康状态丢失。缓存不跨事务或业务时间。

仍保持原预算、unknown/pending、clock、writer、调度与事务语义；不新增 timer、timeout、持久状态或生产读取。先有限2CPU race RED/GREEN与等价回归，再冻结新产品版本并重新 review、CI、候选验证，不沿用旧候选版本的产品批准。

局部 RED 已确认：N=4、已合法完成历史任务2个，同 fixture 稳定轮 group UPDATE=1；时钟推进1秒并通过正式 RuntimeHeartbeat 后 group UPDATE=5，所有执行 liveness 已更新。修后 heartbeat group UPDATE=1，execution UPDATE 仍保留8次（4个 liveness 更新与原 sync 尾部4个重绑定更新），未靠丢写 lastSeen 达成计数。计时分别存在不同编译/共享负载背景，不据此承诺延迟提速。逐步无缓存对照包含 fresh、同为 stale、跨 freshness 阈值、unknown，以及既有跨组 parent、group rebind、健康中间重置和 native/event 合并。
