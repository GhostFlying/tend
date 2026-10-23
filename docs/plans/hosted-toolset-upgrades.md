# 托管主会话工具升级与预备摘要

状态：实施中（2026-10-23）。Refs: D81（本次新增），细化 D68、D73。

## 已确认的取舍

用户接受 schema 变化时使用 fresh native thread，Web 历史及原 native 历史保留。交接采用代码事实包加模型摘要，包含前序 thread ID 和 rollout 路径，必要时回查；不要求将全部模型上下文复制到新 thread。暂时沿用 dynamic tools，不修改 harness 私有数据库或 rollout，不以 MCP 迁移作为前提。

## 目标行为

- 默认主 agent 连续 50 分钟无活动时，在隔离的临时 fork 中生成预备摘要。总结请求和回复不得追加到原 thread 或 Web 对话。
- 服务端 main_agent.summary_idle_minutes 提供默认值（默认 50，0 关闭）；Web owner 设置持久化覆盖，独立修订号、请求去重、跨设备共享，不依赖模型目录可用。
- 摘要存于 DB，记录来源 thread、对话水位/版本、最后完成 turn、格式版本和生成时间。只在覆盖最新内容时直接复用，生成过程中出现新内容则保留为过期摘要。
- 对话原地更新（流式回复、工具结果）也须改变覆盖版本；只比较最大 message seq 不足够。
- 无活动 turn、无待投递用户消息时才获取 fork 快照。总结开始后不阻塞正常用户消息；主会话继续活动时摘要不错误标记为最新。
- 自动总结按来源版本去重并有失败退避，不因 idle ticker 重复请求模型；所有等待接受 context 并负责退出。
- 总结 fork 拒绝所有 Tend 工具调用，在进入参数解析/业务服务前拦截；使用仅总结的开发指令，禁止副作用。超时或失败保留旧摘要，不影响正常聊天。
- schema 指纹覆盖工具名、说明和 schema。启动发现指纹变化（含未知的旧版绑定）后，在投递新用户 turn 前交接；有最新摘要直接用，否则受限生成，失败使用明确标注缺失的事实包和回查索引。
- 先持久化准备状态，创建带最新工具的新 thread，注入交接包并确认成功后，原子校验来源映射再切换。失败保留旧映射；不得重送已处理用户消息。中断留下的目标 thread 可以重用，避免重复创建。
- 原 thread ID/path 和祖先链永久保留；模型配置沿用现有 settings/defaults，新 thread 的第一次用户 turn 获取完整最新账本上下文。
- owner API 的 conversation.handoff 经 app 去重并与用户 turn 同目的地排队，CLI 提供 tend conversation handoff [--prepare-only]，可主动触发无 schema 变化的交接。

## 分工和接口

1. store/app/protocol/api：追加迁移 0026；owner 摘要设置、带完整覆盖信息的快照与摘要、thread 工具指纹及原子迁移记录；handoff 命令排队到 main_thread。读路径经 app/store，所有写经 app。
2. harness/traex：可选隔离 fork、原生 thread 路径信息、临时分支释放；协议及 fake/实际安装版隔离验证。不得影响现有 Session 实现和 worker。
3. mainthread：schema 检测、摘要任务与覆盖校验、50 分钟 idle 调度、身份/工具禁用、handoff 启动与手动触发、失败恢复。时钟和定时器可注入。
4. CLI/Web/配置：独立设置接口及 schema 生成类型；紧凑设置入口与摘要状态，CLI 排队操作；README/config 示例与 D81。

## 验证

覆盖旧 schema 自动交接、摘要最新/过期、消息原地修改、生成时新消息不阻塞且版本不冒进、50 分钟边界/关闭/覆盖默认、没有活动或待投递消息才 fork、工具零副作用、原历史不污染、schema/创建/注入/切换失败和重启恢复、CLI 去重与 owner 鉴权、Web 保存/错误/跨设备同步。实际安装版 fork 测试使用隔离目录和本地 fake Responses，必要时有界真实模型验证；不接触正式会话和任务，清理所有自有资源。

完整 make check、规定的独立 review、PR CI、最新 main rebase 合入后部署并独立验收。

## 覆盖审查修订

摘要格式 2 在同一读事务中取得覆盖水位与输入：首份包含全部持久会话行；后续带前份摘要及新增、原地更新和删除标记。服务端完成通知未必进入 native 历史，必须作为额外观察传给临时分支。完整前份摘要 JSON 与观察 JSON 共用 256 KiB 预算，超限明确失败，不能截断后保存为最新摘要。最新账本事实另受 contextbuilder 预算约束。Web 接收会话变化后立即标摘要过期，合并重读，并拒绝变化前开始的旧响应覆盖新状态。

## 恢复路径补充

- 旧 turn/start 结果未知时，保留原始 turn 与 outbox unknown；已明确关闭原进程的历史不确定性不等同于当前活跃 turn。
- 连续升级遇到旧工具集的 prepared 意图时，在未发生外部调用的阶段重新准备当前工具集；已有确认 target 的 created/injected 意图先按已知阶段完成，再在隔离下迁移至当前工具集，最后才发布 readiness。不重放 creating/injecting/unknown。
- source resume 明确拒绝时保留旧 ID/path，使用缓存摘要与账本恢复新 thread；传输不确定性不触发替代创建。

- unknown 投递作为历史参考事实保留，不计入当前 pending 队列；摘要输入保留消息的未知状态。新人工交接若与另一个未结束 intent 冲突，明确失败并提示核对原请求，避免复用旧 intent 导致请求身份丢失或 FIFO 永久重试。
