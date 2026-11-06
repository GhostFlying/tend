# 验收证据文件持久化（D181）

## 问题与边界

验收结果当前只保存 verdict、任意 evidence JSON 和 issues；其中的本机路径不是文件引用。验收 scratch 在原终态提交得到确认后删除，后续诊断或返修无法再取得原日志。已有实现交付文件使用 D87 不可变内容目录，但验收角色没有上传能力，后续反馈与文件权限也不认识验收文件。

本次只补齐显式选择的验收证据文件链路，不解析 evidence 中的路径，不自动收集任意文件，不长期保留整个 scratch，不改写旧 verdict、失败请求或 unknown，也不声称能恢复已经删除的原件。验收仍只判断交付，文件上传不授予实现、委派或任务控制权限。

## 具体协议与持久化

1. 增加可选 `VerdictPayload.files`、`Feedback.files`、`AcceptanceView.files`，均为现有 `ArtifactFile` 不可变清单；原 evidence JSON 原样保存。增加可选 assignment 开关及 runner capability `acceptance-evidence-files-v1`。新 server 仅对声明该能力的新 runner 开启；未开启时保留原工具与请求，旧缺 files 的请求继续有效且不代表已有文件持久化。
2. `submit_verdict` 在已协商能力下接受可选 `files: [{path, name}]`。调用方显式选取普通文件；验收证据复制为该 run/原 native binding 下的不可变本机上传源，原子持久化清单和稳定上传身份，再复用流式上传和 D87 blob。上传或本机落盘未确认时不确认 verdict；重开只对账原上传，不能因原路径变化给旧请求换字节。
3. 复用 `/runner/v1/files`、`runs.upload_file` 与 `deliverable_files` 原文件存储；仅对真实当前 run、匹配角色与已协商验收能力开放验收上传。原实现上传语义不变，不开放 owner 代冒 worker 上传。上传回执包含原 file ID、名称、size 和 hash。
4. 追加 migration `0095_acceptance_evidence_files.sql`，为 `acceptances` 增加 `files_json TEXT NOT NULL DEFAULT '[]'`。`acceptances.submit` 在原事务内校验每个 manifest 属于该验收 run/task，身份、名称、size、hash 精确一致并唯一，再同时保存 verdict、文件关联、事件和领域状态。没有最终关联的上传不成为任务输入；D87 当前不自动 GC 已发布原件，本次不增加清理器。
   同一迁移新增 `run_acceptance_evidence_contracts`，在首次 Claim 事务冻结 `files_allowed` 与 `feedback_acceptance_id`；旧 run 无记录不补授权。重开或后续 Hello 的能力变化不修改原合同，后续验收不能改变已领取返修 run 的文件来源。
5. finalized 文件解析同时支持提交的 artifact 与提交的 acceptance。读取 ACL 保持 owner、原生产 run、当前任务的精确验收/返修反馈及当前 Issue 的已有责任范围；同名、同 hash、同 workspace 不产生授权。跨任务选作新输入时按已有显式 inputs 机制冻结 `source_task_id/source_acceptance_id`，不假装是 artifact 文件、不因普通 follow-up 自动继承。

## 可见性与兼容

- 下一代同 spec 的返修 assignment 带原 acceptance ID 与 `Feedback.files`；`download_file` 仅下载声明的清单并验证完整 size/hash。主 Agent 的 task/artifact acceptance 视图展示文件清单，`files_read` 和 owner 的现有 file metadata/download 能读取已最终关联的原件。CLI 的 task/artifact JSON、文件读取与下载覆盖相同能力并补帮助与入口回归。
- 线上协议仍支持 N/N−1（当前 2/1）。这是协商后的可选能力，不新增旧请求必填字段、不拒绝旧有效 verdict、不改变旧默认判定。旧 server 严格拒绝未知 payload 字段，因此新 runner 在没有 assignment 开关时不得发送 files 或提供上传承诺；新 server 对旧 runner 保留原路径。旧 journal 已保存的 VerdictPayload 缺 files 按原事实恢复，不重做验收。
- 生成 schema/type 由仓库 generator 产生，不手改。server schema 从当前 94 追加至 95；不改旧 migration，验证真实 94→95 以及发布源最低 schema 的完整迁移范围。runner journal 追加至 18 作为持久结果格式护栏：旧 17 writer 必须在读取/改写新 verdict 前拒绝未来版本，防止旧 `Verdicts.Restore` 静默丢弃 files 后提交并清理。新 runner 验证 15/16/17→18 保留原请求和 held/fenced/unknown；wire 协议仍为 2/1，发布存储范围据真实迁移结果声明。

## 上传未知与会话关闭的独立恢复

原上传尚未确认不等于原 app-server 会话仍在运行，两者分别保存事实。直接本机运行只在实际拥有的进程 Session.Close 成功后记录关闭，流连接断开、引用释放、关闭失败或推测均不产生关闭记录。该记录的物理范围是既有根进程 Wait 完成和进程组终止调用，不声称进程组已经核空，也不结清未知外部业务效果。

