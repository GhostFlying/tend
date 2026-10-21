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

**环境：**

- 两台真实的 Linux 开发机，各运行一个 runner（1 个 slot），都通过 tsnet 加入 tailnet；服务端和其中一台 runner 在同一台机器上。两台机器出网都经过 HTTP 代理。
- 两个 project：tend 自身（权重 2）和 owner 的另一个公开 Go 仓库（权重 1），两台 runner 都服务这两个仓库。
- 会话：两个 Claude Code `-p` 会话，分别在两个仓库的 clone 里启动，使用单独的配置目录、owner 的 tend 登录。其余任务直接调用 `tend mcp` 创建。
- worker 使用 traex，模型 GPT-5.6-Sol。任务都是很小的测试性改动，要求在任务分支上提交、不推送；验收结束后确认两个仓库的远端都没有任务分支。
- 宕机用 SIGKILL 停掉一台机器的 runner 来模拟（worker 的进程树随之退出），恢复用重新启动。为了在合理时间内看到失联判定，`host_lost_after` 临时设为下限 `10m`，结束后恢复默认。

### 结果

| 项 | 结果 |
| --- | --- |
| 会话按仓库绑定 project | 通过：两个会话建的任务分别归入各自的 project，不需要指定 |
| 两台 runner 的排队顺序 | 通过：跨 project 的队列与分数一致（`p0` 最先，其后分数 3.0、2.0）；两台 host 的领取顺序相同。工作区亲和正确：一台 host 空出来时先领 `p0` 的实现，自己工作区里分数较低的验收继续等它 |
| host 宕机，验收 run 正在执行 | 通过：租约到期后 run 变为 unknown；失联满 10 分钟时任务以 `workspace_unavailable` 阻塞，问题写明 host 最后一次有消息的时间。另一台 host 照常领取其他任务 |
| 另一 project 的提醒 | 通过：另一个 project 的会话在回答无关问题时主动提出这个待决事项，并说明它不属于本 project |
| host 恢复 | 通过：重新启动后约 50 秒解除阻塞，tend 以 “wait for the host” 回答；被打断的验收 run 续接原 thread，worker 说明了中断发生在 `make check` 的哪一步、还没有提交结论，随后完成验收 |
| host 宕机，实现 run 正在执行，选择重来 | 通过：会话里按 owner 的指示回答 “start over on another host”，tend 执行；旧工作区记入 `abandoned_workspaces`，第二代实现在另一台 host 上从远端新建工作区，验收通过 |
| 放弃了工作区的 host 回来 | 通过：hello 时 tend 让它停掉旧的 run（“the run is no longer wanted”），这台 host 不会把任务领回去 |

真机验收没有发现新的缺陷。

### 观察

- 待决事项里的时间原先是 UTC（RFC 3339），不便阅读。现在用服务端的本地时区，带时差（#74）。
- host 宕机期间，状态未知的 run 仍占着那台 host 的 slot，`tend status` 显示它在忙。这与实际情况一致：run 可能还在运行。
