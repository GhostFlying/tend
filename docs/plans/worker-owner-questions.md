# Worker 向 owner 提问并续接（D89）

## 目标

调查、实现及验收 worker 可以把需要 owner 判断的问题交回同一个聊天与待决面板，收到答案后在原任务、原 run、原工作区和可恢复的 native 会话继续。主 Agent 只协调和转交，不替 worker 做技术调查，不替 owner 回答。

## 执行选择

本期保留运行中的 run、执行 slot 和心跳，ask_owner 工具回调在代码层等待；不启动模型轮询，不引入 parked-run 生命周期。正常回答回到同一个工具调用；进程重启后的原生调用可能已中断，通过持久问题标识、原始参数和答案续接，不承诺原生调用跨进程仍存活。等待期间会占用容量并影响 runner 升级排空，界面明确说明；owner 仍可暂停/取消或人工接手。

## 协议与数据

- ask_owner 输入为本 run 内稳定的 question_key、问题、调查证据/背景和可选选项。相同 key 与内容幂等返回已有问题/答案；内容变更明确冲突；同一 run 同时一个未决问题。
- 新增 worker_questions 关联表，保存 decision_id、run_id、action_id、spec_version、question_key、payload hash 和证据。问题与答案复用既有 decisions，保留来源消息、问题/答案的关联；不引入话题/tab 模型。
- 创建问题带 request_id，由 app 在一个事务内持久化去重、关联、事件及通知。run 身份仅能问/读自己的问题；已结束、取消或 spec/action 失效的 run 不得继续。普通暂停保留问答以便恢复。
- 通过可取消、有界长轮询取得答案，不占数据库事务等待，不消费答案。服务器/runner 重启或回执丢失不重复创建问题，不把未收到答案解释成默认同意。
- Assignment 协商是否支持 worker 提问，新 runner 在旧 server 上不暴露该工具；旧 runner 恢复带问题的 run 时不得静默忽略问答。评估所有线上字段，保持旧有效请求语义；若无法通过可选协商兼容，则按协议规范递增版本。

## Runner 与 harness

- 两种 harness、两种角色共用 AskOwner callback；请求信封在发送前原子保存，按稳定 question_key 找回。恢复 brief/continuation 带回未决问题原参数和已保存答案，要求复用同一个 key。
- MCP 等待处理显式取消、EOF 和连接断开，不泄漏 goroutine。仅对可按同一问题身份重放的 ask_owner 进行连接恢复，不扩展 submit_result/verdict 的自动重试。
- 显式配置足够长的 MCP 人工等待超时，并核验实际 harness 是否识别；传输超时不会使持久问题失效，也不会替用户作答。

## Owner 与主会话

- DecisionView 返回 worker 来源、run/角色/spec 及证据；任务详情/待决面板明确显示 worker 提问与等待占用 slot。
- 选项和自由文本直接写 decisions.respond；chat 回答可由主 Agent 基于真实 owner 消息解析并记录到同一问题。背景事件不能冒充 owner 回答。
- 用户答案改变任务目标/授权时，主 Agent 更新 spec；旧 spec 的等待不能继续执行新目标。继续同一调查的问题本身不算完成，也不创建一次新的实现代次。
- 开启/回答沿 D88 通知和唤醒链路。问题与答案的 reply/来源关联为未来独立 thread 视图保留基础；本期不新增线程 UI，话题的拆分/合并留待讨论。

## 验收

1. 实现和验收 worker 均能提问；owner 选项/自由文本/chat 回答后收到原答案，任务不因提问而结束。
2. 同请求重放、不同请求同 key、断开、runner 恢复和回答回执丢失不生成重复问题；不同 payload 同 key 明确冲突。
3. 暂停/取消、spec 更新、旧 run 回答、服务重启均保留正确问答归属；失效答案不恢复旧工作。
4. 等待期间心跳有效、模型不轮询；原 thread/worktree 续接有测试证据，无法恢复原调用时使用明确恢复路径。
5. MCP cancellation/EOF 能结束等待；普通结果工具不被自动重放。新旧 server/runner 组合的能力协商有回归。
6. 实际浏览器覆盖问题来源、证据、选项和自由文本；后端覆盖真实 HTTP 问答。使用隔离环境，清理自己创建的测试资源。
7. make check、独立 review、CI 通过后按仓库流程交付。