关闭记录绑定 run、原 native binding 和完整封存输入身份，包括 verdict/evidence/issues、文件名称/size/hash 与原上传 request ID。关闭时尚未知的 file ID 不伪造；重开后仅对账同一组原请求，验证真实清单，并将最终 verdict 字节绑定到原关闭记录。缓存恢复不能自行生成这份组合证明；输入、绑定或最终字节不符时继续待对账。未完整封存、没有关闭记录或实际关闭失败时，不通过新建 thread/turn、重放工具或伪造 StoppedConfirmed 推进。

## 独立审阅后的恢复入口修正

首轮完整独立审阅确认两个可触达缺陷，原审阅及 RED 证据保留，不能因为完整检查通过而忽略。

- runner 的普通启动围栏先于新增 harness 恢复分支执行，导致上传未知且原会话已关闭的真实 run 无法进入对账。修正提供只处理原封存结果的窄恢复入口，在普通新会话启动前验证原关闭记录、native binding、完整封存输入与请求身份；没有这些证明仍走原围栏。默认本机 app-server 工作区使用独立的验收结果只读重新附着，复用完整 state/provider/mount/path 校验，不扩大原 strict Orca 重新附着条件，不回退普通 Prepare。恢复不能新增 native attempt、thread、turn 或执行工具，也不能把上传确认当成停止证明。
- 明确的非法文件名或缺失路径在没有封存 intent、没有上传请求或外部副作用时也被永久保留为 pending。修正区分确定的选择拒绝与已经开始的封存/上传：前者允许后续原生工具调用纠正，并保留拒绝事实；只要已有冻结 intent、已尝试上传或不能证明未发生，就继续沿原身份对账。不能用通用错误或缓存缺失推断零副作用。

文件选择修正复用原审阅探针验证 RED/GREEN；原启动探针依赖第二次 Start，其原字节与 RED 保留，修正通过真实 owned 进程和公开 runner 入口验证零新增 Start/attempt。另覆盖零 intent 的文件纠正、关闭未知、部分上传、封存后改字节，以及结果先保存后中断再重开的恢复。

## 第二轮独立审阅的边界修正

第二轮完整审阅的原候选、原生结论和四个实际 RED 均单独保留，修正前先明确以下合同：

- 已有停止证明、尚未提交 verdict 的普通验收可能尚无 state 目录。只读检查应能明确返回没有原结果候选，再交回既有启动围栏；不能因为专用恢复入口要求候选目录而额外永久拦截正常恢复。有原 pending、intent 或关闭证明的任务仍按原封存身份处理。
- 目录等确定非普通文件的选择，在真实尚无 intent、上传请求或外部副作用时，和不存在路径一样可纠正，并保存拒绝事实。既有 intent、部分封存、已尝试上传和通用 I/O 未知不重新分类。
- 显式选择 FIFO 等特殊文件不能使打开文件先于校验而无限阻塞。选择、读取与封存前均检查文件类型及取消；打开后再次检查原对象，避免引入无归属阻塞 goroutine 或改变已封存字节。
- 上传取消不等于先前封存失败。上传使用原调用 context；完整封存身份的本机持久化必须先于外部效果，或用独立有界只读对账取得真实原封存证明。已真实关闭的 owned session 仍能记录该原证明，不能因为上传 context 已取消而永远丢失关闭范围，也不能从缓存补造关闭事实。

验证直接复用四个封存探针及原失败，补普通已停止、无 verdict 的默认本机工作区恢复、非普通文件可纠正、FIFO 取消退出，以及上传取消后的真实 owned Close 和原身份恢复。冻结后重新完成受影响验证、完整检查、全量独立审阅与 CI。 同轮 CI 的原生回执测试另有一次 5 秒总预算耗尽；该预算同时包含 journal 初始化与四轮回执。冻结原源的分阶段单例仍通过，未建立本次功能改动的因果关系，因此仅补初始化、原请求身份、回执数量及 elapsed 的失败观测，保留 5 秒总预算、1 秒请求期限与原权限断言，不以重跑或增加时间宣称修复未知原因。

## 实施分工与验证

先固定协议字段与接口，再并行 runner/harness 和 server/store/读取链路，禁止多个 writer 同文件。原验收文件路径、上传失败及丢失回执保留在 ignored 调查证据，不提交本机信息。

真实回归必须覆盖：accept 角色上传并提交 blocked/changes_required/passed 原文件；删除 scratch 后 owner、主 Agent 与获授权的后续 worker 仍读到原字节；原请求丢回执和重开沿原 ID 对账、字节变化不替换已选原件；部分上传/落盘/事务失败不确认 verdict、无未关联可读授权；跨 run/task/Issue 或伪 manifest 拒绝；旧能力缺失、旧 verdict、原实现文件与旧协议行为不变。CLI 用真实有效清单验证 JSON、元数据、下载和请求恢复。必要 focused 验证完成后，由协调者统一冻结、完整检查、独立审阅、CI、合入及受控部署，再让 Tend 沿原责任推进。
