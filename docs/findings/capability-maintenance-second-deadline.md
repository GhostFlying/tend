# 维护装配第二次 deadline 的有界诊断结果

本轮只执行一次原单例 race CPU profile，未重跑完整检查，未修改产品源码或测试条件。
原退役声明修复 `2aa0678b`、失败 full 与停止证明保持；profile 的 `8bff9b2a` 仅新增诊断计划。

## 实际样本

使用当前源码编译的 race 测试二进制，在独立进程中直接执行原 inner helper，与原父测试
设置同样的维护 launch 环境。GOMAXPROCS=2，原25秒 context、两秒 HTTP timeout、fresh
迁移、真实 production assembly 和全部核验断言均保持。无人工压力或服务维护。
profile 文件与完整 stdout、exit/wall/child CPU/head/tree 的回执保留于 Task 证据。

| 阶段 | 原 full 失败样本 | 本次唯一 profile 样本 |
| --- | --- | --- |
| seed store.Open 完成 | 19.478 s | 15.409 s |
| production Run 开始 | 20.002 s | 15.904 s |
| initialized | 22.016 s | 17.926 s |
| 首个 health | 2.002 s 超时，后续请求成功 | 1.772 s 成功，ready=false |
| 解除 compaction 后 ready health | 总25.006 s期限耗尽 | 总20.121 s，ready=true |
| 第二个递增健康样本 | 未到达 | 总20.473 s |
| 独立 updater link / verified | 未到达 | 总20.517 / 20.568 s |
| helper 结果 | deadline exceeded，25.30 s | pass，20.77 s |

profile 命令退出0，含 race 退出开销的进程 elapsed=22.220 s，child user=22.398 s、
system=0.782 s。两个执行所处的并发条件不同；单样本不是性能基线或原失败复现。

## 已证、推断与未知

- **已证**：profile duration20.78 s，CPU样本22.60 s（多线程允许CPU超过wall）。
  Store.migrate累计11.16 s/49.38%，OpenWithOptions累计11.64 s/51.50%；
  SQLite schema初始化7.04 s/31.15%，parser9.20 s/40.71%。这些调用栈有包含关系，不能相加。
  runtime.checkptrBase累计5.37 s、TSAN read flat2.38 s显示race/checkptr成本存在。
- **已证**：maintenanceHealth累计CPU1.22 s/5.40%，其中LinuxProcess.Inspect/hashFile
  0.96 s/4.25%。源码每个健康样本都实际校验内核身份和可执行文件SHA，不能用标签代替。
- **已证**：store.Open在原full样本耗时约19.48 s，在本次隔离样本约15.41 s；真实链条在
  本次样本仍经过uncertain compaction阻止Ready、解除后两个递增健康样本、独立updater
  ACK/DB link、原unknown/pin不变与最终admission门槛。该单次成功不覆盖原失败。
- **推断**：fresh迁移及schema解析显著消耗原25秒预算，连同物理身份与真实组件启动成本，
  余量对执行条件敏感。profile不支持把全部成本归为外部等待，也未证明“绝大多数CPU仅在迁移”
  的较强假设；migration仅约49.4%，不能将重叠的runtime成本随意算进该比例。
- **未知**：原full额外耗时是否来自CPU竞争、race调度、I/O或其它路径；没有原full的对应
  CPU/调度/I/O样本，不能由本次profile反推其精确根因。未定位可在当前范围内安全修正的
  源码缺陷，也未证实退役声明改动是原因。

## 退出与未完成项

按[有限计划](../plans/capability-maintenance-second-deadline.md)退出：样本没有复现并解释
原失败，不以通过样本为依据立即重跑full，不重复修复已验证P2，不改变25秒起点、任何超时、
fresh迁移或hash/readiness/请求/CAS/unknown门槛。通用迁移/SQLite性能调整或改变fixture条件
需要另行明确的调查范围与证据；本轮不实施推测性优化，也不声称需要某种生产维护已获证明。

将“有界证据尚不足以选定最小修复”沿原责任链呈报，等待能解释原full失败的新证据或合法
版本化的下一步范围。最终完整检查、Maple独立复验、远端CI和交付仍未完成；生产仍未包含
本候选。保持原问题、失败、spec/Run及原生停止历史，无merge/deploy或维护切换。

Refs: D133、D183。
