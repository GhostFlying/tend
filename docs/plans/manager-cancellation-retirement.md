# Manager 取消式退休

## 范围与依据

沿既有取消责任补齐 owner 的原 manager Run 退休控制。Issue 的取消意图仍是
两阶段：控制请求不能代替真实停止、未知操作对账或最终 `finish_cancel`。
现有 assigned manager 可继续协调 cancelling Issue；现有独立对账命令要求
成功 close，不能表达 owner 要求停止普通协调而由外部接管取消收尾的动作。
本计划唯一实施者负责 manager 退休的协议、app、API 与状态机测试；具名 CLI
及其帮助、能力目录和入口测试由同一候选中的独立文件写入者负责。

## 实施

1. 新增具名 owner `tasks.retire_manager_run` 与原 Task/Run 的只读退休查询。
   写入要求 Task control revision、Issue revision/assignment epoch，以及
   原 Run、host、lease 和 execution pin 摘要完全匹配，只接受 cancelling 的
   当前 manager 责任。沿现有 request_id 去重与单事务入口执行。
2. 在同一事务冻结普通协调准入、标记原 manager 退休、取消其待调度协调动作并
   将原 Run desired 设为 stopped。保原 observed、lease、pin、native 请求与
   未知事实；不创建新的业务 Run，不自行调用 native 或模型。
3. 退休标记阻止后台刷新生成新的普通或自动 receipt-only 协调。已有
   receipt-only 动作也不能越过原 Run 的显式 stop；真正停止仍由原 runner
   及原 harness 完成，原 native stopped/close 回执才可结束物理执行。
4. 只读查询输出当前 CAS 身份、退休 request、物理观察及分页的原 native
   请求 ID/method/status/结果摘要；不暴露原 payload、prompt 或凭据。
   CLI 完成具名查询/写入、完整输入、原信封导出和原请求恢复语义。
5. 独立停止与业务结果继续分开。迟到回执按原身份保存；未 claim 请求可明确
   rejected，已 claim 但结果不明的请求保 unknown。最终取消仍由正式
   `finish_cancel` 检查真实原执行、来源与所有未决责任，不绕过原门。

## 验证

- 复现 assigned manager 在 cancelling 后仍可普通协调；退休后只允许原
  Stop/receipt，不生成普通模型协调或绕过 Stop 的 receipt-only action。
- 覆盖重复 request、旧 Task/Issue revision、epoch、错误 Run/host/lease/pin
  的拒绝，以及控制后 observed/lease/native history 保持原事实。
- 覆盖 pending 的明确拒绝、started 的 unknown、迟到原回执，以及真实
  native_stopped 接纳后才可完成取消的状态机路径。
- 覆盖 owner API 与 CLI 具名入口、分页和未知结果原请求恢复。
- 先运行有界定向 Go 测试、相关 vet、schema/CLI 覆盖与格式检查；本批不运行
  full。完整检查、独立仓库 review、required CI 与部署由后续交付协调安排。

## 兼容与限制

新增 owner 控制和查询，不改变 server/runner wire 字段、旧有效请求或默认
调度语义，无迁移与协议代际变更。现有 runner 已有 desired stop 和历史原
native receipt 通道。没有原 journal、真实停止或原请求证据时继续保持
unknown；传输失败不是空 journal 或停止证明。设计变化登记 D177。
