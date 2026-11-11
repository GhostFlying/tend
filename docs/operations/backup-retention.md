# 备份与封存诊断日志治理

本机入口由已有 updater control socket 提供，CLI 使用同一套配置根与进程所有权。
所有命令都支持 `--json`；不会通过任意目录递归删除来执行回收。

```sh
tend updater backup status --socket "$UPDATER_SOCKET" --json
tend updater backup preview --socket "$UPDATER_SOCKET" --json > preview.json
tend updater backup verify --socket "$UPDATER_SOCKET" --file verification.json --json
tend updater backup protect-log --socket "$UPDATER_SOCKET" --file protection.json --json
tend updater backup prune --socket "$UPDATER_SOCKET" --file preview.json --json
```

`status` 包含当前 preview 与原回收批次进度。`preview` 输出完整的保留、可删、
受保护及未知清单和原因。传 `--file policy.json` 可使用明确的保留策略；默认至少
两个已恢复验证的检查点、七个日备份与四个周备份。同一检查点满足多个时间档时
只保留一份。未知现场和显式保护优先，超过数量预算仍会保留并说明原因。

`verify` 输入包含 `checkpoint_id`、`expected_generation`、`manifest_sha256`，
可带 `class`、`incompatible_schema`、`observation_completed_ms` 和
`protected_reason`。原始文件逐项校验通过只表示字节完整，仍不宣称可恢复。
完成实际恢复演练后，提供 `restore_proof_path` 与 `restore_proof_sha256`；证明须
绑定同一检查点与 manifest，记录 `format: 1`、`restored: true` 和真实
`verified_at_ms`。旧备份没有来源元数据时，证明还须提供实际来源的
`source_artifact_sha256`、`source_manifest_sha256`、`source_release_id`、
`source_protocol` 与 `source_storage_version`。验证保留原来源身份，不将当前
主机的后续版本冒充旧快照的来源。

迁移锚点须完成恢复证明及观察窗口，不兼容 schema 的锚点至少保留三十天。
成功演练工作副本默认二十四小时后可回收；已解释的失败副本默认七天后回收，
必须以明确证明关联另一份已验证的恢复点及独有证据；`unique_evidence_path` 与
`unique_evidence_sha256` 指定真实证据原件，验证后单独持久保存一次。紧凑的演练证明单独保留。
unknown/争议现场不能靠设置过期时间解除。

`prune` 使用原 preview 的摘要、实例身份、generation 与恢复点。新批次执行前
重新计算；变化后需重新 preview。执行先保存实际删除意图及进度，再移除清单内
已校验的文件。中断时继续使用**原 preview**，先读 status，不重新生成一个批次
冒充原操作完成。原件来自固定数据根的文件 manifest；共享不可变对象只存一次，
所有保留备份的引用共同保护对象。在线原件 GC 还必须并入在线 DB、未结动作与
暂存写入的根。原件 pack 不重排，offset 不变。

新受控 launch 按真实 launch ID 形成日志段。普通升级/重启、受控 runner 替换、
前向恢复和维护升级共用同一底层 launch 实现。stdout/stderr 继承写入锁，主进程
退出但后代仍持有写 FD 时仍受保护。只有原 owned Wait 事实和全部写 FD 已关闭
同时成立，日志段才可封存处理；不依赖全局 `/proc` FD 可见性。已封存日志默认
七天后 gzip、三十天后删除。gzip 必须先持久化并核验解压后的完整内容，再移除
原文件。运行中的日志 inode 不重写、不截断、不压缩或删除；旧常规日志缺少
完整代次和 FD 所有权证明时继续列为受保护，不自动操作。

调查保护输入为 `log_id`、`expected_generation`、`reason`。空 reason 表示明确
解除该保护。`status`/`preview` 给出各代日志的实际路径；可安全维护的当前路径
只是指向本代文件的 symlink，旧 regular 日志保持原状。

文件长度、分配块数与可测独占物理空间分别表达。当前不宣称已测得 COW 独占
字节；硬链接、共享对象或同盘备份不是独立灾备故障域。独立灾备仍须实际内容
位于独立故障域。

runner 大事件、未确认命令与重试正文通过持久 spool 引用，读取时验证长度与
hash。ACK 后的传输副本进入回收候选；未知操作、停止事实、租约、水位和冻结
现场仍受保护。回收只遍历该 journal 的 spool，不删除原生 CLI 管理的 rollout。
旧非只读 completed 记录缺少独立的 server ACK 位，迁移先保留其唯一结果；后续
历史回收须核实真实服务端完成事实，不能从 Run 结束或缺少重试记录推断 ACK。

protocol 3 的清单登记兼容响应中，`not_recorded` 只表示没有通用传输请求回执，
`Current`/`Projection` 始终返回真实清单。旧客户端重送同一登记时，服务端先按
主机、local revision、snapshot 和 public hash 读取实际不可变登记，再校验新版本
的 CAS，不追加重复版本。protocol 4 直接以原业务键查询匹配的历史版本，不依赖
请求参数 hash；实际新 incarnation 的 Hello 才能封住未登记的旧进程。
