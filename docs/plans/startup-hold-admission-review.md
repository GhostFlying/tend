# 启动预算与投递准入的审查修正

依据：D150 的有界启动技术责任、D169 的原生回执安全边界。本计划只修正启动升级批次独立审查发现的两个可达缺口；manager 失租责任、历史运行补责任、生产配置和测试超时均不在本计划范围内。

## R1：明确未发送的拒绝不授予对账豁免

普通输入的真实 `turn/start` 明确拒绝后，原 request 会保存为 `rejected` 并回到 pending；没有 pending native receipt 时仍属于未发送输入。先保存此路径的 RED，覆盖首次30秒退避、耗尽后唯一 Attention 与停止自动 Open。只收窄 serviceable-head fallback 对该 request 的分类；显式 pending receipt 继续独立允许只读 attachment，具名 sending/rejected 的冻结恢复分支保持原合同。

## R2：每个原输入在实际准入前重新检查 hold

先复现低优先级 repair 耗尽预算后，被较新的前台输入打开的会话继续领取并启动。不能只检查打开会话时选择的 request。领取原输入的事务内检查其启动状态，open Attention 时不改变原 pending/attempts；在新增 native starting 的事务中再次检查，防止先领取、后建立 Attention 的竞态。已有 native intent 与待对账回执的读取、结算不受另一输入的 hold 影响。问题明确结清后以同一 request 继续，不创建替代业务输入。

## 验证与交付

1. 先落盘本计划，唯一实施者随后补最小 app/mainthread RED；测试不调用模型或生产 API。
2. 在现有完整检查及原候选/基线精简对照自然收口、缓存所有权释放后，使用同一低影响预算运行有限 RED/GREEN，不运行第二次完整检查。
3. 同时覆盖 rejected 加显式 pending receipt、较新输入不被旧输入预算阻断、hold 阻止原 pending 领取及已领取输入的 native starting、结清后原身份继续，以及既有未知回执保留。
4. 九项 named 等待截止失败与这两项产品缺陷分别保存证据。不得靠修改 timeout、将未知结论归给资源或局部 GREEN 来消除原完整检查失败。
5. 修正完成后更新 D150，保留独立原子提交；最终候选的规定 review、CI 与一次必要完整检查另按交付门禁完成。重跑完整检查前先取得既有预算小配对结果，避免盲目重复长检查。

此修正不新增 API、CLI、迁移、配置字段或 runner wire。准入门只约束本批新建的启动故障状态，不改变历史请求结构或现有 native receipt 的有效性；runner wire 仍为2/1。
