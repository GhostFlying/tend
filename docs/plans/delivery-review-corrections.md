# D152 独立验收修正计划

本轮沿原机制任务、PR 和历史提交修正 P1/P2，不替换原失败产物，不修改 UI 或 admission guard。

1. P1：把 PR base、目标分支当前 tip 与 merge 是否包含于该 tip 分开观测；GitHub 使用 baseRef.target.oid 和按不可变 SHA 的 compare 证明 lineage。候选、版本化问题、任务领取、start 和完成核对使用同一目标身份；开放 PR 合入时仍核对已批准的 PR base，不能因状态切换跳过 base 漂移；合入后只允许目标恰为本次 merge，merged 候选可确认包含原 merge 的当前 tip。旧 runner 的缺失事实保持未知，新增事实经独立能力握手发送。
2. P2：延期是持久责任状态。prepare 不得清除延期或创建问题，provider/baseline 失效仍保留 deferred；resume 必须是 owner 的显式请求或当前 admitted owner 消息，不能复用原 defer 消息或更早的 owner 来源。
3. 先补实际 provider 形状和 drift/defer 回归，覆盖真实答复、任务创建/领取、StartUpgrade、合法来源恢复及历史/幂等边界；再执行 make check 和平台构建。以当前远端 main 核实兼容性，不用旧 head 检查替代新候选检查。
4. 保留原 PR，提交修正并推送新 head，记录独立 review/CI 的实际状态。正式独立重验由原 Tend 责任链调度，本运行不伪造 verdict，不 merge/deploy。

## 第二次独立验收后的原任务修正

起点为 PR 原候选 `4c11abf0`，开始时工作树干净，沿既有分支保留提交与失败记录。远端 main `90f873c1` 新增独立 UI 修复；rebase 仅整合设计文档冲突，同时保留 D146 卡片说明与 D152 交付机制，不修改 UI 代码。

1. 补实际 prepare→versioned owner answer→create_task→最终 spec 的回归；使用真实 merged PR 的历史 base、merge 与当前 target 三种独立身份。合同须要求 merge 为 target 的祖先、构建 target，禁止构建历史 base，open 合入后的来源规则保持原守卫。
2. defer 选项有无解释文字均写入持久延期；将真实 attentions.respond 带文字答复纳入 validation/prepare、provider、baseline、查询失败及合法 sourced resume 回归，保留批准带条件文字不授权的原边界。
3. 先运行新增失败回归，再修最小代码及 D152 文档；对整合后的候选运行针对性 race、完整 make check、平台构建及指定独立 review，更新原 PR 并核实精确 head 的最终 CI。独立正式重验由 Tend 原责任链执行，不在本运行自行提交 verdict。
4. 当前仅交付新候选，不合入、不部署、不联系其他人；原实现及 changes_required 产物和历史不变。
