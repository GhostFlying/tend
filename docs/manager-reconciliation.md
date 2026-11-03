# 原 manager 的有证据收束

未知并不证明原会话不存在，也不授权开始另一条 native turn。先读取原 Task/Run 的
账本状态：

```sh
tend task manager-reconciliation TASK --run RUN --json
```

返回真实的 Task revision、assignment epoch、原 Run/lease/pin、成功 close request
与其原结果 SHA、事件尾序号、未应用事件数、未决 native request 数和原业务回执状态。
`can_settle_waiting` 只是当前快照，写入时会在同一事务重新核验。缺少成功 close、事件
未应用、native/业务结果尚未知时均保持阻塞，不能根据 session_not_observed 猜停止。

当原 successful close 与所有业务回执均已对账，可提交独立动作：

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

成功只将这次已有证明的 manager 运行收束为 waiting 责任，并释放其旧 lease；不代表
Issue 已完成，不改原产物、native 回执或 pin。后续工作仍经正常 admission 领取新 Run，
不能沿旧 Run 重跑。若响应未知或网络超时，保留完整原 JSON、revision 和 request ID，
先查询原 request，再用同一请求续账。原业务回执尚未对账的 `unknown_outcome` 不发生
收束；回执真正归账后仍可用该原请求继续。新业务意图才使用新 request ID。

此能力不接收手写“进程已停止”的布尔值，不处理缺少成功 close 的普通 worker/give-up，
不对外部副作用的未知结果伪造成功，也不取消现有任务以掩盖对账缺口。
