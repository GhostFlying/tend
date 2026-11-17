# Server 提交后的目标 runner 重连窗口

## 目标与真实调用链

业务起点是正式升级接纳完整发布及固定目标，终点是 server 持久提交后，同一批次的
目标 runner 完成重连、控制确认和原有滚动安装。静态风险尚不等于生产故障。

`StartUpgrade` → `prepareUpgradeHosts` 发 PrepareOnly 控制；真实 RuntimeHeartbeat
确认 prepared → `TickUpgrade/advanceUpgradePreparation` 进入 server_draining；
主会话排空后给目标下发 maintenance 与固定授权 → runner 确认 →
`ServerUpgradeAuthorize` 固定 server 候选/期限 → 新 server 的真实就绪观察与绑定
原候选的 updater committed receipt → `reconcileServerCandidate` 恢复控制并进入
runners → `advanceRunnerUpgrade` 等 ACK，再排空/授权 runner。

当前 queued 分支先按 Online/60 秒心跳判断离线，再检查控制 ACK。server 停机超过
心跳窗口时，新控制尚未被 runner 读到就可能进入 attention。先通过上述转换复现，
不直接把 fixture 的 UpgradeRow 设置成 runners/completed。

## 实现边界

1. 先新增真实转换回归并在未修复源码上保存失败原件。
2. 仅在 server 已持久提交、queued 原目标仍持有本批次替换前维护观察、尚未确认
   server 恢复后的控制时等待重连。不得把等待当在线、就绪或安装授权；不改变目标、
   批次、阶段/整体期限，不恢复全 fleet 在线门槛。
3. 控制和 batch 身份、旧程序哈希、现有候选/安装授权不符合预期时沿既有检查，
   新 ACK 后真正离线仍暂停。下载/安装失败、unknown 与候选身份匹配规则保留。
4. 公开主机 phase 表达等待重连；真正阶段进展沿既有规则更新。等待本身不续期。
   设计同步 docs/design.md，操作文档给统一部署说明旧版本的正式恢复路径。

## 验证与故障恢复

Fixture 只提供注册主机/旧程序、候选清单、可控时钟和外部 updater 观察。准备、
维护、固定 server 授权、持久提交消费、控制恢复、normal/maintenance ACK、runner
排空及安装授权均走真实 App 方法。分别覆盖刚好 60 秒、显式离线、重连成功、重复
检查不续期、阶段/整体到期、无 server 替换及已确认恢复后离线、错误批次/程序观察。

运行针对升级主链的新鲜回归及 make check 选择的实际影响验证；独立运行仓库规定
codex review，保留最终候选/基线及 CI 结果。发布前不改生产状态。

故障时仍可读取原 upgrade status/steps、host/runtime、原 updater receipt 与期限。
旧版本若已提前 attention，先核对原 server committed、同一目标重连与 normal ACK、
未知候选/取消回执；只使用正式 retry/cancel 和当前 revision。retry 不续原期限，
已授权候选先对账；过期后仅对账/合法取消，不改库、不 force、不缩目标、不改期限。
统一部署 owner 负责实际安装与生产业务验证，本 Issue 交付 merge 和验证路径。
