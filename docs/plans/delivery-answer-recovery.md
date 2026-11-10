# 交付答复准入与原候选恢复

## 问题与边界

具体交付问题通过通用答复入口接受 `option` 和 `text`，但授权读取明确拒绝任何带自由文本的答复。原答复已关闭后，`prepare` 又无条件复用同候选的 answered/version2 问题，造成已接受却无法绑定且无法正式恢复的状态。不能把说明或条件文本自动改写成无条件批准，也不能修改历史答案。

## 最小实现（D214）

1. 仅对实际交付责任关联的决策，在原已答相同答复的幂等回读之后，拒绝新的 `approve`/`end_delivery`+`text` 组合。批准或结束的审计解释放 `Envelope.reason`；自由或附条件答复使用纯 `text`，其本身不授权执行。`defer` 可继续带备注，原子进入持久延期，既不批准执行也不结束责任。通用问题不变。
2. 同候选 `prepare` 只复用 open/version1，或原来源仍有效且可读取为 `approve`、`defer`、`end_delivery` 的 answered/version2。其他历史答复允许显式准备同候选的新问题，旧答案、原消息和版本保持原样，不自动制造批准。
3. 复用原候选、来源、epoch、执行/租约/工作区及升级 unknown 检查。延期与已结束责任不重开；已有有效选项不重复提问。无新字段、迁移、CLI 操作或 server/runner 通信变化。
4. 回归覆盖入口原子拒绝与解释指引、通用问题兼容、历史真实来源的混合答复恢复及幂等回读、纯文本非批准、有效选项幂等、来源失效与未结清执行围栏。仅执行受影响的定向 regular/race/vet，由协调者负责最终完整门槛。

## 验证与历史

保存修复前真实失败及定向 RED，修复后核验相同原候选能经正式 `prepare`、纯选项答复继续。任何生产批准、恢复、合入、部署均不属于本计划的源码实现动作。

## 独立审阅修正

1. 原具体问题准备后，Issue policy 可能变为 auto；其后纯文本或历史混合答复不能在恢复时转成自动执行授权。同候选的不可用已答问题恢复必须保持 manual 具体确认，即使当前 policy 为 auto 或已清除。
2. live duty 已存在时，加载不会再校验原 Task。实际替换不可用问题前，复用原 live Task 身份、control revision 和 lifecycle 校验；原已验收/已有产物的历史来源不套 live 前提。不变更纯幂等回读。
3. 先用真实 manual→auto→条件答复及 pause/cancel/stale-control trace 记录 RED，再定向 regular/race/vet；旧完整检查、审阅与首次 focused 收据保留，新修正证据独立存放。

## 延期合同兼容收口

最终完整检查发现原延期回归明确支持 `defer`+说明，并要求 provider/baseline/检查漂移不重开，只有真实 owner resume 来源可恢复。收窄混合答复拒绝到批准与结束选项，原延期回归保持原样；新三入口矩阵同时证明延期说明被接受且不成为批准。最终定向验证必须包含 `TestDeliveryDeferSurvivesPrepareProviderAndBaselineDrift` 与 `TestDeliveryDeferredManagerNeedsCurrentOwnerResumeSource`。
