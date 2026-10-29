# Canonical Issue callers：A 批闭环

在最终 P5/P8/P6/P2e 候选链加 r9 test-only overlay 的隔离副本实施。无 tracked、手写 Web、部署、真实服务/数据库操作。依据 D121–D126 与公开入口收口清单，A 仅补缺失 caller；旧页所需活动入口留待 B 统一退役，不新增永久双模式开关。

## 实际交付

- owner API `/issues/{id}/context`、`/followups`、`/followups/{rule}/history`、`/deliveries`，新 typed 公共投影使用 issue_id；复用原 coherent revision、facts/source分页、rule/history/occurrence、delivery observation。现 /issues 列表保持，named 增 issues_list 和同组读取；main 可全局读，manager 读自身/后代，写仍只自身，咨询不加权限。
- owner `issues.followups_create/update/pause/resume/cancel/trigger` 与 `issues.refresh_delivery`，用当前认证、原 request_id、CAS、assignment epoch；manager 经现 execution/effect/epoch wrapper。复用 followup 内部处理与 observer，不删除旧入口 canonical guard，不新增 timer、provider 或授权证明。deadline-owned规则识别现 followup_targets deadline 关系，仅 deadline API 可修改。
- followup paused/terminal/unknown、原触发预算/receipt/restart/handoff保持；canonical唤醒沿既有消息/Issue关联和background batch，真正核验当前manager收件。手动delivery刷新沿原generation/nonce/age与 runner poll/report，不引入 native执行权或改变原run/spec。
- CLI issue context/followups/followup-history/deliveries/refresh-delivery + issue followup create/control，复用 stdlib/protocol HTTP与原请求重试，不要求 owner 提交额外来源证明。
- 先分离现 FollowupCommand 的内部规则处理，显式已核实 canonical Issue 参数仅供新入口，旧路径继续围栏；不是全局移除旧保护。公开context/followup/delivery投影不改变存储JSON。

## 兼容与验证

runner wire1/0、deliveries_poll/report及其 work_item_id 字段、worker spec/历史数据保持原字节语义。owner/tool只加新入口；旧入口本批不删，不需无证据升代。用原delivery runner HTTP兼容测试和实际旧decoder形状核对，不把Go命名当wire证据。

真实owner HTTP/CLI与pooled manager工具、source/context/child read与own write负控、followup完整控制/history/真实唤醒、deadline规则拒绝generic修改、原request replay/CAS、refresh→poll→report→canonical读、历史工作者消息不变。只跑差异所需race/vet/staticcheck/schema/types；不fullcheck。最后冻结patch/manifest供独立review。

## r2 独立评审修正

首轮实际评审仅一项缺口：条件订阅只查旧stage link，canonical无stage primary task被拒。现在canonical条件成员查询以现task_work_item_owners为准，legacy仍查原link，不制造stage或转任务责任。真实owner create-task/no-stage后tasks_completed经过实际实施/独立验收触发，decision_answered经同任务Attention原回答触发，外项任务/决定继续拒绝；先保留旧查询red再定向green。r1代码/manifest保持。
