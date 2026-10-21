# 阶段 D 计划：project、规则排序与 run 续接（W1–W9）

**日期：2026-10-21，owner 已确认 §14 的选择**
**依据：`docs/design.md` D31、D41、D47、D51–D54；`docs/plans/phase-c.md` §12；`docs/findings/phase-c-acceptance.md` 的真机验收与故障演练；约定见 `AGENTS.md`。**
**前置：阶段 C 及其后续（`docs/plans/phase-c-followups.md`，C11–C22）已合并，真机验收完成。**

本阶段的工作项编号为 W1–W9，避免和决策编号 Dnn 混淆。

阶段 C 之后，owner 已经可以在自己的 agent 会话里把工作交给 tend，tend 在一台机器上按优先级执行。下一步的问题来自多条工作线并行：所有任务挤在同一份上下文里，模型容易把不同工作线的任务混在一起；排序只有一个整数优先级，owner 无法表达“这条线多分一些机器”；backlog 越积越多时，没有规则决定先做什么。D52 提出用 project 解决这些问题，本阶段把它落地。另外两项来自真机验收：中断后的 run 从头开始；工作区所在机器不可用时任务静默卡住。

## 0. 范围与完成标准

- **交付物**：
  - project：服务端配置定义，任务属于一个 project（W1）；
  - 会话按所在仓库绑定 project，上下文按 project 划分，需要处理的事件跨 project 提醒（W2、W3）；
  - 任务评级与未评级提醒（W4），按 project 权重、评级和等待时间算分排序（W5）；
  - `tend status` 按 project 显示队列，`tend rate`（W6，§7）；
  - 中断的 run 续接原来的 worker thread（W7）；
  - 工作区所在机器不可用时阻塞任务并开启待决事项（W8）；
  - 真机验收（W9）。
- **完成标准**：
  - `make check` 通过；
  - §11 列出的测试全部通过；
  - 一个端到端测试：两个 project、两个会话分别在两个仓库里，各自只看到自己 project 的任务，另一 project 的新待决事项仍然提醒到；
  - 真机验收（§12）。
- **不在本阶段**：
  - 托管主 Agent（D45）；Web、SSE 和通知 channel（D50）；
  - 和 cove 的执行层对接，以及经 cove 的人工接管（D51，见 §10）；
  - worker 支持 traex 以外的 agent；
  - Claude Code 在同一进程里 `/clear` 后无法绑定的问题（C15 记录的风险，真机验收未决问题 8）：遇到再处理。

## 1. 需要写入 design.md 的决策（提议）

| 编号 | 决策 | 落地 |
| --- | --- | --- |
| D52 | 由“提议”改为“已确认”，细节按本计划的 D58–D60；排序按分数（D60），不按机器份额。 | W1 |
| D58 | project 在服务端配置里定义，和仓库、host 一样启动时同步进账本（§2）。 | W1 |
| D59 | 会话按 cwd 所在仓库的远程地址自动绑定 project，也可以显式选择；上下文按绑定的 project 划分，需要处理的事件跨 project 提醒（§3、§4）。 | W2、W3 |
| D60 | 评级 `p0`–`p3` 取代整数优先级；`p0` 最先，其余按分数排序：project 权重 × 评级分 + 等待小时数 × 等待系数（§5、§6）。 | W4、W5 |
| D61 | 同一个 run 的下一次尝试续接原来的 worker thread，失败时退回新 thread（§8）。 | W7 |
| D62 | 工作区所在 host 退役或失联过久时，有待执行动作的任务以 `workspace_unavailable` 阻塞，并开启带效果的待决事项（§9）。 | W8 |

## 2. W1 project 的定义（D58）

- **配置**：服务端配置增加 `projects` 列表，和 `repos`、`templates` 写法一致。每项包括：
  - `name`：project 名，规则和仓库名相同；
  - `title`：显示名；
  - `weight`：正整数，默认 1，是任务分数里评级分的乘数（§6）；
  - `repos`：project 涉及的逻辑仓库名，必须已在 `repos` 里注册；用于会话绑定（§3）；
  - `template`：可选，默认的工作区模板名。
- **同步**：启动时写入账本的 `projects` 表。从配置删除的 project 标为 retired：已有任务照常进行，不能再建新任务。
- **默认 project**：配置里没有 `projects` 时，tend 使用隐含的 `default` project，包含全部仓库，权重 1。只有一条工作线的 owner 不需要任何配置。迁移把已有任务归入 `default`。
- **任务属于一个 project**：`tasks.project` 列，在创建时确定，`tasks_update_intent` 可以修改（校验版本，产生事件）。
- **`tasks_create` 的 project**：
  - 调用方给出了就用它；
  - 否则用会话绑定的 project；
  - 会话未绑定、但只有一个活动 project 时用它；
  - 以上都不满足时返回 `invalid_request`，并列出可选的 project。
  
  未给出工作区时，使用 project 的默认模板（如果有）。
