# Agent 技术等待与已停止业务责任的显式续行

## 问题

负责人设置的技术等待，在条件解除后仍要求新的 owner 消息，负责人与主协调
Agent 都无法自行结束自己设置的等待。管理会话恢复又会推进 Issue assignment
epoch；即使负责人、运行身份和业务任务均未变化，原已停止 handoff 也会被旧
epoch 拒绝。仅放开 resume 入口仍会在后继 claim 时失败。

## 实施范围

1. 区分实际 owner 等待与负责人自行设置的技术等待。仅对已保存 Agent actor
   和 Agent 答复来源的原等待，允许当前已准入的负责协调者显式 resume；真实
   owner 来源、未知或旧来源仍沿用 owner 恢复路径。保持原答案和等待历史。
2. resume 增加可选 `expected_assignment_epoch` 与 `reason`。只有明确提供当前
   epoch 才承接已停止业务责任；不根据同名 Agent 自动放行，也不回写旧合同。
3. 在同一事务追加原 Task/spec/control、decision/continuation、原已停止 Run、
   当前 Issue epoch、Agent、冻结 execution pin、请求与理由的承接事实。仅支持
   原命名执行者与当前负责人完整执行身份保持一致的恢复。活动、unknown、
   未停止原运行以及真实执行者/配置变化继续拒绝，不以新 Task/spec 绕行。
4. 当前业务责任投影供 Task 准入、pin 保留及后继 claim 共用。旧 Run 责任和
   原持久合同不改写；只有该精确 stopped predecessor 可据承接事实续行，旧
   live worker 的 epoch 校验保持严格。后继 Run 写入当前责任，仍唯一生成。
5. 同步 named tool schema/说明、owner CLI 帮助与能力覆盖、查询投影、生成
   schema/types 及 D209。无新 runner 请求或必填字段，无 wire/journal 升级；
   追加存储迁移，旧有效 resume 和 owner 等待语义保持。

## 验证与交付

- 先复现同 Agent 管理恢复后原 stopped handoff 的 owner resume/claim 失败。
- 覆盖正式持久承接、唯一后继、最终真实结果与原 Task/spec/history 保留；覆盖
  actor/source 分界、当前协调作用域、CAS、错误/缺失 epoch、pin 变化、原运行
  未停、unknown 和旧 live Run 仍被拒绝。保留原 owner pause 的严格语义。
- 运行受影响定向与 race 检查、最终候选完整 make check、规定的独立 review
  和 CI；从最新 main 线性合入。完整发布集受控部署后，以正式入口恢复原
  decision，核验同一业务 Task 实际执行，再按真实证据结算原 Action。
- 不在本修复处理一般用量性能、跨 Agent 任意任务转移或证据分享范围问题。
