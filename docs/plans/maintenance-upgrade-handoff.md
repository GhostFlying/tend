# 临时维护到升级的连续准入交接

## 业务契约与源码调查

起点是 owner 已通过 `hosts.maintenance` 停止新领取，runner 已确认当前控制修订，
已有 Run、租约及结果仍存在。终点是正式升级接管维护责任，准备、server 切换及
目标 runner 排空期间不重新开放领取；只有该 host 安装提交或明确安全恢复后开放。

当前路径为 `tend upgrade start` → 本机 `Controller.Start` → `StartUpgrade.Apply`
→ `prepareUpgradeHosts` → `advanceUpgradePreparation` → server / runner 升级。
当前 main 只接纳 normal 控制及 normal ACK，故必须先解除临时维护；发布包准备
也只接受 normal ACK，server 提交后还统一恢复 normal。只放宽入口不足以消除窗口。

本次只交接无其他升级/安装授权的独立临时维护；不同批次、未确认修订、过期心跳、
未知安装授权继续拒绝。没有新增安装权限或协议字段，部署仍需单独授权。

## 最小实现（Refs: D253）

1. 在写入新批次的同一事务校验 standalone maintenance 的当前 revision、ACK、
   升级身份及能力；把交接来源作为每 host 的持久批次事实，准备控制提升 revision，
   绑定真实新 upgrade ID，保持 maintenance，不经过 normal。
2. 准备允许 maintenance / ready ACK，runner 在既有 Run 尚未排空时也可准备缓存，
   PrepareOnly 仍不授予安装权限。保持 normal 起点的既有在线准备行为。
3. server 提交后保留交接 host 的门禁；串行 runner 升级直接从新鲜维护 ACK 排空。
   同哈希跳过也须显式恢复并收到 normal ACK，不能留下无界维护。
4. 复用原 20 分钟整体期限与阶段 drain / restart / grant 期限。不续期固定授权。
   准入失败不动原维护；准入后准备/排空失败或取消安全恢复未替换的原进程，
   已授权/未知候选继续凭原批次及真实终态回执对账；回滚沿既有正式出口恢复。

## 回归与证据

- 使用真实 `HostMaintenance` → runtime heartbeat → `StartUpgrade`，不以预置升级
  fixture 代替交接。每个阶段实际尝试 claim，验证无新 Run，旧 claim 可回放，
  旧 Run / lease / immutable artifact 保持。另验证入口拒绝不会改动原维护。
- 覆盖 stale ACK、错误身份、未清授权、fresh revision，准备卡住/阶段超时、
  cancel、安全回滚、同哈希恢复及 normal 起点不退化。
- runner 回归通过实际下载入口证明维护中的活跃 Run 不阻止 PrepareOnly，且不提交安装。
- 本地 `make check` 按影响计划，指定独立 codex review，最终候选及基线必要 CI；
  建立带 tend 来源标签的受管 PR，按保护规则 rebase & merge。
- 本地转换和测试替身不证明生产安装；本次不启动实际升级，不接管旧账本操作。

## 独立审阅修正

首轮 reviewer 用旧基线的 server 接续代码复现：协议兼容的旧候选会忽略每 host 的
交接来源，并在 server 提交后恢复 normal。采纳该缺陷；在原 capability 探测边界读取
绑定实际 server SHA 的 `maintenance-upgrade-handoff-v1` 能力，维护交接必须有该证明。
普通 normal 起点保留无此能力的旧候选路径。拒绝必须在创建批次和改 host 控制之前，
并补本机 Controller 透传、缺失/错误证明拒绝、原维护未改动及真实能力输出回归。
首轮本地 make check 的已通过阶段保留；因候选需修正主动中止剩余阶段，不记为通过。