- **为什么放在配置里，不用工具创建**：
  - project 很少变；
  - 权重决定 project 的任务排在多前，是 owner 的资源分配，不应该由 agent 随手修改；
  - 这样也不需要新增 owner 写入接口。
  
  代价是增删或修改 project 要改配置、重启服务端，和仓库相同。

## 3. W2 会话绑定 project（D59）

- **CLI 发送仓库地址**：`tend hook` 和 `tend mcp` 读出 cwd 所在 git 仓库的远程地址（全部 remote），随请求发给服务端；cwd 不在仓库里时不发。不发送本地路径。
  - cwd 取 hook 输入里的 `cwd` 字段，没有时用进程的工作目录。实施前用 C5 的 probe 确认三个 agent 是否都提供这个字段。
  - CLI 只依赖标准库，读取远程地址用 `git` 子进程，失败就当作不在仓库里。
- **服务端匹配**：
  1. 把远程地址规范化：去掉 scheme 和用户名，host 改为小写，去掉 `.git` 后缀；ssh 的 `host:path` 和 https 的 `host/path` 视为相同。
  2. 和已注册仓库的 `remote_url` 比对，得到仓库。
  3. 包含这个仓库的活动 project 恰好一个时自动绑定；没有或有多个时不绑定。
- **显式选择**：新工具 `session_project`，参数是 project 名，空值表示全部。
  - 显式选择优先于自动绑定，持续到会话结束。
  - `tend_context` 的输出写明当前绑定、绑定来源和可选的 project。
- **绑定变化**：自动绑定第一次确定或显式切换时，下一次 hook 返回完整快照，重建基线。
- **记录**：`agent_sessions` 增加 `project` 和 `project_source`（`auto` 或 `explicit`）。绑定在 D54 的会话绑定之上，未绑定 tend 的会话仍然什么都不收到。
- **未绑定 project 的会话**：和现在一样看到全部内容，每个任务行多一个 project 标签。

## 4. W3 上下文按 project 划分（D59）

- **完整快照**：只列绑定 project 的未结束任务和待决事项。另附一行汇总：其他 project 有多少未结束任务、多少待决事项。
- **增量**：只含绑定 project 的事件。
- **跨 project 提醒**：其他 project 中需要处理的事件（D47 的 attention 类：新待决、run 失败或失联、未通过的验收、投递失败）照常进入增量，放在单独的 “Other projects” 一节，每行带 project 名。其他 project 的结果和进度不发。
- **读取工具**：`tasks_list` 等读取视图增加 `project` 参数，默认是会话绑定的 project，`*` 表示全部。
- **写入**：不受绑定限制。会话可以操作其他 project 的任务，例如回答提醒里的待决事项；版本校验照旧。

## 5. W4 评级（D60）

- **取值**：任务带评级 `rating`，取值和含义如下，也可以为空（未评级）：
  - `p0`：紧急，排在一切之前；
  - `p1`：高；
  - `p2`：普通；
  - `p3`：低。
- **取代 `priority`**：
  - 迁移把 `priority > 0` 记为 `p1`，`= 0` 记为 `p2`，`< 0` 记为 `p3`；
  - 之后不再读写 `priority` 列（迁移只追加，列保留）；
  - 工具的 `priority` 参数改为 `rating`，`tasks_update_intent` 可以修改评级。
- **未评级**：
  - 排序时按 `p2` 处理，不阻塞执行；
  - `tasks_create` 没带评级时，结果里提示主 Agent 向 owner 确认评级（外挂的主 Agent 只在用户输入时运行，派发时用户就在场，D52）；
  - 完整快照里，绑定 project 的未评级任务单独列为一节；
  - 增量里，别的会话或 owner 新建的未评级任务带一行提醒。
- **人工设置**：owner 命令 `tend rate <task_id> <p0|p1|p2|p3>`，带读到的版本。

## 6. W5 排序（D60）

排序回答的问题是：runner 有空位时，从当前排队的动作（某个任务的一次实现或一次验收）里先领哪一个。只有排队的动作多于空位时才起作用。

调度器仍是纯函数（AGENTS.md §1.1）。在某个 host 可执行的候选中：

1. **`p0` 先行**：有 `p0` 时，按等待时间取最早的。
2. **其余按分数**，分数高的先：

   分数 = project 权重 × 评级分 + 等待小时数 × 等待系数

   - 评级分：`p1` 为 3，`p2` 为 2，`p3` 为 1；未评级按 `p2`；
   - 等待小时数从动作创建（开始排队）算起；
   - 等待系数在服务端配置 `queue.wait_per_hour`，默认 0.1：权重为 1 时，等 10 小时相当于升一级。
