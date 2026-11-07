# 维护装配预算的阶段证据

范围仅为两项二十五秒 serverkit 装配用例。本次没有实际服务或 runner 环境维护。

| 受控样本 | fresh 迁移 | 等待独立 link | helper 总耗时 |
| --- | --- | --- | --- |
| 原候选，默认轮询，第一个用例 | 13.50 s | 4.14 s | 20.60 s |
| 原候选，默认轮询，receipt 用例 | 14.51 s | 4.20 s | 21.95 s |
| task base，同一诊断，第一个用例 | 14.26 s | 1.92 s | 21.61 s |
| 仅注入测试轮询，第一用例 | 15.26 s | 0.062 s | 19.50 s |
| 仅注入测试轮询，receipt 用例 | 14.19 s | 0.112 s | 18.29 s |

这些是有界样本，不是通常性能基线或原整轮失败的逐例阶段恢复。原完整检查的两项超时仍为
真实失败；当时没有阶段日志，不能证明具体失败阶段或负载因果。base 对照只支持迁移/装配的
主要成本并非 Capability 新增，不排除其它间接影响。

源码确认 controller 默认 Interval 为五秒。测试模拟独立 updater 提交后，真实 controller 在
后继 tick 才读取该提交、校验身份、写入原 request/CAS 的 DB link 并取得独立 ACK。
测试将这段与轮询相位有关的空等和完整 fresh 迁移放进同一个固定二十五秒预算，余量很小。

修复仅通过 serverkit 的显式外部边界传入已有 Interval，隔离测试选短间隔，正式 RunFile 的
空依赖继续沿原五秒默认值。真实装配、fresh 数据库、kernel identity、health 两个递增样本、
不确定 compaction 阻止 Ready、独立 updater 状态、DB link、ACK、receipt 启动和未知 Run/pin
保持等断言全部原样执行。不扩大二十五秒总预算或两秒 HTTP 超时，不直接制造 Ready/link，
也不省略 production controller。保留阶段日志以便未来失败准确归因。

原样本通过不能替代完整检查。只在上述阶段证据及修复前后 link 等待变化成立后，安排一次
最终完整默认预算检查，并保留成功逐例 JSON。若该检查失败则重新判断，不循环挑绿。

Refs: D133、D183；[有界计划](../plans/capability-maintenance-timeouts.md)。
