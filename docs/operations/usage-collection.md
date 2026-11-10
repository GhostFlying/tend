# 用量采集来源的退役与恢复

Refs: D195、D207、D224。适用于包含本次来源生命周期实现的部署。

采集状态与用量覆盖分别显示。`retired/unavailable` 表示停止自动读取、已有用量仍不完整，
不表示已收齐；`retired/empty` 需要系统证明从未发起执行，不产生虚构的零用量样本。
`needs_reconcile` 保留停止未知。新来源拿到原生路径不代表文件已落盘，也不自动启动轮询。

```sh
tend usage collection list --state retired --reason unavailable --limit 50 --json
tend usage collection list --host "$HOST_ID" --after "$CURSOR" --limit 50 --json
tend usage collection show "$SOURCE_ID" --json
```

列表支持 `--host`、`--run`、`--conversation`、`--thread`、`--state`、`--reason`、
`--after`、`--limit`；`next` 与 `more` 用于稳定分页。show 返回当前采集代次、revision、
writer 停止与执行证据、读取预算、游标文件数及总偏移、待确认上传和最近管理指令。
本地文件路径不会通过 owner HTTP 或 runner 的状态上报暴露。

来源读取失败按二至三十秒退避，连续五分钟没有有效进展后退出自动读取。
正常执行暂无新记录不消耗该预算；有完整分页数据继续排空。重启或重复事件不会续期。
采集退役不删除任何用量事实、游标或未确认请求，已有上传沿原 Envelope 对账。

明确结束某个来源的自动读取：

```sh
tend usage collection retire "$SOURCE_ID" --revision "$CONTROL_REVISION" \
  --reason unavailable --request-id "$REQUEST_ID" --json
```

从 show 读取 `control_revision`，以原值写入；版本变化时重新检查，不自动覆盖。
不能通过参数声明 `empty` 或 `complete`。writer 未证明停止时，该命令只能转为
`needs_reconcile`，不会停止模型、释放业务租约或制造关闭证明。

查明历史路径已恢复后，可显式重新开启一次受限读取：

```sh
tend usage collection resume "$SOURCE_ID" --revision "$CONTROL_REVISION" \
  --request-id "$REQUEST_ID" --json
```

当前 CLI 的 resume 显式发送 `rescan=true`：增加采集代次，复用原归属与样本去重键，清空读取游标并从头进行有界重扫，不启动模型、不重放业务请求。未确认上传保留原 Envelope，先完成其确认，再提交新重扫批次；同一原生响应重复读入不会重复计费。

远端完整重扫需要 runner 协议 3。旧 API 请求未指定 `rescan` 时仍保留游标，按旧协议继续读取，不能将此结果称作完整重扫；需要显式保留该行为时使用 `--data '{"rescan":false}'`。请求完整重扫而 runner 仍为协议 2 时明确拒绝，先完成受控升级再使用新请求执行重扫。原未知请求应先查询和对账，不改变其输入。
仍不可读时再次受有限预算约束。需要先检查补采内容时，继续使用现有 `usage preview`、
`usage reconcile`、`usage reconcile-run` 或 host 范围的 preview/upload 入口。

命令支持 `--dry-run` 导出原 Envelope。网络失败或回执未知时保留原 request ID、
source ID、revision、动作和 payload；查询原请求结果或按原输入重试，不另开恢复代次。

runner 来源的管理先返回 `pending`。只有实际 journal 应用并收到持久化回执后，show 的
指令状态才变为 `applied`；`stale` 表示本机采集状态已前移，原指令没有生效。
离线或未协商采集能力时，不能把中央期望当作本机结果。原注册和上传确认继续独立处理。

退役后不进行周期全历史巡检。若需要重新检查，使用上述显式恢复，或等待该线程真正
再次执行所产生的新生命周期事实。不能仅凭日志不再出现判断已收齐，应核对状态原因、
coverage、未确认上传与原执行事实。

`unobserved` 表示尚无采集观察，不能沿历史 coverage 断言已收齐。未确认上传和原生记录冲突分别保持待确认或需对账状态；停止 writer、到达文件尾和计量完整是不同事实。
