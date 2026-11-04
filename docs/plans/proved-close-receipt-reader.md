# 已证关闭后的原业务回执读取

基线：`bc2be98a8e1e735f2d506195f0ecddf14ce91408`。Refs: D173、D169、D155、D156。

## 问题与不变量

原 manager 的尾部原始事件已经真实消费，原 close 与 stopped 回执也已确认，但原业务执行仍有待对账 receipt。物理停止不是业务终态。旧 unknown Run 占位使现有 receipt-only 后继无法排队，旧失效 lease 也不能领取 native 请求。不能借暂停/恢复控制、续活旧 lease、替换 pin、伪造终态或删除 pending 解除该循环。

## 先验证，再收敛产品范围

1. 在独占离线冷备副本中重现上述已证物理事实与未决业务并存的场景。未保存完整原信封时，明确区分依据既有事实构造的场景与原请求逐字节重放；不构造原业务终态，不访问 provider 或生产服务。
2. 验证现有正式对账、调度、冻结身份选择和只读 receipt consumer 的全链，保留旧 pending、原请求与原始事件。评估更小的现有正式入口，不能为通过守卫拼接 pause/resume。
3. 若确需补入口，仅允许精确原 Run/lease/pin/assignment epoch、原成功 close 及独立停止证明所约束的 receipt-reader admission。优先复用现有 receipt-only Run 的正常租约与硬性业务写入禁令，不放宽旧失效租约或普通业务准入。旧默认 settle 行为不变。
4. 只读路径严格恢复原 thread，不能 fallback 到新 thread、迁移、摘要或模型选择。只有真实原 native receipt 才能通过既有对账链结清 pending；读取失败或缺少终态继续 unknown，并明确不可得边界。
5. 同时核验终态不可取得时的既有正式放弃/恢复语义。保留未知与已发生效果，不能借新机制无限推迟同一缺失事实，也不能冒充业务完成。若现有语义不足，先报告最小责任处置合同，不扩成第二套恢复框架。

## 实际执行入口

读取必须经过真实 runner 的冻结 snapshot resolver 与 coordination driver。receipt-only 解析仍校验原 snapshot、hash、环境与 agent/runtime revision，但不执行普通业务 readiness probe；该 probe 会查询模型并创建临时 thread。仅 receipt-only resume 保留原 AppliedModel、provider、effort，不重新查询当前模型目录或改写冻结配置。普通业务解析、权限与模型选择保持原规则。reader 的原 thread 恢复、find、usage 与自身 close 之外的 native 业务请求必须拒绝。

新模式显式协商 `runner-manager-receipt-reader-v1`：admission、该模式新 action 的 claim 与该 Run 的恢复均拒绝旧执行器。新增可选能力不改变历史 receipt-only 或普通业务请求，也不升级 wire 代际。

独立 review 的两项修正：原 closing Run 的 metadata-only attachment 可以恢复该 Run 已确认的缓存启动元数据，缺原模型时不能提前切断其事件消费；这不是读取当前模型目录或打开 native。新模式的原业务 pending 尚未对清时，后续 receipt-only 代次继续携带原 reader 请求与原待对账身份，不能因一次读取失败或非终态而丢失能力要求；原 pending 全部对清后，新的无关责任不继承该限制。

对上述关闭窗口继续核验：新 reader 没有 models RPC，因此优先读取同一 Run、精确原 thread 的已确认 resume 内实际模型配置；只有历史 resume 没有该配置时，才使用该 Run 已有的缓存模型目录作兼容恢复。原 compaction 的 Reconcile 仅读账本既存完成证据，应继续允许；创建 compaction、handoff 或工具业务的禁令不变。候选修正先做必要定向和限定独立核查、受控二进制原故障验证；目标验证通过后再并行正式完整检查与 CI/review，避免对尚未实证的候选反复全量验证。

## 回归与交付

先保留真实入口阻塞的 RED 与离线证据，再实现所需最小修改。覆盖错误原身份/证明、未确认停止、未知业务保留、禁止新业务、原 thread/pin、读取失败与真实终态对账。新增用户能力必须同步 typed CLI、帮助、能力目录、请求恢复与 schema；内部 wire 不发生变化时不凭空提升协议。

稳定候选的定向回归、规定独立 Sol/xhigh review、CI 和唯一完整本地检查按资源允许并行。全部门通过后 fresh main / rebase 线性合入，交唯一 operator 按实际合入版本受控发布并验收原业务事实；没有真实终态或正式处置时不报告整体恢复。
