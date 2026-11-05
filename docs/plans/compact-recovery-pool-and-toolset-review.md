# Compact 恢复的局部隔离与工具集接续

D150 原 runtime/thread 修正后，独立审查确认两项边界需要补齐：一个历史 compact 缺少可信 runtime 的局部 unknown 不应中止整个后台 conversation pool 扫描；只读原 compact thread 恢复也不能在结算后直接接受新业务，绕过升级后必要的 toolset handoff。

## R1：局部阻塞不影响其它 conversation

启动资格门把业务层 CodeUnknownOutcome 视为该 conversation 暂不可用，和已有的无效状态/权限阻塞一样继续扫描其它项；保留直接恢复选择 API 的 unknown，数据库错误仍原样返回。不把缺证转换成成功或猜当前 runtime。以一个缺证 compact 与另一个正常 manager 并存复现扫描隔离。

## R2：对账后重入正常启动

恢复 attachment 期间保留原 thread 只读事件流，禁止新业务和工具回调准入。原 compact 结算并持久确认后，退出该恢复 session，重新走正常启动、原 binding/toolset 核验和必要 handoff，才发布可做新业务的 ready。原完整事件须先 ack；已经持久结算但没有新事件也须能在周期检查退出恢复，不能无限挂着。无 native 重发或替代 compact。

测试先以 fake session 的原 thread、旧 toolset 和原完成事件复现，再核其恢复 session 不能启动新业务、完成对账后正常启动确实重新核验并 handoff。保持既有 native-turn 优先及 unknown/请求身份边界；有意义目标 race GREEN 后先规定独立 review 收口到 CLEAR，随后最终本地正常 make check 与 exact CI 并行。此前 d18 full 的部分阶段与取消、CI 和 review 均保留，不当作最终通过。
