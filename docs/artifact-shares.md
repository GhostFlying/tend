# 跨 Issue 复用原 artifact

共享授予一个精确 artifact 及其已提交文件的只读权限。目标是当前 Issue 的责任代次，
不是源 Task 的控制权；不能共享任意目录或未提交上传。原报告和验收保持原出处，
不复制正文生成伪来源，也不因另一个 Issue 需要它而重做调查。

先用 `tend artifact show ARTIFACT --json` 读取原 `ref_sha256` 和文件 manifest；
用 `tend issue show ISSUE --json` 读取目标 revision/assignment_epoch。新的授权：

```sh
tend issue share-artifact ISSUE --revision REV --epoch EPOCH \
  --request-id REQUEST --data '{"artifact_id":"ARTIFACT","ref_sha256":"SHA256","reason":"复用原已验收报告"}' \
  --dry-run > original-envelope.json
tend issue replay --file original-envelope.json --json
```

占位符必须替换成真实读取的身份。保存完整原 Envelope；HTTP 结果未知先用
`tend issue request REQUEST --json` 查询，再按原 ID、原 revision、原 epoch、原 payload
重放，不能把新 revision 填入旧请求。主 Agent 工具 `artifact_shares_grant` 还必须引用
本 turn 已准入、未撤回的 owner 原消息及其精确内容版本；Issue manager 不可自授权。

```sh
tend artifact-share list ISSUE --status active --limit 50 --json
tend artifact-share list ISSUE --after CURSOR --limit 50 --json
tend artifact-share show SHARE --json
```

`status` 是原授权状态，`applicable` 是当前代次下的适用性。换代后历史 active grant
不能授权新负责人；需要新的显式动作。当前 manager 可用 `artifact_shares_list` 发现
原 artifact ID，沿 `artifacts_read` 读取原报告，沿 `download_file` 读取其精确原文件。
源 Task、其它 artifact 和 Issue 上下文继续使用各自原权限。

撤销使用当前目标 Issue revision/epoch 以及 share 自身 revision：

```sh
tend issue revoke-artifact-share ISSUE --revision REV --epoch EPOCH \
  --request-id NEW_REQUEST \
  --data '{"share_id":"SHARE","expected_share_revision":1,"reason":"结束未来读取授权"}' \
  --dry-run > revoke-envelope.json
tend issue replay --file revoke-envelope.json --json
```

撤销阻止后续新读取，保留授权、撤销和原成功请求的历史。已经下载的字节不能收回；
已经合法冻结到 worker spec 的输入继续按原 spec 可读，撤销不改写历史执行合同。
