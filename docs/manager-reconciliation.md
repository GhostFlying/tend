# 原 manager 的有证据收束

未知并不证明原会话不存在，也不授权开始另一条 native turn。先读取原 Task/Run 的
账本状态：

```sh
tend task manager-reconciliation TASK --run RUN --json
```

返回真实的 Task revision、assignment epoch、原 Run/lease/pin、成功 close request
与其原结果 SHA、独立 stopped request 与其 payload SHA、两项证明的事件尾序号、
未应用事件数、未决 native request 数和原业务回执状态。
`can_settle_waiting` 只是当前快照，写入时会在同一事务重新核验。缺少成功 close、事件
未应用、native/业务结果尚未知时均保持阻塞，不能根据 session_not_observed 猜停止。

当原 successful close 与所有业务回执均已对账，省略 `--mode` 可提交原有的收束动作：

```sh
tend task reconcile-manager TASK --revision REVISION --request-id REQUEST \
  --run RUN --lease LEASE --close-request CLOSE_REQUEST \
  --close-sha256 CLOSE_RESULT_SHA256 --assignment-epoch EPOCH \
  --reason 'Reconcile the original proved-close manager' --json
```

也可以提交完整原 JSON，不与上述证明字段 flags 混用：

```sh
tend task reconcile-manager TASK --revision REVISION --request-id REQUEST \
  --file reconcile.json --json
```

JSON 必须包含 `run_id`、`lease_id`、`close_request_id`、`close_result_sha256`、
`expected_assignment_epoch`、`reason`。使用读取的原值，不把新 Run、另一个 lease 或
重建的 close 回执替换进去。owner 可直接发起；托管主 Agent 使用
`tasks_manager_reconciliation` 与 `tasks_reconcile_manager`，后者还必须引用实际准入
owner 消息的 `source_message_id` 和 `expected_source_revision`。Issue manager 不能
自行获得此控制权限。

省略模式的成功只将这次已有证明的 manager 运行收束为 waiting 责任，并释放其旧 lease；
不代表 Issue 已完成，不改原产物、native 回执或 pin。后续工作仍经正常 admission 领取
新 Run，不能沿旧 Run 重跑。

若 close 与独立 stopped 均已成功，事件已全部应用、原身份未变，唯一 blocker 是
`business_receipts_unreconciled`，可明确请求读取原业务回执：

```sh
tend task reconcile-manager TASK --revision REVISION --request-id REQUEST \
  --mode read_receipts --run RUN --lease LEASE \
  --close-request CLOSE_REQUEST --close-sha256 CLOSE_RESULT_SHA256 \
  --stopped-request STOPPED_REQUEST --stopped-sha256 STOPPED_PAYLOAD_SHA256 \
  --assignment-epoch EPOCH --reason 'Read the original native receipt' --json
```

`read_receipts` 在同一事务重新核验这些已入账证明。它将已证物理关闭的旧 transport Run
记为 `failed`、释放旧 lease，并保留原业务 pending、logical unknown、原请求和冻结 pin。
此处 `failed` 描述旧 transport 的停止，不是判定原业务失败。成功回执包含
`mode: "read_receipts"`、`responsibility: "receipt_reading"`、原证明身份、更新后的
`task_revision` 与尚待读取的 `pending_request_ids`。CLI 输出完整操作回执；请求成功
不表示后续 reader 已领取或原业务已恢复。

后续仍由现有调度分配 `receipt_only` 新 Run 和新 lease，使用同一冻结身份恢复原 thread；
不续活旧 lease，不启动另一条业务 turn，不迁移 thread 或生成摘要。只有读取到真实原
native terminal receipt 并核验其来源，既有消费链才会清除 pending。原 thread 不可恢复、
缺少原模型信息、历史缺失或仍为非终态时继续保留 unknown，不根据物理停止推导业务成功。

两种模式均支持完整 `--data` 或 `--file`（`--file -` 读取 stdin）。新模式的 JSON 在原必填
字段外增加 `"mode":"read_receipts"`、`stopped_request_id`、`stopped_payload_sha256`。
CLI 保留 JSON 原字段，不替换原证明或自动刷新 revision。可选来源引用也可通过
`--source-message MESSAGE --source-revision SOURCE_REVISION` 表达；这不授予托管主 Agent
额外权限。flags 输入的 `--reason` 同时进入 payload 与 Envelope；完整 JSON 输入下，
`--reason` 仅设置 Envelope reason，JSON 内的 `reason` 保持原值。

发送前加 `--dry-run` 可导出完整 Envelope，使用 `tend request replay --file ENVELOPE_FILE`
重试同一请求。若响应未知或网络超时，保留完整原 JSON、revision 和 request ID，
先查询原 request，再用同一请求续账。默认收束模式因原业务回执尚未对账而返回
`unknown_outcome` 时不发生收束；回执真正归账后仍可用该原请求继续。切换模式或改变
证明属于新动作，应使用新 request ID；不能改变已发请求的 payload 规避未知结果。

此能力不接收手写“进程已停止”的布尔值，不处理缺少成功 close 的普通 worker/give-up，
不对外部副作用的未知结果伪造成功，也不取消现有任务以掩盖对账缺口。

新 `read_receipts` 模式要求原 host 已报告 `runner-manager-receipt-reader-v1`。其新 action 的领取及新 Run 的恢复也要求相同能力；先升级真实 runner，再读回诊断并提交原证明。旧 runner 仍可执行原有普通模式，不能被新模式用于探活或创建临时 thread。