3. **同分时**，验收先于实现（先推进已经有产物的任务），再按等待时间，最后按动作 ID。

例：A 权重 3、B 权重 1。A 新来的 `p3` 任务 3 分，B 的 `p2` 任务 2 分，B 等 10 小时后超过它；B 的 `p1` 和 A 的 `p3` 同为 3 分，按等待时间先后。权重是重要程度的乘数，不是机器份额：A 持续有新任务时，B 主要靠等待时间排上。这种规则只用当下的信息，每个任务的位置可以直接用分数解释。

正在运行的工作不被抢占（不变）。现有的资格规则（OS、标签、仓库、工作区所在 host、工作区占用等）不变，排序只作用于有资格的候选。

## 7. W6 owner 视图

- `tend status` 按 project 分组。每个 project 显示权重、运行中和排队的数量；排队的任务按调度顺序列出，带评级、等待时间和分数。`--project` 只看一个 project。
- 读视图（`protocol` 类型）增加 project 列表和队列视图，任务视图增加 `project` 和 `rating`。owner API 提供同样的读取。
- `tend rate`（§5）。

## 8. W7 run 续接（D61）

- **问题**：故障演练里，中断后的 run 重新开始时 worker 开新 thread，从头执行 brief。工作区保留了中断时的改动，但中断前模型的工作要重做一部分。暂停后恢复也是同样的路径。
- **做法**：
  - runner 在 thread 建立后立即把 thread ID 记进 journal。
  - 同一个 run 的下一次尝试（runner 重启后 tend 让它继续，或暂停后恢复）先用 `thread/resume` 接上原 thread，再发一个短 turn：说明执行被中断，工作区保持中断时的状态，继续完成原来的任务。
  - resume 失败时（rollout 不存在、traex 版本不兼容等），退回新 thread 加完整 brief，并记一条 info。
  - 验收 run 同样续接。动态工具在 resume 之后仍然有效（`docs/findings/phase-b-traex.md`），`submit_verdict` 可以照常使用。
- **只在同一个 run 内续接**：新代次是新 run，总用新 thread。新代次的含义是重来或带着验收意见修改，brief 已经包含需要的信息。

## 9. W8 工作区所在机器不可用（D62）

- **现状**：工作区建在第一个领取实现 run 的 host 上，产物（提交和快照）只存在那台机器的仓库里，之后的 run 只能在那台 host 上执行。那台 host 退役或长期离线时，任务只是一直排不上，没有阻塞原因，也没有提醒。
- **做法**：
  - 工作区所在 host 退役，或失联超过 `host_lost_after`（服务端配置，默认 24 小时）时，有待执行动作的任务以新阻塞原因 `workspace_unavailable` 阻塞，并开启待决事项。选项如下，带效果的按 D53 执行：
    - “wait for the host”：无效果；
    - “start over on another host”：有效果。放弃旧工作区，下一个领取的 host 从 base 新建工作区，开始新一代实现；旧产物不再参与验收；
    - “cancel the task”：有效果。
  - host 回来（hello）时自动解除阻塞，未回答的待决事项由 tend 以“host 已恢复”关闭，进入增量。
- **验证**：实际部署会有不止一台 runner。测试和开发期验证可以在同一台机器上运行第二个 runner（不同的 host ID 和 home）；真机验收用实际的多台 runner。

### 9.1 W8 实施细节

