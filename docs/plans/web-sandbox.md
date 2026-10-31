# Web 交互沙盒

Refs: D132、D131。Plan: S7。

## 边界与提交

1. 文档提交：本计划、D132 与总计划登记。
2. 重构提交：将正式服务端组装与相应配置／测试移到 `internal/serverkit`，入口仍解析同一配置。通过 Dependencies 显式注入网络身份、托管会话 opener、时钟／ID 及初始化／后台生命周期；默认使用原真实边界。更新 AGENTS.md 依赖方向。
3. 功能提交：`cmd/tend-sandbox` 解析参数，`internal/sandbox` 承载专用脚本与 demo，不被正式服务端依赖。复用 testkit 会话，通过真实 tools 回调执行操作；真实 runner 使用脚本 Workers 与限定在数据目录内的 Workspaces。

不修改 owner API、runner wire、迁移、生成类型或发布集合，不新增依赖。沙盒数据与进程生命周期独立于生产；禁止未标记非空目录、标记内容错误或符号链接目录，重置仅删除已验证沙盒目录中的内容。

## 脚本规则（按顺序匹配）

| 输入 | 动作 |
| --- | --- |
| 包含“新建”或“创建” | 主会话通过 issues_create 创建 Issue；负责人会话创建其子 Issue |
| 包含“派发”或“执行” | 主会话先创建 Issue 并通知负责人；负责人通过 issues_create_task 冻结任务要求并派发 |
| 包含“问我” | attentions_create 创建可回答的 decision |
| 其它 | 短中文回答，分几个 delta 延迟输出；负责人可通过 issues_read 读取上下文 |

每个工具调用均以本次 turn/call 身份去重并记录真实工具事件；每轮读取、分类并提交当前消息版本及上下文 review，以满足托管逻辑 turn 合同。任务默认数秒输出进度、交付 Markdown 文件并由独立 acceptance 脚本通过；包含“失败”的任务产生失败，包含“问我”的任务通过 AskOwner 等待 UI 回答，之后继续。取消关闭所有会话和执行 goroutine。

## demo

真实 app 命令配置多个项目／仓库、在线脚本主机与离线主机、Agent／类别目录；建立每个看板列（含已取消）及关闭状态的 Issues、长标题、父子层级、进展与交付／会话；建立完成、失败和运行中的任务；包含 decision、action、information 收件箱条目、主对话工具历史与 Token 用量。播种使用稳定 request_id，重复启动不重复创建；失败的播种可重试原请求，完成标记仅在完整播种后写入。真实运行任务终态自然推进，不通过 SQL 伪造状态。

## 开发入口

`make sandbox` 先构建 Web，然后使用临时数据目录运行；`make sandbox SANDBOX_DATA=<dir>` 保留 demo 与后续交互。额外参数通过 `SANDBOX_ARGS` 传入，例如 `-reset`、`-listen`。

HMR：先运行沙盒，再以 `TEND_DEV_SERVER=http://localhost:8790 pnpm --dir web dev` 启动 Vite。沙盒默认仅绑定回环；外部访问由协调者明确配置 listener／代理，不自动加入 tailnet。

## 验证

默认 Go 测试使用临时目录与回环 listener，不调用外网或模型：登录与 owner API 读取每个看板列／三类 attention，主会话派发走真实工具和 runner 达到终态，owner API 回答决定并验证解决；再次播种验证幂等；错误目录与 reset 验证保护；取消等待所有后台循环退出，目标少于 30 秒。

运行全部 Web 门禁与完整 make check。桌面／手机浏览器 smoke 验证派发、终态与收件箱回答，截图和日志置于仓库外，验收后停止沙盒。
