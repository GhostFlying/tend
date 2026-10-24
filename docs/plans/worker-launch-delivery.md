# Worker 启动提示投递确认

状态：已实现，定向 race 与完整 make check 通过；初次独立 review 完成，SessionStart 时间边界补充等待增量复核。Refs: D66。

## 问题与范围

Orca 创建终端成功不代表启动 brief 已投递。现有 worker 忽略 `prompt.outcome=not-delivered`，先记录 turn.started，再等待不存在的 agent 状态，失败后又因无状态卡在终端释放。修复只涉及 Tend 的回执解释、持久恢复和已拥有终端的清理，不补发提示、不修改 Orca、不重放未知外部操作。

## 实现

1. 将启动回执的 prompt outcome 与终端 handle/pane/worktree 一起保存到既有 worker thread。明确 `handed-to-terminal` 才记录 turn.started；`not-delivered` 持久保存否定事实并立即失败；缺字段及未知 outcome 保存为 unconfirmed，明确失败并等待对账。
2. 恢复已保存的 not-delivered/unconfirmed 时不再 launch/send，继续原失败/清理路径。旧 journal 未保存 outcome 的历史 worker 仍按既有恢复逻辑读取，不凭字段缺失补造否定事实。
3. 仅明确 not-delivered 可在缺少 agent 状态时清理本次终端：核对原 handle、pane、worktree；当前 agent 有提示/执行活动或身份变化则保留并交还 owner；正常关闭后读回确认消失。清理失败保持原否定事实，重启可重试清理，不能伪造 stopped。
4. 未知投递不获得空终端清理证明。已有能证明原 turn 的正常清理路径保持，未知操作仍不补发。

## 验证

覆盖 implement/accept 两角色：否定回执即时失败且无 turn.started；否定事实跨重启保留且不 launch/send；清理成功/失败后恢复；终端身份改变和 owner 活动保留；未知和缺字段不重发且不按空终端已停止处理。使用 fake Orca 执行真实 Client/Workers/runner 恢复边界，不启动外部模型。定向 race、vet 后执行 make check，提交前由独立 review 复核。

## 已有验证

- 两角色真实 Client/Workers 启动、持久否定事实、无 turn.started、重启不 launch/send、空终端关闭与读回回归通过。
- 真实 runner 边界覆盖启动失败、清理首次失败、不伪造 stopped、journal 关闭重开后复用原 release 请求完成；两角色均通过。
- 当前终端身份改变、owner 提示/工作/held、provider 查询失败时保留终端；unknown/缺字段不重发、不获得空清理证明。
- 取消与否定回执并发时不经旧的直接 stop 路径发送 Esc/close，仍交由有身份和活动检查的 finalization。

## 独立复核补充

Orca 的 SessionStart 也可能呈 working，不能仅按非空 state 判断 owner 活动。否定回执同次持久保存 receipt 时间，仅允许时间不晚于该回执、无 prompt/tool/output/interruption 的启动状态；更新的活动及缺少时间依据的状态继续保留。旧记录不补造时间，重启沿用原回执边界。

## Fencing 复核修复

unconfirmed 不能当作没有 turn：取消、fencing 或退出并发时，复用原 stop 限时范围核对三重终端身份、原 brief 和 turn 时间；仅已证明的原 turn 执行原停止动作。缺状态、owner 活动或身份不符不盲停，恢复不补发。两角色覆盖中断、取消、held、身份变化及重启。
