# 阶段 E 验收结论

计划 §7（E7）的真机验收：两台 host 改为 orca 承载的 TUI worker，Codex 作为 worker，跑通实现、验收、人工接管和 runner 重启，再把一台改回 app-server worker。

## 环境

- 两台真实的 Linux 开发机，各运行一个 runner（1 个 slot），都通过 tsnet 加入 tailnet；服务端和其中一台 runner 在同一台机器上。
- 两台都运行 orca 1.4.229；worker 是 Codex TUI（两台的 Codex CLI 版本不同，一台有更新可用），使用 Codex 的默认模型。owner 的 Chat UI 设置下，两台的 `orca agent launch` 结果都是终端。
- 一个验收用的 project，仓库是每台机器上的一个本地 bare 仓库（内容相同的很小的 Go 模块）和它的 clone；clone 在两台的 orca 里登记，在 Codex 里信任。任务要求在任务分支上提交、不推送。
- 任务直接调用 `tend mcp` 创建；owner 的接管用 orca CLI 向 worker 的终端发一句话来模拟，与在 orca 界面里输入相同。

## 结果

| 项 | 结果 |
| --- | --- |
| 集成测试 `TestIntegration_OrcaWorkers` | 两台都通过（修复 F1、F2 之后）：实现和验收各由 orca 里的一个 Codex 完成，验收结论经 runner 的 MCP 桥交回 |
| 只接受单仓库任务 | 通过：两台都是 orca 时，多仓库的 `tasks_create` 被拒绝，说明没有活动 host 的 worker 支持多个 mount；主 Agent 的快照在 Limits 一节写明 |
| 经 orca 完成任务 | 通过：任务的 worktree 出现在 owner 的 orca 里，分支是 orca 的命名（`<用户名>/<工作区名>`），产物记录这个分支；实现、验收、completed 约两分钟 |
| 人工接管与交还 | 通过：worker 工作时 owner 在它的终端里追加一个要求，约 40 秒后 tend 察觉（等一轮结束的检查每分钟一次），任务转为 `human_control`，主 Agent 的上下文把 `task.taken_over` 列在需要处理的一节；worker 没有发 Esc，agent 照常做 owner 的要求。owner 的一轮结束后 `tend resume`，worker 发交还的一轮，agent 检查工作区后确认完成，提交里包含 owner 追加的改动，验收通过。发现 F3 |
| runner 中途重启 | 通过：SIGTERM 时 worker 发 Esc，agent 停在中断状态，终端保留；重新启动约一分钟后 run 回到 running，worker 接上同一个终端，发“执行被中断”的续接说明，没有重新启动 agent；任务完成并通过验收 |
| 启动回执丢失不重复启动 | 真机上没有制造这种情况，由 `TestFault_OrcaLaunchReplyLost`、`TestFault_RunnerStoppedMidLaunch` 等覆盖 |
| 一台改回 app-server | 通过：多仓库任务又可以建，只由 app-server 的 host 领取，工作区按 runner 自建的布局（`tend/<工作区名>` 分支）；产物记录未改动的仓库为 unchanged。第一次的任务 spec 引用了不存在的文件，worker 如实说明无法完成、没有改动，run 以 `no_changes` 失败并开出待决事项，取消后重建的任务通过。切换 worker 时发现 F4 |
| §5 待核对的 orca 行为 | Esc 让正在工作的 agent 停在 done 且 interrupted；`terminal wait` 超时的错误码是 `timeout`（超过一分钟的轮次被当作仍在工作）；`worktree show` 按路径返回 worktree；agent 状态取 working、done |

## 发现并已修复的缺陷

- **F1**：一台机器的 Codex 配置关掉了 hook（`[features] hooks = false`）。orca 从 agent 的 hook 得知状态，于是一直没有这个 agent：终端在运行，worker 却判断不了一轮何时结束，一直等到集成测试超时。现在终端还在、orca 持续 `Settle`（默认两分钟）没有 agent 的状态时，worker 以指明 hook 的错误失败；部署说明列出这项前提（#86）。owner 在这台机器上打开了 hook。
- **F2**：有更新可用时，Codex TUI 启动后先弹出更新提示。orca 把 worker 的提示输入到终端，而不是作为命令行参数传入，Codex 不会因此跳过更新提示，agent 停在那里，一轮始终开始不了。tend 为 worker 写的项目级配置现在设置 `check_for_update_on_startup = false`（Codex 和 traex 都从项目级配置读取它），F1 的错误也提示检查终端里的提示（#87）。
- **F3**：worker 发出 `worker.taken_over` 后紧接着报告接管，runner 随即停下这次尝试并停止上传事件，这几条事件留在 journal 里，直到 owner 交还后的下一次尝试才上传：run 的日志在交还之后才出现接管。现在尝试停下时先把待上传的事件传完（有时限，runner 退出时跳过）（#88）。
- **F4**：把 host 的 worker 从 app-server 换成 orca（或反过来）后，runner 启动时重发 journal 里换之前的领取请求，被 tend 以 worker 不符拒绝；runner 删掉了这个请求却仍然退出，要再启动一次才正常。现在被拒绝的旧领取请求记一条警告后跳过（#88）。

## 其他观察

- orca 在每个 worktree 里另外打开一个 shell 终端，与 agent 的终端并列；tend 只操作自己启动的终端。
- runner 比较 orca 仓库的 origin 与登记的远端时按字符串比较，两者只差一个符号链接时也会记一条警告，不影响工作。
- 察觉接管的延迟取决于等一轮结束的检查周期（`WaitChunk`，默认一分钟）；owner 在 orca 里的操作在这段时间内不受 tend 干扰。
- traex 作为 orca 里的 worker 本次没有在真机上验收（owner 确认 orca 的上游已修复它的目录信任问题，计划 §5）；traex 的 app-server worker 在改回 app-server 的 host 上照常工作。

## 结束状态

- 修复 F1–F4 后，两台 host 都运行 orca worker，又完成了一个任务；切回 orca 时 runner 对换之前的领取请求只记警告、继续运行（F4 的修复）。
- owner 的另一个仓库没有在 orca 里登记，orca 的 worker 要求每个仓库都有登记，所以暂时不由这两台 host 服务；需要时在 orca 里登记后加回 runner 配置。
