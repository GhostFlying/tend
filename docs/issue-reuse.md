# 显式接纳新输入到既有 Issue

当新 owner 输入明确继续一个已有业务责任时，Main 可先读取 `issues_reuse_status`，再用 `issues_reuse` 指定该 Issue，而不是按标题猜测重复或创建替代 Issue。缺少可核对的原创建回执时返回 `observation_unavailable`；原创建结果未知必须按其原 request_id 对账。

CLI 读取 `tend issue reuse-status ISSUE_ID --json` 获得真实 `original_request_id` 与 Issue revision/assignment epoch。再读取 owner 消息的 content revision 和 association revision，将完整 typed payload 通过 `tend issue reuse ISSUE_ID --revision REVISION --epoch EPOCH --request-id REQUEST_ID --file reuse.json` 提交：

```json
{
  "original_request_id": "ORIGINAL_CREATE_REQUEST_ID",
  "source_message_id": "OWNER_MESSAGE_ID",
  "expected_source_revision": 1,
  "expected_association_revision": 0,
  "reason": "The owner explicitly continues this original responsibility"
}
```

原 ID 重试保留所有原始输入和 revision，也可保存完整 Envelope 后用 `tend issue replay --file envelope.json`。新输入另用独立 request_id。接口 `GET /api/v1/issues/{id}/reuse-status` 与 `issues.reuse` 对应相同合同。

复用只接纳精确的新来源，保留原计划、owner、assignment epoch、已终态任务和交付责任。新材料仍须原责任方复查；它不会隐式完成复查、创建新执行或重开 closed/cancelled Issue。Main 只可转交当前执行已准入的真实 owner 来源，不可把历史消息复用为新的授权。该能力不自动折叠历史 Issue，也不禁止用户确实需要的两个相似责任。
