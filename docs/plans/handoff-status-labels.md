# 预备摘要的请求与覆盖状态展示

Refs: D81、D119。

## 已有事实与范围

当前 HandoffStatus 的 preparing 表示内存 summaryActive；pending 同时包含主会话消息、
当前活动与维护投递。pending 不能证明 provider 在排队，也不能证明正在生成新摘要。
summary 独立表示最近已保存摘要，日期不能与当前未决请求拼成同一次 attempt。

1. 仅以 preparing 表示正在生成；未决消息、已提交但未观测请求、维护失败与读取失败分别
   表达，不根据未知原因猜测限额、超时或 provider 队列。保留现有请求恢复与按钮准入规则。
2. 最近摘要明确标注已保存日期及覆盖状态，不把旧日期当新请求生成时间。普通 pending
   不遮蔽可确认的最近维护失败，也不隐藏旧摘要事实。
3. 通过有意义的状态组合回归与真实构建组件验证 pending+旧摘要、preparing、failed、
   unknown、读取不可用及 ready/stale，保留请求身份/权限/失败重试原行为。
4. 仅修改 Web 与回归，现有 CLI/API 字段不变。历史交接总数、时长与 source/target/attempt
   的进一步账本读取由独立后端范围处理，本变更不制造这些事实或宣称完整历史可观测。
