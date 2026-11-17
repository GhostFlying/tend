# 正式续审原件与 owner 持续执行

## 原业务目标与当前边界

在同一 Issue、原 Task 上让独立 reviewer 读取正式实现成果、验证附件和续审说明，保留实际产出 Run、冻结身份/profile、候选 head/base 与 requirements digest。已合入的 category execution-pin binder（PR341）先核实而不重复实现；合入、运行版本和部署业务验收分别取证。本责任只准备修复、验证、独立仓库审阅及保护规则下的 rebase 合入，部署和配置由其原责任负责。

## 调用链与调查顺序

1. 核对当前 main、PR341 的 `ReviewIssueDelivery` → 实际 Run/execution pin 或完成 artifact → start/record；复用已有 category、独立性、同 generation 续行回归，保留历史 stale digest 和 terminal 恢复事实。
2. 正式文件 producer 为 `UploadDeliverableFile`，确认上传只保存不可变字节和原 Task/Run，不自动把私有工作文件变成成果。正式提交走 `SubmitArtifact` 的 files，普通正式补充走现有 conversation 附件发布/关联；追踪 `materialScopeSQL` → `IssueMaterialFile` → `ReadRunContext`/`OpenRunContextFile` → reviewer 同 Task 续行。历史仅在文本中提及而未进入成果引用的续审文件曾返回 403/404；用真实 producer、提交/关联和读取复现，先判断现行合法路径，再决定必要最小修复，不以文件名或正文中的 file ID 推导新权限。
3. 第4项只调整 `NamedIssueOwnerInstructions`：实际冲突先解决并固定当前 head/base，按实质 delta 评估旧证据和必要测试/审阅；不打断有用验证，不机械全测，不增加控制系统或批准层。
4. 第5项核对真实事件 producer → coalescing → owner 唤醒/通知，只对已证实同链缺口做最小修改。保留失败、unknown、可行动阻塞和必要终态。外部 observer 的验证/去重/事件续行原件由既有维护责任交回；缺件明确未结，不复制 observer、不新增定时器或模型轮询。

## 故障时仍可执行的动作与恢复

文件读取失败时保留精确 file ID、来源、403/404 和当前候选；其他实现、自测、报告和已有可读原件仍可继续。由原 owner 通过现有正式成果/附件关联原件，或在原问题正式答复完整原文，reviewer 再沿原 Task 恢复。不得自动公开私有 trace/rollout，或借无关 Issue 文件绕过拒读。上传成功不等于正式提交、审阅通过或当前候选检查通过。

实际冲突/失败/unknown 与本地验证状态分别可见；未知外部变更沿原身份对账。外部等待保存可观察条件与恢复位置；同目标使用原 Task 追加 Run。外部 observer 原件未到时保留具体提供者和待交付内容，不能以产品 PR 代替完成。

## 回归与交付

待复现的最小修复只针对原 owner 的已确认上传：当前 Issue 的结构化背景 `source` 明确等于该 file ID、上传来自本 Issue 的真实 owner Run 时，按已有背景原件引用解析元数据与字节。仅正文提及 ID、实施者未提交的 trace/rollout、其他 Issue 的私有上传均不因此获得读取权；正式实现附件仍走 artifact/files 或 conversation 附件。若真实 producer 回归不能证实此缺口，则不扩展该读取路径。

真实 producer 回归在原代码返回 `uploaded file ...: store: not found`，同 Issue 结构化引用存在但原件解析缺失；修复沿原背景来源与上传归属解析，不新增权限表。第5项已复现另一窄缺口：同作者完全相同的 progress 重复提交会推进 revision 并产生外部变更通知。最小修复在原权限/revision/epoch 校验之后比较既有进度，原样返回相同事实；内容或作者变化继续原通知链，不按失败或 unknown 关键词过滤。

从正常上传、正式提交/附件关联、独立 reviewer 领取、上下文元数据和原字节读取，到正常停止/同 Task 续行报告验证；私有未提交文件及无关 Issue 私有文件继续拒读。fixture 仅提供资源/外部 provider 观察，不预置要验证的提交与读取转换。按实际 diff 执行 `make check`，最终候选按仓库要求执行独立 Codex review 和必要 CI，再受保护 rebase 合入。交接提供精确 merge、合法登记入口、部署后原业务动作、预期和原件；本地、CI、合入与部署/业务验收状态分别报告。
