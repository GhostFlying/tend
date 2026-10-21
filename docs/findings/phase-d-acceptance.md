# 阶段 D 验收结论

计划 §12（W9）的验收分两步：

- 先在一台开发机上做本地预验收，覆盖不依赖 tailnet 和多台机器的部分（本节）；
- 剩下的部分和 owner 一起在实际部署的多台 runner 上完成，见“真机验收”一节。

## 本地预验收

**环境：**

- Claude Code 2.1.301；worker 使用 traex 0.210.1，模型 GPT-5.6-Sol。
- 服务端和两个 runner（`linux-1`、`linux-2`，各 1 个 slot，不同的 home）在同一台机器上。服务端不加入 tailnet，用 `listen` 和 `local_owner`（D49），租约 60 秒。
- 两个 project：`calc`（权重 2）和 `notes`（权重 1），各对应一个本地 bare 仓库，内容是很小的 Python 模块和单元测试。
- 会话在两个仓库的 clone 里启动。确定性的部分不经过模型：直接调用 `tend hook` 和 `tend mcp`（JSON-RPC），会话 ID 自定；模型行为用 Claude Code 的 `-p` 会话检查。
- runner 的 host 退役用改服务端配置、重启服务端完成；runner 重启用 SIGTERM 后重新启动。

**环境说明：** CLI 只发送网络形式的远程地址，不发送本地路径（D59），所以用本地 bare 仓库做远程时会话不会绑定 project。验收时仓库在服务端注册为 `https://` 形式的地址，clone 的 origin 用同一个地址；runner 进程通过 git 的 `url.<base>.insteadOf`（`GIT_CONFIG_*` 环境变量）映射到本地仓库。

### 结果

| 项 | 结果 |
| --- | --- |
| 会话按仓库绑定 project | 通过：在 clone 里调用工具后，完整快照写明当前 project，只列它的任务，另附其他 project 的数量汇总；`tasks_create` 不带 project 时归入会话的 project |
| 另一 project 的提醒 | 通过：另一 project 的新待决事项和失联的 run 出现在 “Other projects, needs attention” 一节，每行标明 project；它的普通进度不出现 |
| Claude Code 会话 | 通过：只描述本 project 的任务；另一 project 出现待决事项后，在回答无关问题时主动提出，并在 owner 选择后用 `decisions_resolve` 记录，tend 执行了效果 |
| 排队顺序 | 通过：`tend status` 的队列与分数一致（`p0` 最先，其后 calc `p2` 4.0、notes `p1` 3.0、calc `p3` 2.0、notes `p3` 1.0）；runner 领取顺序相同，验收与同分的实现相比先领 |
| host 退役，任务有待执行的验收 | 通过：一分钟内以 `workspace_unavailable` 阻塞并开出待决事项；`tend decide` 选择 “start over on another host” 后，旧工作区记入 `abandoned_workspaces`，第二代实现在另一台 host 上新建工作区，验收通过 |
| host 退役时 run 正在执行 | 发现缺陷 F1，修复后通过 |
| host 回来 | 通过：一分钟内解除阻塞，tend 以 “wait for the host” 回答待决事项；runner 重启后接管中断的 run，worker 续接原 thread |
| runner 在 run 中途重启 | 通过：重新启动约 30 秒后 run 回到 running，worker 收到“执行被中断”的短 turn，续接原 thread，没有重发完整 brief；run 正常完成并通过验收 |

### 发现并已修复的缺陷

- **F1**：工作区检查只看有待执行的实现或验收动作的任务。host 退役时正在执行的 run 在租约到期后变成 unknown，它的动作仍是已派发，任务既不阻塞也不提问，停在 running，正是 D62 要避免的静默卡住。现在工作区所在 host 上有 unknown 的 run 也算在等这台 host（#71）。

### 其他观察

- 会话在调用 tend 工具之前，hook 不注入任何上下文（D54）。验收脚本需要先调用一次工具，真实会话里 agent 第一次用 tend 时自然满足。
- worker 是否提交由它自己决定（D57）：一次实现把改动留在工作区未提交，产物记为 “uncommitted changes in snapshot”，验收照常进行并通过。

## 真机验收

待和 owner 一起在实际部署的多台 runner 上完成：

- 两台以上的真实 runner，排队顺序与分数一致；
- 让一台 host 退役或断网超过 `host_lost_after`，确认待决事项、“start over” 和 host 回来后的自动解除；
- 两个 agent 会话分别在各自的仓库目录里启动，确认上下文按 project 划分；
- runner 在 run 中途重启，worker 续接原 thread。
