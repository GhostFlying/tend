# 已提交 server 的本机受控维护

正常升级与原窗口恢复都不能用于绕过未知业务 run 的边界。此入口以新的受控窗口替换
已提交 server，保留 run/Task/pin/thread 和全部历史。当前实现限定 Linux systemd 用户
simple service；原 unit、参数、私有配置、原锁和留存 child 身份必须完整匹配。

## 私有计划

计划包含 `maintenance`、`unit`、`updater_binary`、`config_file`、`candidate` 和旧 updater
退出行为证据文件及其 hash。`maintenance` 中提供独立 maintenance/request/link 身份，
候选 `release_dir` 与 successor process 身份、操作者 UID、限时窗口和资源交接文件 hash。
`preparation_revision` 是准备后的预期修订；另提供固定 `preparation_request_id`、
`preparation_cancel_request_id` 及完整 `host_controls`。各 host 保存原 `revision`、
`previous_mode`、独立 `request_id` 和 `restore_request_id`；未知主机不可省略。
所有请求 ID 不同。计划观察补齐 state、旧 process、配置、unit、产物与 journal 链 hash。

```sh
tend updater maintenance plan --installer "$REVIEWED_UPDATER" \
  --installer-sha256 "$REVIEWED_SHA256" --config "$UPDATER_CONFIG" \
  --plan "$PRIVATE_DRAFT" --json
# 将输出 plan 完整保存为 PRIVATE_PLAN 后，后续只使用该固定计划。
tend updater maintenance apply --installer "$REVIEWED_UPDATER" \
  --installer-sha256 "$REVIEWED_SHA256" --config "$UPDATER_CONFIG" \
  --plan "$PRIVATE_PLAN" --json
tend updater maintenance status --installer "$REVIEWED_UPDATER" \
  --installer-sha256 "$REVIEWED_SHA256" --config "$UPDATER_CONFIG" --json
```

第一跳只换 updater。apply 返回非零且 phase 为 `starting_adopt` 时结果是未知；保持原 ID，
先 status 对账。只有确证中断且需要新动作时，使用同计划、独立 `--resume-request-id`
和 status 的 journal `--expected-generation` 执行 apply；原请求重放不重新执行副作用。
status 同时提供 updater durable state，业务阶段使用其中的 state generation。

## 准备、授权与取消

```sh
tend updater maintenance prepare --socket "$UPDATER_SOCKET" \
  --plan "$PRIVATE_PLAN" --expected-generation "$STATE_GENERATION" --json
tend updater maintenance authorize --socket "$UPDATER_SOCKET" \
  --plan "$PRIVATE_PLAN" --expected-generation "$STATE_GENERATION" --json
```

prepare 建立本窗口的 archive/host CAS。authorize 实读旧 server 已有 archive status；
HostedDrained 为 false、owner 在途、host 未确认维护、集合或修订变化时绝不停止旧 server。
持续阻塞可在旧 server 尚未停止时执行 `maintenance cancel`，参数同上；它仅恢复自身
请求和 CAS，不取消任何业务 run，也不改别人后来设置的维护。过期允许安全取消，不续授权。

## 提交、独立读回与释放

候选通过两次健康及独立数据库 link 后，status 显示 `ledger_linked`。独立读取：

```sh
tend updater maintenance ledger --socket "$SERVER_CONTROL_SOCKET" \
  --maintenance-id "$MAINTENANCE_ID" --json
tend updater maintenance health --socket "$SERVER_CONTROL_SOCKET" \
  --plan "$PRIVATE_PLAN" --json
tend updater maintenance finish --socket "$UPDATER_SOCKET" \
  --plan "$PRIVATE_PLAN" --expected-generation "$STATE_GENERATION" --json
```

finish 只在精确 linked 收据后释放本窗口准备及 host 维护 CAS。若别人改过修订会拒绝，
不能覆盖。旧收据与 journal 不删除；下一版本建立新的窗口、请求与候选，延续 journal 链。

## 本窗口候选失败后的前滚

仅限此机制已持久进入不可逆 `checking_successor`，或已经两次健康提交但尚未 link 的
`successor_committed` 父窗口。新计划提供 `parent_maintenance_id`，新独立 maintenance、
register/link request 及候选 artifact；plan 钉父完整计划/hash、generation、真实 failed
process、原已生效 archive/host 授权快照 hash 和完整当前 state。旧失败、receipt 与 proof
保留，不能把它们伪装成 linked 或已健康。

子窗口继承父的准备修订、prepare/cancel 请求和 host set/restore 请求责任，不向不可用
candidate 查询新 API。兼容范围必须覆盖 Current 与父 candidate 可能已迁移到的版本，
只允许前滚。`failed_process_exited` 只有实际 PID/start 与 launch 全扫描证实退出时才有效，
不能靠布尔值免核验。仍在运行的候选必须精确匹配且没有第二 writer。新 installer 保原锁，
子授权凭新鲜本机资源交接和父持久授权启动修正版；后续两次健康和独立 DBlink 流程相同。

已被子接管的旧父不能再分叉。此分支不授权任意旧升级、改写 D154 原窗口或绕过正常升级
规则；子尚未完成时不允许 cancel 释放父仍持有的 admission。成功后 finish 才释放继承 CAS。

## 旧进程已退出、候选尚未启动时的续进

如果明确得到 `ErrNotStarted`，phase 记为 `launch_not_started`，不把它伪装为有进程的
unknown。`authorized` 或 `stopping_old` 若中断后实际原进程已退出，以及
`old_exited`、`backed_up` 或这一确定未启动阶段若授权过期，可修复本机启动
条件，然后对同一固定计划提交新的限时资源授权：

```sh
tend updater maintenance resume --socket "$UPDATER_SOCKET" \
  --plan "$PRIVATE_PLAN" --expected-generation "$STATE_GENERATION" \
  --request-id "$CONTINUATION_REQUEST_ID" --expires-at-ms "$FRESH_EXPIRY_MS" \
  --window-claim "$FRESH_WINDOW" --resource-evidence "$FRESH_RESOURCE_FILE" \
  --resource-sha256 "$FRESH_RESOURCE_SHA256" --json
```

新请求固定新的 generation、期限与资源文件 hash；真实核验旧进程退出、候选 launch 不存在，
并保留原 archive/host 授权及同一 candidate。它不依赖已停止服务的 API，不改原计划、不
更换候选字节。相同 continuation 请求重放只返回既有事实；真正含糊的启动结果仍只沿原
launch 查证，不能借 resume 再起一次。候选可能已接单时只能走限定父窗口前滚。

完整原 state 只在本窗口不可变 journal 保存；持久维护记录用 SHA 引用，避免状态及历史
递归复制。父数据库已 link 而 updater 确认丢失时，新子窗口还钉精确 `parent_receipt`，
保留父 Linked 的真实成功事实，子窗口仍独立 fence、提交和 link。
