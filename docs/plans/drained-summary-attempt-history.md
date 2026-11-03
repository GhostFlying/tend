# 已排空托管组件与未结账摘要历史

Refs: D156。

## 已证阻塞

`conversation_summary_attempts.status='started'` 是持久尝试记录，正常路径在摘要函数
退出时以有限 cleanup context 写回终态。写门超时或进程退出可以留下 started；它不是
内存活跃数，retry_at 到期也不会清理它。恢复校验把这一记录无条件当作活动 effect，
即使正式托管组件已报告排空，仍永久拒绝受控替换。

## 最小接受集合变化

仅在旧服务正式 ArchiveStatus 的 HostedDrained 为 true 时，允许一个合法正数的
`summary operations` blocker 作为原样保留的未结账历史。真实排空由前台 workMu、
summaryActive、启动/销毁/活动/维护状态以及后台 manager 全部退出共同观察；prepared
禁止新 idle summary。false 排空时继续拒绝正数摘要，其他 effect、owner、准备 CAS、
host 维护 ACK、领取数、原 Run/lease 保护等条件不变。保留两份库存与原授权快照，
不写 failed、不删除尝试、不补造摘要成功，本次 prepared/prelink 不重试原模型动作；
finish 后普通摘要的 backoff/调度保持原规则，不承诺永久禁止同 snapshot 摘要。

共享校验用于授权、停止前重验及后继启动，不能只改安装侧。后继 prelink 仍真实保持
组件静止、核数据库与 compaction 状态；本改动不使健康函数恒真。

## 验证与交付

定向覆盖真实排空下历史摘要允许且原快照保留、未排空仍拒、坏/重复计数拒、其他
正 effect 拒，以及授权后排空反转时停止前拒绝。独立局部源码复核后构建 clean 本地
验证包，由唯一运营代理开新窗口；正式 PR 检查后补，不把本地验证冒充正式发布。
