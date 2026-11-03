# 原问题补充附件

D171 把原 owner 问题答复中的指定附件交给同一个原 worker；不改变已接受的 answer、原 spec、执行 pin 或业务责任。

先用 `tend question-inputs show <decision-id> --json` 读取 `revision`、`question_version` 与 `original` 中的 Run/lease。`original` 只是原身份事实，不证明执行器仍存活或权限仍适用。

```sh
tend question-inputs grant <decision-id> --revision 0 \
  --request-id <new-request-id> --file grant.json --json
```

`grant.json` 完整示例：

```json
{
  "version": 2,
  "run_id": "<original-run-id>",
  "lease_id": "<original-lease-id>",
  "source_message_id": "<owner-answer-or-reply-id>",
  "expected_source_revision": 1,
  "file_ids": ["<exact-file-id>"],
  "reason": "Owner supplied this original file for the question"
}
```

来源必须是原问题真正的 owner 答复消息，或明确 ReplyTo 问题通知的 owner 补充消息。只选 `file_ids`；同消息的其它附件不会被开放。返回 grant revision、精确文件名称、大小、SHA256、来源与原执行 scope。主 Agent 的 `question_inputs_grant/revoke` 必须在真实准入执行内引用 owner 来源，不能把其他 Agent 的说明当 owner 授权，也不另造一轮泛确认。`question_inputs_read` 读取相同 inventory。

将返回的 file_id 和元数据沿现有合法通知/manager relay 交给原执行者，使用既有 `download_file`。runner 先保留 assignment 的 spec/产物权限；对原问题的新附件，按同一个 Run 凭据重新读取 question 元数据并校验下载 SHA。server 在每次文件读取时复核 grant 未撤销及原责任仍适用。这里不保证旧执行器收到主动 steer 或自动注入，也不会重启它来接文件。

```sh
tend question-inputs revoke <decision-id> --revision 1 \
  --request-id <new-request-id> \
  --data '{"grant_revision":1,"reason":"Stop future fetches"}' --json
```

请求结果未知时，用原 request_id、revision 与完整 payload 对账重试，不能换 ID 或修改已接受答案。撤销追加事实，旧 grant/answer 的成功回执不改；后续 fetch 被拒，但已下载字节和独立 spec.inputs 权限不被抹去。

本批只支持原 implementation/acceptance Run 的有效 lease。停止前驱、continuation、新代次、其它 Run 和角色不隐式继承；后继需要自己的合法新问题或新的明确 spec 输入授权，不能用本 API 给同一个旧问题换 RunID。