- **判定**：服务端每分钟检查一次（和租约清扫并列）。任务的工作区所在 host 已退役，或 `last_seen_at` 早于 `host_lost_after`（默认 24 小时，`0` 关闭失联判定，退役仍然生效），并且任务受托管、没有其他阻塞、有待执行的实现或验收动作时，任务以 `workspace_unavailable` 阻塞，开出待决事项，产生 `task.workspace_unavailable` 事件（需要处理的一类）。检查由系统 principal 执行的命令 `workspaces.check` 完成，什么都没变时不留请求记录。
- **待决事项**：选项为 “wait for the host”（无效果）、“start over on another host”（效果 `start_over`）、“cancel the task”（效果 `cancel`），按 D53 只对提问时的版本生效。
- **再问一次**：回答“start over”时任务若已经变过（例如改了评级、暂停后恢复），按 D53 效果不执行，而 start over 没有单独的命令。所以检查时，仍然以 `workspace_unavailable` 阻塞、host 仍不可用、既没有开着的这类待决事项、也没有在当前版本问过的任务，会再开一个（事件带 `asked_again`）。owner 在当前版本选了等待，就不再问。
- **重来**（`domain.StartOver`）：版本加一，实现代次加一，任务回到排队，清掉验收中的产物，取消所有未完成的动作，新建下一代实现动作，事件 `task.started_over`。app 把旧工作区的记录移到 `abandoned_workspaces`（保留工作区 ID、名称和 host），删除工作区和挂载的记录；下一个领取的 host 按原来的规则新建工作区，但不能是放弃了工作区的 host：那里的工作区目录和任务分支还在，回来后会被接着用。旧 host 上的目录不动。旧工作区里的 run 迟到的 `report_workspace` 被拒绝：工作区不在这个 run 的 host 上（或任务还没有新工作区）时不记录挂载。
- **工作区占用**：只看当前工作区所在 host 上未结束的 run。重来后旧 host 上状态未知的 run 不再阻止新工作区；旧 host 回来时，这些 run 的动作已取消，hello 让 runner 停止。
- **在线**：hello、心跳和领取都记下 host 的 `last_seen_at`（领取每分钟最多记一次）。空闲的 runner 不发心跳，只在长轮询领取，所以领取也要算。
- **恢复**：同一个检查也处理反方向：工作区所在 host 重新可用后，以 `workspace_unavailable` 阻塞的任务解除阻塞（`task.workspace_available`），并以 tend 的名义回答还开着的这类待决事项（选项 “wait for the host”，说明 host 已恢复），进入增量。这样不论 host 是重启后 hello、还是网络恢复后继续领取，都能在一分钟内解除。
- **配置**：服务端配置 `host_lost_after`，时长；不为 0 时至少 10 分钟，因为领取每分钟最多记一次在线、检查也是每分钟一次，更短会把空闲的 runner 误判为失联。

## 10. 人工接管（本阶段不做）

按 D51，人工接管是在 cove 里对同一工作区开终端，接口在 cove M1 合入后设计。

如果希望在 cove 之前先有最小版本，可以做：
- `tend takeover <task>`：控制模式切到 `human`，显示工作区所在的 host 和路径；
- `tend handback <task>`：runner 把工作区现状记为一个产物，任务回到 `managed` 并请求验收。

owner 确认等 cove，本阶段不做。

## 11. 测试

- **scheduler 穷举测试**：
  - `p0` 先行；
  - 分数计算（权重、评级、未评级按 `p2`、等待时间）；
  - 等待时间让低分任务超过高分任务的边界；
  - 同分时验收先于实现，再按等待时间；
  - 资格规则不受排序影响。
- **端到端**：`TestEndToEnd_ProjectsScopeContext`。两个 project、两个会话，经过真实的 `tend hook`、`tend mcp` 和 HTTP。
- **故障场景**：
  - `TestFault_RunnerRestartContinuesWorkerThread`；
  - `TestFault_ThreadResumeFailsFallsBackToBrief`；
  - `TestFault_WorkspaceHostRetired`；
  - `TestFault_WorkspaceHostReturns`。
- **其他**：远程地址规范化的表驱动测试，评级迁移的测试。

## 12. 真机验收（W9）

- 两个 project（tend 自身和一个示例仓库），两个 agent 会话分别在各自的仓库目录里启动。确认上下文按 project 划分，另一 project 的待决事项仍然提醒到。
- 多台 runner。确认排队顺序与分数一致；让一个 host 退役，确认待决事项和 “start over” 的效果。
- runner 在 run 中途重启，确认 worker 续接原 thread，不再从头执行。

## 13. 实施顺序与提交拆分

| 步骤 | commit | 内容 |
| --- | --- | --- |
| 1 | `feat: define projects in the server config` | W1，D52、D58 写入 design.md |
| 2 | `feat: bind sessions to projects and scope their context` | W2、W3，D59（两项的 review 修复交织，合为一个提交） |
| 4 | `feat: rate tasks and remind about unrated ones` | W4，D60 |
| 5 | `feat: order the queue by project weight, rating and wait` | W5 |
| 6 | `feat: show projects and the queue in tend status` | W6 |
| 7 | `feat: continue an interrupted run's worker thread` | W7，D61 |
| 8 | `feat: block tasks whose workspace host is gone` | W8，D62 |
| 9 | `docs: record the phase D acceptance` | W9 |

## 14. owner 的选择（2026-10-21）

1. project 放在服务端配置里（§2）。
2. 会话按 cwd 所在仓库的远程地址自动绑定，绑定不了时显式选择（§3）。
3. 排序按分数：权重 × 评级分 + 等待时间，`p0` 最先（§6）；不采用按 project 份额分配机器的方案。
4. W7、W8 都在本阶段做，实际部署会有多台 runner；人工接管等 cove（§10）。
