# 直接执行与人工交接

Refs: D183。自动运行使用 `appserver` worker 与 `git` workspace。runner 本地 Agent 的 runtime 使用 `appserver-worker`，显式选择 CLI 与固定 executable；不把 `central-traex-worker` 当成直接执行后端，它仍会创建 Orca TUI。Issue-manager 的协调 transport 也须使用对应的直接 runtime。模型、provider profile、用途与 category 的实际支持性应在对应 CLI 环境中验证。

## 恢复 Issue 上下文

```sh
tend issue handoff-prompt ISSUE_ID
tend issue handoff-prompt ISSUE_ID --run RUN_ID --json
```

该查询需要 owner 身份，只有指定 Issue 实际负责的 Run 可以加入 prompt；证据关联不授予执行责任。查询不会创建 session、改变控制或调用模型。

prompt 包含 Issue/control/context 版本、来源水位、分页入口及已记录缺口。新会话先通过当前部署的 CLI 查询原始来源、范围修正、结果、交付与活动，按 `more` 和游标逐页读取；读完后重新核对版本并获取变化。第一页和摘要不能代表完整历史，`watermark` 表示已审来源水位，不等于最后消息。

## 续接具体 Run

```sh
tend run native-entry RUN_ID --provider appserver --json
tend takeover TASK_ID --reason 'Continue the selected Run manually'
```

`native-entry` 读取原 Run 冻结的执行环境、workspace 与原生 thread/turn。`workspace_recorded`、`session_recorded` 只表示记录存在。`resume_state=requires_takeover` 要求先取得人工控制；`requires_local_verification` 仍须在原 host 核验 workspace/session 与原自动 writer 已停止。CLI 不自动启动 Orca，不选择最新 session，也不凭记录宣称停止或可续接。显式 provider 与 workspace 不符会报告原因。

确认原 writer 已退出后才编辑。人工写入结束、工作区状态核对完成后，用 `tend resume TASK_ID --reason 'Manual work completed'` 显式归还。控制写入继续使用现有 request/CAS 规则；unknown 按原身份对账，不以新请求重放。原生 session 不可用时仍可用 Issue prompt 在新会话恢复账本上下文。

## 有界等待

worker 的 app-server 启动、恢复握手与首个 turn 请求共用六十秒预算；正常运行不受该启动期限限制。交接与原生定位查询为三十秒。新滚动升级批次持久保存二十分钟整体截止，默认 drain 六分钟；阶段期限与新 grant 受整体剩余时间限制，重试和阶段切换不延长批次。旧批次没有整体字段时保留历史分段语义。

到期后停止新安装授权，保留已固定候选的对账和取消路径；unknown 不自动变成 stopped。需要强制推进时使用 D180 的显式有界维护与本机身份核验入口。普通开发与运行共享 CPU，不因检查负载切换维护或迁移运行。

## 确定失败委派的纠正

协调工具 `issues_create` 可用 `corrects_request_id` 引用同一责任轮内原 `invalid_request` / `stale_revision` 失败。来源必须被原执行接纳，旧请求必须证明无副作用失败；只允许补齐空的 `plan.title` / `plan.intent`，或修正原先允许的 source/CAS 元数据，不能替换已设契约值。成功后显式关系解除该失败的收尾阻塞，原失败回执与事件永久保留。unknown、无关请求和缺少可核验历史快照的记录不被推断为已纠正。
