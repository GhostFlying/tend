# 已验证合入后的原执行收尾

Refs: D185、D187。

已用真实共享审阅及 server 合并执行链复现：原授权 head/base 已合法合入，后续 target tip 包含该 merge，但三个入口仍强求 target 等于 merge，导致 duty 失效、worker 授权失效及已完成执行无法收尾；merge-only 又不允许重新准备 merged deployment-only 候选。

1. 仅在原已绑定、仍 executing 的 merge-only 执行中，认可 server 观察提供的匹配 head/base/merge、非空 target、`TargetContainsMerge=true` 且 lineage 无错误的事实。目标后续前进不改变原 duty revision、授权或执行绑定；完整检查与共享审阅仍绑定原候选。
2. 对同一原已知合入身份的临时 provider/lineage unknown，保留原 duty/grant/execution，但禁止 worker 有效性与 complete。后续有界 server 读取恢复真实祖先证明后，以原已完成 Task 收尾；不重新发起 merge，不把未知记为成功。
3. 同步修正候选失效、worker 授权视图和 complete 使用的 source 检查。部署继续校验冻结的精确来源；不为历史 runner 事实扩大默认权限，不新增合入、部署或补审授权。
4. worker 的受限 provider 视图追加可选 target 祖先证明及 lineage 错误字段，保留原来源，不从相等 SHA 猜测证明。
5. 回归使用既有真实共享 review/交付 Task/run/server Claim/Record 链，验证 target 前进后原执行完成及 duty/grant/binding 不变；错误 head/base、祖先未确认、lineage unknown 及部署来源变化仍拒绝。验证旧 runner 原有 target==merge 行为。

本次只作上述死路的最小修复，聚焦验证后交统一最终候选检查；不做生产写入或独立完整检查。

## 正式 review 的有界观察恢复补充

已复现：provider 已确认 merged，但 lineage 查询 timeout/429 时，终态筛选会在下一次到期时丢弃成员，永久停止自动恢复。将该部分读取失败纳入既有有限重试：保留已知 merged/head/base/target/merge、原查询回执及 lineage 错误，同时提供可见错误和重试时间；仅仍有重试预算的 merged 失败可再次领取。完整证明成功后停止终态轮询，耗尽后结束 pending，仍可沿显式刷新恢复。成功读取重置共享 job 的连续失败预算，不让之前成功轮询消耗新的失败预算。回归先复现缺口，再验证跨关联 Issue 的恢复、超时/429 耗尽、原回执重放及显式恢复。
