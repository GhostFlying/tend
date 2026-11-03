# 飞书文本 channel

Refs: D162。

## 范围与激活

飞书 adapter 默认关闭。配置 `feishu` 后，服务端仅登记 `configured_unverified`，不把本机配置当作飞书鉴权或发送成功。配置示例中的环境变量必须由部署的私有 secret 来源注入；不把值写入配置或仓库。

```json
{
  "feishu": {
    "app_id": "application-id",
    "tenant_key": "tenant-id",
    "app_secret_env": "TEND_FEISHU_APP_SECRET",
    "verification_token_env": "TEND_FEISHU_VERIFICATION_TOKEN",
    "encrypt_key_env": "TEND_FEISHU_ENCRYPT_KEY",
    "callback_listen": "localhost:8081"
  }
}
```

回调为 `POST /hooks/feishu`。可选监听只提供经签名鉴权的回调，必须由部署方提供 HTTPS 入口；它不开放 owner API。与已有 webhook 使用同一监听时，地址必须一致。飞书应用需启用消息事件订阅、机器人消息收发权限，并完成租户授权与回调 challenge。实际 app、tenant、owner open_id、私聊 chat_id、回调可达性和权限是联网激活的前置；离线替身通过不代表这些条件已满足。

只接受绑定 owner 的私聊文本，不根据消息自报内容创建权限。签名校验使用收到的原始 body，之后才解密与解析；校验签名时间、verification token、app、tenant、sender 与 chat。URL challenge 不创建业务消息。附件、群聊和交互卡片未实现，明确拒绝而不冒充已收取；owner 可在 Web/CLI 处理附件。

## CLI 与请求恢复

```sh
tend channel feishu show --json
tend channel feishu bind --app-id application-id --tenant-key tenant-id \
  --sender-id owner-open-id --chat-id private-chat-id --revision 1 \
  --request-id ORIGINAL_REQUEST_ID --reason 'approved owner destination' --dry-run
# 检查并保留 dry-run 输出的完整 Envelope 后，使用相同参数去掉 --dry-run。
tend channel feishu inputs --after 0 --limit 100 --status pending --json
tend channel feishu unbind --revision 2 --request-id NEW_REQUEST_ID --json
```

新绑定操作可省略 revision/request-id，让 CLI 读取当前 revision 并生成请求身份；结果不确定后的重试必须保留原完整 payload、request-id、revision、reason，先查询原请求而不获取新 CAS 或更换 ID。`inputs` 返回 event/message/request 关联、绑定 revision、内部 message ID 和业务状态，不返回消息正文或凭据。按 `more` 与 `next` 分页；支持 pending、accepted、unknown、succeeded、failed 筛选。`channel list` 也报告未决收件数。

provider event/message 身份先持久化一个稳定 request-id；业务消息、事件、outbox 和 request record 在同一事务提交。重启恢复只处理尚无业务 request record 的持久收件；accepted/unknown 不作为缺失请求重放。原消息引用通过已知 provider receipt 解析到内部 ReplyTo/Issue 路由，不能通过任意 Issue ID 绕过绑定。已知父输入仍暂存时，保留子输入及原请求身份，等待父事务提交后恢复，不记录永久业务失败。

发送冻结 app、tenant、chat、绑定 revision 和稳定 delivery UUID。回复固定原输入的 owner、chat 和绑定 revision；绑定更改或撤销不向新地址补发旧消息。明确的 429/限流错误码拒绝按受限 x-ogw-ratelimit-reset（兼容 Retry-After） 重试同一 outbox/UUID；网络断开、5xx、缺失或坏 receipt 保持 unknown，没有凭空的 UUID 反查能力，因此 reconcile 不重送。发送前 token 获取的网络/限流/5xx 瞬态失败只重试原 outbox 身份，尚未调用消息发送接口；明确永久鉴权拒绝才失败。成功回执的 opaque message ID 经 owner receipts/trace/CLI 和 Web 展示，成功仅表明 provider 接受消息，不代表用户已读或模型已处理。

本实现使用应用自建 tenant access token，只在内存缓存且不自动重发消息；不新增 OAuth 共享认证。回调/发送测试均使用本地 HTTP 替身，不向真实飞书发送消息。
