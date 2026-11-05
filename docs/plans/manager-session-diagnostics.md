# Manager 会话关闭原因的精确观测

Refs: D173。

## 问题与范围

原生 manager 的正常 close 可以把仍有业务回执的 transport run 记为 failed。
当前 serve 的返回错误只在全部清理完成后输出，清理告警缺少 run/thread/request
身份，无法区分关闭触发原因与关闭过程中出现的错误。main_turns.failure_json
只保存原生 AgentFailure 分类；native close 请求也没有保存 serve 返回错误。

本变更只补结构化生命周期日志，不修改关闭、重试、超时、调度、租约或业务状态。
不新增 wire、数据库迁移或用户操作；内部日志不产生新 CLI 能力。

## 实施

1. 每次 serve 只冻结当场确定的 instance、conversation、Issue、runner run 和 assignment epoch。
   不缓存跨 delivery、跨重连的 thread、turn 或 request；无法确定的身份直接省略，
   依赖 run 和 Issue 在持久账本中关联。日志不增加 Manager 生命周期状态。
2. 清理开始前记录原始 serve 返回错误与 context.Cause，close 的 begin/end 与 cleanup
   error 独立记录，不覆盖原始错误。已有显式 close 的直接调用处可记录已知的
   initiating_request_id，不猜测它代表哪一个 native turn。
3. BackgroundPool 在 cancel 前记录原返回阶段、原错误与直接处理的 delivery request/outcome。
   不把首次 startup thread 当作取消时的当前线程，不跨重连保存身份映射。
4. 日志不得读取或输出 prompt、正文、环境、配置、凭据或工具参数。定向验证原错误/ctx
   和 cleanup 不混淆、Close 阻塞前已有原 cause、coordination 的一次关闭语义不变。

前期独立审查的跨回合身份保留问题不再适用：删除相关缓存和断言，而不是声称已经
修复所有 thread/turn/request 归属。本变更的目的仅是可靠区分 serve 与 pool 的退出原因。

## 验证与交付

必要定向回归和 race 先验证目标。随后固定候选并行 PR CI、规定独立 review；
本机完整 make check 等现有唯一全量检查释放资源后运行，不并发第二套重全量。
最终精确 head/base 全门通过后线性合入；生产二进制和原件验证交唯一 operator。
