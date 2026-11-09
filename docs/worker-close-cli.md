# 本机原执行关闭对账

`tend worker-close` 用于既有关闭范围已遗失的 Linux cgroup 执行。它记录独立的
`external_execution_boundary` 事实，不声称原生 Close 曾成功，也不停止服务、修改
业务数据库、发消息或重跑模型。普通执行仍沿既有自动关闭路径。

必须先在原 runner 精确存活时准备。操作员确认原 unit 覆盖该执行及其后代，使用
已有维护证据中的原 PID、start ticks、boot、Run、thread 和原 owned 记录 SHA：

```sh
tend worker-close prepare \
  --state-dir "$ORIGINAL_STATE_DIR" \
  --run "$ORIGINAL_RUN_ID" --thread "$ORIGINAL_THREAD_ID" \
  --owned-sha256 "$ORIGINAL_OWNED_SHA256" \
  --runner-pid "$ORIGINAL_RUNNER_PID" \
  --runner-start-ticks "$ORIGINAL_RUNNER_START_TICKS" \
  --runner-boot-id "$ORIGINAL_BOOT_ID" \
  --unit "$ORIGINAL_RUNNER_UNIT" --acknowledge-unit-execution \
  --request-id "$CLOSE_REQUEST_ID" --json
```

命令读取原 `owned-appserver-process.json`，校验原 Run/thread/记录 SHA，绑定实际
存活 runner 的身份、cgroup 路径及 kernel 目录 device/inode，保存不可变 intent。
优先使用所选 unit 的非根 unified v2 cgroup。混合布局若统一层只有根路径，则读取
原 runner 的 `name=systemd` 层级，并从 `/proc/self/mountinfo` 定位其实际 cgroup
挂载；不从 unit 名称猜内核路径。原 runner 必须位于所选 unit 本级 cgroup。
该 intent 还保存原执行进程列表、操作员 UID 与明确的 unit 范围确认。

旧 v2 原件没有新增字段，canonical 字节与哈希不变。v1 的 boundary 新增
`hierarchy=systemd_v1` 和 `filesystem_root`，后者是实际挂载路径，mountinfo 的
root 须为 `/`；`root_device/root_inode` 绑定该挂载目录，原 `cgroup_path` 仍是
内核层级中的完整原路径。这些只是本机维护原件，不改变线上协议。

通过既有受控维护路径停止原 unit，在启动任何后继 runner **之前**确认：

```sh
tend worker-close confirm \
  --state-dir "$ORIGINAL_STATE_DIR" --request-id "$CLOSE_REQUEST_ID" --json
```

确认阶段自行进行两次有界内核观察：原 boot 和 cgroup filesystem 仍一致，原 runner、
原 root 及已登记 PID 不活跃且无身份复用。v2 要求原 cgroup 的递归 `populated=0`
且 `cgroup.procs` 为空，或原 kernel 目录确已移除。v1 **只接受原 kernel 目录移除**：
目录仍存在就保持未确认，不依据 `tasks` 或 `cgroup.procs` 快照猜测递归为空；
类型校验也拒绝 v1 的 `empty` 证明。目录身份替换、挂载变化、仍活跃的子 cgroup、
缺失观察或身份冲突均返回错误，不写关闭证明。丢失的历史 PGID
不再被猜成成员 PID，而由已事先绑定的整个执行边界覆盖。

v1 允许目录及其祖先改名，因此原路径消失后还会在已绑定的 filesystem root 内
有界查找原 device/inode。最多检查 4096 个目录，只读目录元数据，不读取 tasks
或进程内容；找到改名后的原目录、读取错误、超时、跨 device 或超过上限都保持
未确认。完整查找未发现原 inode 后，才允许该次 `kernel_removed` 观察。

成功 JSON 包含 `phase=confirmed`、`intent_path`、`intent_sha256`、`proof_path`、
`proof_sha256` 及完整 intent/proof。文件在原 StateDir 独占发布并 fsync：intent
以 request ID 的 SHA 命名；证明为 `owned-external-close-<原 owned SHA>.json`。
原 owned 记录保持原字节。runner 校验精确绑定后记录独立的关闭对账范围，再沿
原 Run 的确认停止与已有结果回收路径推进；证明不替代结果或验收。

操作中断或结果未知时，保留原参数与请求 ID，只读原请求：

```sh
tend worker-close show \
  --state-dir "$ORIGINAL_STATE_DIR" --request-id "$CLOSE_REQUEST_ID" --json
```

`phase=prepared` 表示 intent 已保存但没有确认；`confirmed` 返回原证明。原输入
相同的重复 prepare/confirm 读取同一事实，不覆盖、不换请求。证明是旧边界已空的
历史事实，后继 runner 合法进入同名 unit 不使它失效；另一 attempt 的 owned SHA
不匹配时也不会消费旧证明。损坏的匹配证明保持明确错误。

所有命令支持 `--dry-run` 导出输入，完全不读写执行状态。`--timeout` 默认 10s，
最大 20s。输出为完整 JSON，无筛选或分页。此为本机请求，使用 `worker-close show`
恢复，不能用服务端 request API 代替。非 Linux 可读取已有原件和导出输入，不能
生成 Linux 内核观察。命令不自动重试或延长维护窗口。
