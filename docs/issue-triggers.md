# Issue 调度、Webhook 与诊断

所有调度在 server 判定。Runner 领取交付查询、执行只读观察并上报；等待不占模型或执行 slot。Webhook 与定时触发均进入既有 Issue 的来源、后台批次和负责人消费路径，不扩大业务授权。

## CLI

`tend issue` 默认显示未关闭 Issue 摘要；`issue show <id>` 先展示目标、当前评估、下一步与执行概况，`--json` 读取完整对象。创建、编辑、暂停、恢复、取消、关闭、重开、子项、依赖、负责人交接、任务合同、来源与完成核对继续使用现有命令。Issue 的选择与人类行动通过 `tend attention` 管理。

```sh
tend issue
tend issue show "$ISSUE_ID" --json
tend issue activity "$ISSUE_ID"
tend issue results "$ISSUE_ID"
tend issue context "$ISSUE_ID"
tend issue schedules "$ISSUE_ID" --json
tend issue events "$ISSUE_ID" --json
tend issue webhooks "$ISSUE_ID" --json

tend schedule create --issue "$ISSUE_ID" --file rule.json
tend schedule list --issue "$ISSUE_ID" --json
tend schedule show "$RULE_ID" --json
tend schedule history "$RULE_ID" --after-revision 0 --after-occurrence ""
tend schedule pause "$RULE_ID" --reason '暂缓核对'
tend schedule resume "$RULE_ID" --reason '恢复核对'
tend schedule update "$RULE_ID" --file rule.json --reason '调整安排'
tend schedule trigger "$RULE_ID" --reason '现在核对一次'
tend schedule cancel "$RULE_ID" --reason '不再需要此安排'

tend observation list --issue "$ISSUE_ID" --json
tend observation refresh --issue "$ISSUE_ID" --delivery "$DELIVERY_ID"
tend event list --issue "$ISSUE_ID" --source webhook --json
tend event trace "$EVENT_ID" --json
tend event sources "$EVENT_ID" --after-source 0 --json
tend event watch --issue "$ISSUE_ID" --json
tend request show "$REQUEST_ID" --json
```

`rule.json` 是 FollowupSpec。例如每天当地 09:00 检查，时区明确、期限由 owner 指定：

现有 Issue 跟进卡片可以展示 cron 表达式、时区及次数；cron 的创建和编辑通过 CLI 完成。

```json
{
  "purpose": "核对已授权交付",
  "instruction": "读取最新 Issue 与交付事实，继续等待或报告真实阻塞",
  "kind": "cron",
  "cron": "0 9 * * *",
  "timezone": "Asia/Shanghai",
  "expires_at": 1893456000000,
  "max_triggers": 20
}
```

截止时间使用 UTC 毫秒，必须仍在未来。cron 仅接受五字段数字语法；支持 `*`、`,`、`-`、`/`，星期 0/7 为周日。不接受六字段秒级或 `@daily`。时区缺失、无效日期和期限前没有下一次机会时拒绝登记。固定间隔 every 仍为时长，不等价于当地每日时间。

写入前输出 request ID 与原 revision/epoch。回执丢失后先查询该 request；重试携带原 ID、revision、epoch 与相同输入。`--dry-run` 可输出原信封供调用方保存，`tend request replay --file original.json` 重放原 owner 信封；不自动刷新旧请求控制版本。CLI 不保存另一份业务账本。外部 webhook 由原签名发送方沿 delivery ID 重试。

## Webhook 配置

在私有 server 配置中启用独立接收入口：

```json
{"webhooks":{"listen":"127.0.0.1:8641"}}
```

省略 webhooks 时不启动外部监听；启用但省略 listen 时默认回环。现有 tailnet owner/runner 入口不变。外部反代只转发 `/hooks/v1/`，负责 HTTPS；本功能不配置域名或公网网关。

owner 准备 32..4096 字节私有 key 文件，用文件或标准输入传入，避免作为命令行参数。配置与回执只保存 key_ref；列表、事件及日志不回显 key。

```sh
tend webhook create --issue "$ISSUE_ID" --secret-file "$KEY_FILE" \
  --data '{"title":"构建结果","instruction":"读取当前 Issue，核对外部结果；事件本身不授权执行"}'
tend webhook list --issue "$ISSUE_ID" --json
tend webhook show "$SUBSCRIPTION_ID" --json
tend webhook update "$SUBSCRIPTION_ID" --file subscription.json --reason '调整检查说明'
tend webhook rotate "$SUBSCRIPTION_ID" --secret-file "$NEW_KEY_FILE" --reason '轮换凭据'
tend webhook pause "$SUBSCRIPTION_ID" --reason '暂停接收后的分发'
tend webhook resume "$SUBSCRIPTION_ID" --reason '恢复新事件分发'
tend webhook receipts "$SUBSCRIPTION_ID" --json
tend webhook cancel "$SUBSCRIPTION_ID" --reason '结束订阅'
```

`subscription.json` 为 title/instruction 元数据，不含 key。修改不改变所属 Issue；换目标应新建订阅。

## 入站合同

`POST /hooks/v1/{subscription_id}` 的 body 为不超过 1 MiB 的 JSON；发送方提供：

- `X-Tend-Delivery`：1..200 字节稳定投递身份；重试沿用。
- `X-Tend-Timestamp`：Unix 秒，接收端允许前后五分钟；重试可重新签名。
- `X-Tend-Signature`：`sha256=` 加 HMAC-SHA256 十六进制摘要。

签名原文为 `subscription_id + "." + timestamp + "." + delivery_length + "." + delivery_id + "." + 原始请求 body`，delivery_length 是 delivery_id 的原始字节数（十进制），用于消除身份与 JSON body 之间的分隔歧义；key 是文件的精确字节。签名与目标订阅绑定验证；复制到另一订阅不能取得权限。轮换后旧 key 立即失效。202 仅证明接收事务成功，不证明 Agent 处理或 Issue 完成；响应包含 event_id 和接收状态。重复同内容返回原回执，不创建新触发；相同 delivery 身份不同内容返回 409。

## 只读诊断

`/diagnostics` 提供调度和事件列表。事件详情关联规则、接收、Issue、批次与请求，显示有证据的处理阶段；Issue 内入口带过滤条件，手机使用全屏详情，返回保留筛选。查询、复制 ID、跳转和筛选均不触发业务操作。历史没有关联记录的阶段不会补造。

Owner API：`GET /api/v1/diagnostics/{schedules|events|webhooks}` 及对应 `/{id}`。列表支持 issue_id、schedule_id（事件精确关联）、source、status、search、after、limit；事件额外支持 since/until（UTC 毫秒）。上限 100，默认 50。事件按实际创建时间及 ID 倒序分页，游标 opaque；调度按稳定 ID 正序分页。读取由 owner 认证，独立 webhook listener 不提供这些接口。
