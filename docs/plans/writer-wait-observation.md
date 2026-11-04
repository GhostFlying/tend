# 单写事务等待的最小定位计划

状态：最小候选已实现，尚未完成完整发布门；诊断基线为 `558df9d6348d6c69d8d04d133e4324ea56c007d5`。

## 已有证据和边界

原 owner 输入在 `Store.Write` 的 `BeginTx` 返回 context cancelled，尚未进入
dedup、消息写入及 outbox 的事务体。单独的 GET 使用读池，快速返回不证明写池畅通。
现有代码将池连接获取与 SQLite `BEGIN IMMEDIATE` 合并在一次 `BeginTx` 调用中，
因此现有错误不能区分两者，也不能确认是哪一个持有者或事务阶段阻塞。

只读 FD 样本未发现受检查 Go 测试打开生产数据库；生产进程自身持 WAL 写锁并不
证明泄漏。短时 CPU profile 看到 native usage 解析、只读事件回放，以及一个来自
`CommitLocalUsage` 的事务开始样本；这些都不证明当前 writer 的调用来源。

本计划不新增业务状态、持久缓存、协议、API、CLI 或配置入口；不修改单 writer、
请求 dedup、原 context、rollback、commit、timeout 或重试规则。Refs: D11、D142。

## 最短路径 A：有可复现原因时直接修

独立源码分析继续核对：

- `App.Execute` 中的 reviewed-context 收集、Apply、`persistOutcome`、
  `refreshManagerTasks` 与 maintenance settlement。
- `persistOutcome` 内无条件的 attention responsibility reconcile，和其递归查询；
  messaging、followup 等尾部处理。
- 绕过 `Execute`、直接调用 `Store.Write` 的 usage 等写入。

若查出明确的无界查询、嵌套写事务、未关闭 rows 或其他阻塞路径，先写稳定 RED，
再做保语义的局部修复。未证明原因时不改变 SQL、调度、事务边界或 usage 语义，
不以未加载正式配置的快速离线样本排除现场公共尾。

## 最短路径 B：仅剩 live 阶段不可见时的窄观测

### 分清池等待与 SQLite begin

`Store.Write` 保留签名与单连接池，显式调用 `s.write.Conn(ctx)`，在同一连接上
执行 `conn.BeginTx(ctx, nil)`。原事务结束后关闭连接，所有错误、取消、rollback
和 commit 的控制流保持不变。分别记录 `connection_wait`、`begin_immediate`、
`callback`、`commit` 的局部耗时，不设置新 deadline，不增加重试。

### 在原事务内标记有限阶段

在 `App.Execute` 通过已有 context 携带经验证的 operation/request/trace 元数据；
事务体仅标记 reviewed-context、Apply、persist-outcome、attention reconcile、
followups、manager refresh、maintenance settlement、request insert 等固定阶段。
标签不得包含 payload、SQL 参数、prompt、provider 原始错误或凭据。

未带业务标签的 `Store.Write` 也必须可辨识，例如仅捕获静态调用函数名；
`CommitLocalUsage` 不得因为绕过 `Execute` 而被漏掉。不为此逐个改写全部写入口。

### 只输出慢摘要和取消摘要

以固定阈值筛选慢写；快速正常写不输出每条 SQL 或每个阶段日志。摘要给出静态
来源、阶段耗时、取消类别与写池 `DBStats` 的计数，不把全局统计差值冒充该请求
独占的等待。服务组装层将安全字段转为现有结构化日志，无新诊断服务。

首版不维护 current-holder 身份 map 或额外全局 holder 状态，不增加任何 timer。
计时器、周期巡检与长期保留身份均不属于本次实现；仅在原写调用结束或取消时
输出该调用的慢摘要，使用可注入的非业务诊断时钟。若仍无法定位未返回的持有者，
先报告证据缺口，不自动扩展观测范围。

## 预计产品文件与验证

最小候选：

- `internal/store/store.go`：显式 Conn 生命周期与四阶段计时。
- `internal/store/write_observation.go`：可选 observer、context 元数据与有限阶段记录。
- `internal/app/app.go`：请求标签和公共尾的有限阶段标记。
- `internal/serverkit/main.go`：现有日志器的慢摘要组装。
- 对应 store/app 的定向测试；不修改 protocol、CLI、Web、迁移或持久状态。

验证顺序：

1. 用 channels 稳定占住唯一 writer，让第二次写取消，证明未进入 callback，
   且分类为连接池等待；随后首写完成、连接可复用。
2. 在独立测试数据库用外部连接持有写事务，证明连接已取得而 begin 尚未完成时
   的错误分类不同；使用原 cancellation 规则，不扩大生产 busy timeout。
3. 证明成功、callback 错误、commit/rollback 错误和原 request replay 的结果语义
   未变；observer 不可改变原错误、事务或输出。
4. 用既有 App 夹具验证公共尾阶段与脱敏字段，先有限 race，不新增 full 并行负载。

预计实现与 RED/GREEN 为 15–25 分钟，定向验证约 2–5 分钟，均为估计而非实测。
实际编译/测试遵守唯一资源预算。不得改写其他候选的测试树或把局部通过当完整门。
需要本地受控候选观测时由 operator 绑定源码与产物，正式 merge 仍保完整检查门。

## 验收与停止条件

能从一条取消或慢摘要区分连接等待、SQLite begin、事务体具体阶段和 commit，
并识别其写入来源，才算观测能力有效；不承诺此补丁本身恢复业务。拿到源码与
复现支持的原因后回到路径 A，停止扩展观测范围。禁止增加并发 writer、盲增
timeout、自动重放原请求、停止健康检查、重启服务或暴露公网 pprof。

## 首版实现收口

事务结束与连接释放后才调用 observer；observer 重入写入不占着原 writer。
显式连接需要在 callback panic 时先 rollback 再关闭，否则 `Conn.Close` 可能等待
未结束的事务；panic 继续传播，不转换为业务成功。原错误包装与 `errors.Is` 链保留。

阶段为 inclusive，特别是 callback 包含 `persist_outcome`，后者包含 attention 和
followup。日志不可将这些值相加当成总耗时；pool wait 累计量是整个连接池的计数，
不是本请求独占的等待。缺少业务标签时只保留调用 PC，真正输出摘要才解析函数名。

真实 SQLite 的 BEGIN 可能忽略刚发生的取消，随后在 statement/commit 观察到取消；
因此分段正确性同时覆盖真实 SQLite 写锁竞争和最小可取消 driver，不修改生产
driver 的 context 或 timeout 行为。另覆盖自动 rollback 先完成而 Commit 返回
`ErrTxDone`：只记录失败边界的 context 状态并保留取消摘要，不替换错误、不宣称
取消是任意业务错误的原因；已成功 commit 后才取消仍是成功。
