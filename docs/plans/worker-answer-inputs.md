# 原问题答复的特定附件权限

Refs: D171。原 HB7/EHBN 的 c-input-access：后补的 owner 问答附件虽然合法，仍不属于 Run 原 spec.inputs，因此只能外部复制或内联。D164 的跨 Issue artifact 共享不代替这个原问题边界。

## 最小合同

采用独立的新 grant/revoke 动作，不改已接受 answerJSON，不让同 ID 换 payload，不自动共享消息的全部附件。

- 对象仅真实 worker question。grant 要求 question version、原 Run/lease、独立 input revision CAS、明确 file_ids，以及原 owner 答复或 ReplyTo 该问题通知的 owner 补充消息与 content revision。附件必须确实属于该消息；冻结原 file_id/name/size/SHA，不复制内容或开放路径。
- grant 只在原冻结 Task/spec/action/代次/角色和责任仍有效时创建；原 lease 必须仍可认证原执行；已停止前驱和后继的附件继承本批不隐式授权。普通 Task 问题、取消/已替代问题或无法证明的历史来源拒绝，不猜适用性。
- owner 可显式操作；主 Agent 必须在实际 admitted Main 上下文传入当前 owner 授权来源。负责 Agent/worker 不能给自己授权。审批 provenance 不作为一般消息/其它附件的读取权限。
- grant/revoke 与原 request record、稳定输入 revision 事件同事务追加，使用现有 events(object_type,object_id,event_seq) 索引按单问题读，不新增迁移或扫描全库 JSON。撤销阻止该 grant 的后续 fetch，不改已接纳答复/原请求结果或已经冻结的其它独立输入权限，也不声称收回已下载字节。
- 读取只认原 worker question 的确切 Run/lease/role/Task/spec/action；本批仅授权原 Run，不把现有 QuestionContinuationForRun 的投影当授权证明；后继需要独立可证授权，本批明确拒绝。不能倒推给同 Task 的新 spec/其它 Run、兄弟 Issue 或验收角色。
- typed owner API、CLI grant/show/revoke、主 Agent 工具与原 worker question 的附件 metadata 同批实现。普通 worker 仍用现有按原 Run 凭据的文件入口，不借 owner 凭据。新动作是 opt-in；旧 answer 请求/默认行为/规范 payload_hash 和旧 runner wire 不变。没有 grant 的 legacy 仍按旧权限拒绝。

## 验证

先用原完整问答/真实 blob fixture 重现答复附件被拒，然后验证：exact file/hash 可读、同消息其它附件拒绝；跨 Run/role/Task/spec/lease、未准入/撤回 owner 来源、伪 source/hash/版本拒绝；grant/revoke unknown 同 ID 回放；accepted answer 和原 spec/pin/租约不改；continuation 和新代次不会隐式继承；重开 DB 后授权事实不变；撤销后 future fetch 拒绝、已有 spec.inputs 独立权限不变。CLI/HTTP/Main 工具与原 runner 文件接口做真实边界回归。

本批不自动合并语义相似 Issue，不增加任意附件共享、目录访问或通用 workflow，也不在停机生产上验证。

实际可见性：原执行器可通过现有 worker question 读取入口取得新增 inputs 元数据与 input_revision；owner/Main 的 grant/show 返回同一 file_id/name/hash/scope。既有协议不保证主动 steer 自动注入，主协调可沿原问题 relay 通知执行方读取，不能声称自动重启或注入。真实 owner 答复/ReplyTo 补充本身就是精确附件授权意图，不另加泛确认。

runner 的既有 download_file 先保留 assignment 原 spec/产物路径；仅新附件不属于上述范围时，按原 Run 凭据读取既有 questions metadata，核 Run/action/spec/role 后校验 SHA 下载。每次新 fetch 重读，不把本地缓存当权限；server 最终读再次核 active grant。旧 server 无 inputs 字段时按原拒绝，没有新必填 wire 或新增期望状态。

## 最终原生来源依赖

本分支真实依赖 PR234 冻结父 55a8815987ed463a29a232fefe2f86979836c058，不复制 shared helper。grant/revoke 的 shared gate 保留父既有操作并集，用实际 owner reply 的 native delivery 允许写入；已分类加入当前 review list 的历史 reply 仍不构成原生准入。组合回归同时覆盖这两个控制入口、当前原生 grant/revoke 和原成功请求回放。此前独立审查与完整检查只证明各自原冻结源，不替代新 helper 组合验证。
