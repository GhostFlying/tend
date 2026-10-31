# Web 交互沙盒

Refs: D133、D132。Plan: S7。

状态：已实现。

## 边界与提交

1. 文档提交：本计划、D133 与总计划登记。
2. 重构提交：将正式服务端组装与相应配置／测试移到 `internal/serverkit`，入口仍解析同一配置。通过 Dependencies 显式注入网络身份、托管会话 opener、时钟／ID 及初始化／后台生命周期；默认使用原真实边界。Telegram 传输 opener 同样显式注入，正式配置与准备／投递／轮询行为沿 D131 保留；沙盒不提供 Telegram 配置或凭据，通用阅读回执 dispatcher 及通知 fanout 仍走共享组装。更新 AGENTS.md 依赖方向。
3. 功能提交：`cmd/tend-sandbox` 解析参数，`internal/sandbox` 承载专用脚本与 demo，不被正式服务端依赖。复用 testkit 会话，通过真实 tools 回调执行操作；真实 runner 使用脚本 Workers 与限定在数据目录内的 Workspaces。

不修改 owner API、runner wire、迁移、生成类型或发布集合，不新增依赖。沙盒数据与进程生命周期独立于生产；禁止未标记非空目录、标记内容错误或符号链接目录，重置仅删除已验证沙盒目录中的内容。

## 脚本规则（按顺序匹配）

| 输入 | 动作 |
| --- | --- |
| 包含“新建”或“创建” | 主会话通过 issues_create 指派给登记的沙盒负责人；负责人只按直接发到该 Issue 会话的消息创建子 Issue；继承的新建来源不再次执行 |
| 包含“派发”或“执行” | 主会话先创建 Issue 并共享原消息给负责人；负责人通过 issues_create_task 冻结任务要求并派发 |
| 包含“问我” | attentions_create 创建可回答的 decision |
| 其它 | 短中文回答，分几个 delta 延迟输出；负责人可通过 issues_read 读取上下文 |

主会话使用当前合同仍支持的具体 hosted manager 指派，不伪造 runner manager 原生通信；执行任务仍由真实 runner 领取。demo 使用 report/files 组合交付，不保留已退役的 report delivery 字段。失败样本显示真实 failed run 与 waiting 任务，不添加不存在的 failed 任务生命周期。

每个工具调用均以本次 turn/call 身份去重并记录真实工具事件；每轮读取、分类并提交当前消息版本及上下文 review，以满足托管逻辑 turn 合同。任务默认数秒输出进度、交付 Markdown 文件并由独立 acceptance 脚本通过；包含“失败”的任务产生失败，包含“问我”的任务通过 AskOwner 等待 UI 回答，之后继续。取消关闭所有会话和执行 goroutine。干净退出后持久化脚本原生身份与实际 receipt，重启恢复原会话，避免复用新进程的 turn ID；不为未知历史伪造完成。

## demo

真实 app 命令配置多个项目／仓库、在线脚本主机与离线主机、Agent／类别目录；建立每个看板列（含已取消）及关闭状态的 Issues、长标题、父子层级、进展与交付／会话；建立完成、失败和运行中的任务；包含 decision、action、information 收件箱条目、主对话工具历史与 Token 用量。播种使用稳定 request_id，重复启动不重复创建；进程先启动真实 runner 并等候能力登记，再通过 app 命令播种组合交付任务；失败的播种可重试原请求，完成标记仅在完整播种后写入。真实运行任务终态自然推进，不通过 SQL 伪造状态。

## 开发入口

`make sandbox` 先构建 Web，然后使用临时数据目录运行；`make sandbox SANDBOX_DATA=<dir>` 保留 demo 与后续交互。额外参数通过 `SANDBOX_ARGS` 传入，例如 `-reset`、`-listen`。

HMR：先运行沙盒，再以 `TEND_DEV_SERVER=http://localhost:8790 pnpm --dir web dev` 启动 Vite。沙盒默认仅绑定回环；外部访问由协调者明确配置 listener／代理，不自动加入 tailnet。

## 验证

默认 Go 测试使用临时目录与回环 listener，不调用外网或模型：登录与 owner API 读取每个看板列／三类 attention，主会话派发走真实工具和 runner 达到终态，owner API 回答决定并验证解决；再次播种验证幂等；错误目录与 reset 验证保护；取消等待所有后台循环退出，目标少于 30 秒。

运行全部 Web 门禁与完整 make check。桌面／手机浏览器 smoke 验证派发、终态与收件箱回答，截图和日志置于仓库外，验收后停止沙盒。

## 本地实施记录

已实现共享组装、空目录／标记与运行锁保护、脚本工具与可取消流、回环 runner、真实文件上传、六列看板与三类 attention demo。Go 交互测试同时覆盖普通决定与 worker 问题的回答／恢复。已有 JSON fixture 脚本未改动；迁移到真实沙盒留给后续 UI 巡检。

## S7 独立评审修正

R1：沙盒包装每个 FakeThread，为每轮脚本建立独立可取消 context；Interrupt 先取消并等待脚本输出／工具回调退出，再报告 interrupted，不修改正式 harness 或 testkit 的默认行为。增加真实 owner API 的发送、停止和停止后不再产出完整回答／工具调用检查；普通回答说明停止入口，并以 120 ms 的小片段延迟保留可操作的停止窗口；测试通过沙盒私有的首片段同步点保持流式窗口，避免 race／负载使短回答在观察前完成。

R2：make sandbox 仅将自动生成的临时目录解析为物理路径；显式传入的符号链接目录仍被拒绝。

V1：沙盒工具调用与 metadata 的读取／CAS 临界区可取消地串行执行，避免假主 Agent 与假负责人同时改动同一 review 上下文；流式输出、原生事件持久化与真实 runner 仍独立并发推进。先等完整回答被真实账本接收，再读取、分类并提交精确消息版本；保留已有关联，并验证主会话派发的来源／回答关联与所有工具结果。V2 的 worker_failed 决定说明来自正式领域代码，保持不变。V3：三个种子的 Attention 使用不同的真实原始说明来源；不添加协议 body 字段或直接写数据库。
