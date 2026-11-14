# 依据实际影响选择验证

Refs: D25、D130；承接测试时间语义修复。本计划在实现前记录，最终只保留本地独立候选，不合入或部署。

## 目标与边界

每次 PR 无条件同时执行本地全量、CI 全量、所有浏览器场景、跨平台和发布构建，会重复验证未变路径。选择必须基于实际 diff 和依赖/契约，不能把当前后端状态事务问题误称为纯前端，也不能只运行改动文件。正式验证仍保留 race、动态根发现、断言、失败/取消与原生逐例报告。

## 共享选择器

1. 新增仅标准库的 impact 库与命令：读取确定的 Git base/head、工作区差异与文件内容指纹，输出临时 JSON 计划及每个包含/排除理由。PR 比较实际 base 与 checkout HEAD；本地缺省取 origin/main 的 merge-base，显式 CHECK_BASE 可覆盖；无法解析基线时明确失败，不把空结果当作排除计划；未知文件类别或无法建立 Go 图时扩大为完整验证，不静默排除。
2. Go 实现改动使用 `go list -json ./...` 的 Imports、TestImports、XTestImports 建立反向闭包；测试文件改动至少执行所属包。新增包、测试和文件自动发现。跨组件契约通过反向测试导入和明确的核心边界规则保留；移除包、迁移、协议/生成、依赖及检查基础设施等广影响走全量。
3. 纯文档执行差异校验；纯 Web 执行原类型/lint/单元/build 和所有 layout 组，不拖入后端 race/各架构发布构建。普通 Go 变更执行受影响包静态检查与原完整包级 race 根，保留受影响 API/mainthread/E2E/Sandbox；不凭目录排除跨层导入。main/release 和显式 full 始终完整兜底。
4. 本地 make/check 与 Actions 影子诊断读取同一计划；Actions 正式执行仍完整覆盖。计划绑定 base/head/tree 与实际内容指纹；来源改变必须重新生成。只用临时工件与已有 Git/检查结果，不新增永久账本或审批步骤。
5. 本阶段 Actions 只输出、上传影子计划，原五组完整 race、Web/layout/cross/release 和 required check 保持；影子诊断不是业务卡口。动态矩阵、按计划明确排除与严格聚合另存待选 patch，用户决定后才启用。单元测试先验证未来方案不能把 necessary job 的 missing/skipped/failure/cancel 当作成功；本阶段不部署该选择。


## 有效验证

用真实历史 docs/UI/后端/迁移差异做选择对照；构造 leaf→生产依赖→跨包测试导入的漏选回归、测试文件单包、删包、未知文件和计划身份变化。用伪 Go/Make 可控执行器验证本地选择与 Actions job 状态聚合，不启动另一套 full 或新 CI。工作流做语法/数据契约静态验证，不能称为 GitHub 已执行。

同步修订本地无条件重复 full 的旧约定：普通本地验证采用影响计划，PR CI 暂时完整覆盖。计划本身和广泛影响以及 main/release 使用完整兜底。独立审阅评估最终候选与选择理由；最终 head/base 改变重新计算影响与已有证据适用性，不机械重复相同不变候选。

收尾依赖审计发现 `TestPublicCapabilityLocalControlCoverage` 会解析 runner、updater、upgradectl、maintenance 及 runner 入口源码，属于 Go imports 不可见的实际契约。对应生产源码变化必须包含既有 `capability-check`，不为这项源码扫描无条件追加全部 API 运行时测试；真实 API 依赖仍由导入图选择。入口程序生产装配与 release 产物身份改动保留完整兜底，入口测试文件仍按所属包选择。增加没有导入边也必须包含能力覆盖的漏选回归，不把这些真实依赖误报为无关。
