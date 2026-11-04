# 保留普通 Run 的原身份继续

Refs: D159、D142。基线为已冻结的 writer/concurrency 修复候选；本批独立 worktree、唯一 writer，正式合入前重绑实际前置 main。

## 问题与范围

恢复维护登记保护了当时所有未结束 Run。普通执行 Run 即使没有未结原生业务请求，后续 Hello 也只会 retain；现有设计把独立业务对账入口留到后批，导致恢复机制不能自行继续修复自己的 Run。

仅支持命名执行的普通非 manager Run；原 Task/action/spec/generation/host/lease/pin 不变，业务仍 managed/wanted，且当前与冻结原生业务请求为空或明确已结算。独立继续不会关闭 Issue、完成 Task、创建 Run、换 provider、清除旧 unknown、删除维护保护或改 runner journal。manager、暂停/取消、未知请求与身份变化保持拒绝。

## 合同与实现决策

1. 增加只读 `GET /api/v1/runs/{id}/maintenance-reconciliation`，CLI `run maintenance-reconciliation <id>`，主会话与 Issue owner 提供同一受范围约束的读工具。返回原 Task/action/generation/spec/host/role/lease/full execution pin、Task 控制 revision、完整保护集合及其摘要、原请求的已知结算状态、当前 blocker 与既有继续决议；不返回凭据、payload 正文或伪造零值。
2. 增加 typed `runs.continue_retained`，CLI `run continue-retained <id> --revision N --data/--file`，主会话及负责该 Task 的 Issue owner 可经既有身份/来源/作用域边界调用。输入冻结 Task/action/generation/spec/host/lease/full pin、完整 protection-set SHA256 与精确保护身份集合，附独立 reason。request_id 去重和 Task 控制 CAS 在同一 app 事务中校验。
3. 只允许全部原保护已 linked、当前 archive 未 prepared、host normal 且原执行能力/完整 pin 仍支持的情况。查询并核实冻结原请求与当前相关 native/hosted 请求；任何 pending/unknown 或未应用业务结果阻止。空集合必须由账本真实查询得到，不能由调用者声明。不能把 maintenance linkage 当作业务完成。
4. 追加迁移保存独立 continuation resolution，引用精确原 Run/lease、Task revision、完整 pin 与保护集合摘要；旧保护、原请求、物理停止事实永久保留。该动作递增 Task 控制 revision，返回独立决议。相同 request_id 原样重放；新 capture 或 Task/action/pin/lease/control 变化使决议失效，不复用旧授权。
5. Hello 仍严格核原 runner 身份和原 lease。匹配有效独立决议后，复用现有 Hello 将 expired/lapsed 但未 released 的同一 lease 设为 active 并续租，再返回 Continue；决议提交本身不续租、不伪造 running。缺失 journal 不 Reassigned；只允许原 journal 提供合法 interrupted/starting/running/waiting 观测后继续。heartbeat 不能独立消费此授权绕过 Hello。
6. 原物理 release/stop 回执仍按历史保护规则存事实，不因独立继续把旧请求改成业务失败或成功。新的合法完成可按既有合同提交。原 runner 已有 Continue 清 durable fence 与 saved Thread reattach；本批不修改 provider send、root observation 或原生 session 身份。

## 验证与交付

先建立有意义 RED：新只读能力在当前 server 为 404，以及 protected 普通 Run 无继续入口；随后覆盖真实 App/DB 的 retain→独立继续→同 lease Hello Continue，含 expired/lapsed lease。明确保持原请求/unknown/pin/保护原文，重复请求幂等且修改输入冲突。负例覆盖 manager、paused/cancelled、stale CAS、lease/pin/spec/generation/action/host 漂移、不同 protection set、未 linked、prepared/maintenance、pending native/业务结果、新 capture、缺 journal、伪造输入与越 Issue 作用域。

CLI 覆盖 typed 参数、帮助、输出、保存原 envelope、单次 POST 与 unknown 后原 ID 对账，登记能力目录/入口测试；工具读写与 API 使用相同 app 校验。同步设计决策与生成 schema/类型入口；协议 wire 不变，新 owner 能力不改变旧 runner 请求默认语义。

定向检查仅使用已授权 2 CPU、package parallel 1、低优先级，不启动另一套 full/model 或生产巡检。局部验证后可先本地受控候选验证；正式 PR 与本地完整验证/独立 review/required CI 并行，最终门全通过才合入。任何生产继续动作另按精确读回结果交付，不在实现阶段自动执行。
