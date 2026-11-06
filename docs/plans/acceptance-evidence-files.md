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

## 实施分工与验证

先固定协议字段与接口，再并行 runner/harness 和 server/store/读取链路，禁止多个 writer 同文件。原验收文件路径、上传失败及丢失回执保留在 ignored 调查证据，不提交本机信息。

真实回归必须覆盖：accept 角色上传并提交 blocked/changes_required/passed 原文件；删除 scratch 后 owner、主 Agent 与获授权的后续 worker 仍读到原字节；原请求丢回执和重开沿原 ID 对账、字节变化不替换已选原件；部分上传/落盘/事务失败不确认 verdict、无未关联可读授权；跨 run/task/Issue 或伪 manifest 拒绝；旧能力缺失、旧 verdict、原实现文件与旧协议行为不变。CLI 用真实有效清单验证 JSON、元数据、下载和请求恢复。必要 focused 验证完成后，由协调者统一冻结、完整检查、独立审阅、CI、合入及受控部署，再让 Tend 沿原责任推进。
