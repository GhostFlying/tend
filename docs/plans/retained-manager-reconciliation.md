# 受保护 manager 的有证据对账入口

Refs: D169。基于已合入 D159 的 retained 原身份语义，不新增迁移。

## 现存边界

D159 允许无关运行继续注册，但故障维护保留的原 Run 不因此恢复。原 journal、lease、
pin 与业务回执仍属同一原身份。`session_not_observed`、transport EOF、没有本地进程、
lease 过期或 owner 想继续，都不是业务完成或原执行已停止的证据。

现有 FinishManagerTurn 已有一条受控收束：原 manager 的成功 native close 与全部原
业务回执已对账后，结束该次物理运行，Task 保留 waiting 责任，后续只以新 Run 继续。
该系统入口目前没有用户可调用的 typed 诊断、CLI 或主 Agent 对账能力。

## 首批闭环

- 仅覆盖已证明成功 close 的原 manager；不声称原 worker/session 无证据情况下能恢复。
  普通 worker 的未分类 give-up、没有成功 close 的 manager 继续是后续独立边界。
- 提供单 Run 的结构化、只读对账状态：Task/Run/action/host/lease、原 pin、Task CAS、
  close request 身份、事件游标与未处理数、原 native requests 和业务 receipts 的阻塞。
  单对象读取不伪装成完整历史枚举；不暴露 token 或 native 用户内容。
- 对账动作以 Task 为 target，要求原 request_id、expected_control_revision、精确
  Run/lease/close request 和原因。原 close 必须来自该 Run 的真实已完成成功回执；
  其事件尾序号必须仍匹配全部记录且无未应用事件。任何 pending/started/unknown native 操作或 non-rejected 的业务错误回执
  或 hosted receipt 阻塞都维持 unknown，不对旧动作换 ID 重发。
- 只在同一事务重新验证上述证据后复用既有 waiting 收束。新动作不改原 close、原
  stopped/give-up、原请求结果和原 pin，不续旧 lease、不把旧 Run 重新派发、不声称
  Issue/业务任务完成。推进 Task CAS，保留原责任；后续新 Run 仍走正常 admission。
- owner 可显式发起；主 Agent 只在当前实际准入上下文及现有 planner/Task 权限内发起，
  不给 Issue manager 或外部 run 自行结算权限。CLI 诊断/动作/帮助/能力目录和主工具
  同批提供，未知结果继续原完整请求重试或读原 request，不能生成新 ID 猜成功。
- 默认旧请求与 runner wire 不变；无需新 schema 或 runner verdict，不通过改保护标志
  绕过物理停止/业务回执证明。只读状态不可当动作的预授权。

## 验证和未覆盖项

真实 App/lease/native receipt 测试覆盖成功 close→保护仍保留→独立对账→waiting/new Run
承责；close 错误/缺失、错 Run/lease、native unknown、未应用或追加事件、业务 pending、
stale CAS、取消/旧 owner、同 ID payload 冲突及未知重试均拒绝或按原事实回放。
CLI/主工具与真实 handler 的 typed 输入输出、权限与请求恢复必须覆盖。

此批不承诺缺失原 session 的原地继续，不承诺清除所有历史 unknown，不撤销已发生
外部副作用，不在生产停机期间启动任何旧服务或模型。其余原 unknown 恢复仍明确留账。

停止证明必须覆盖当前 native 活动：成功 close 之后若有非确定拒绝的 resume/open/turn/其它有作用请求或再次未确认 close，旧 close 不再足够。只有 models/find/usage 真只读和确定未执行的拒绝不使旧证明失效；不以时间戳相同混淆原记录的追加顺序。

## 实际原生来源依赖

最终分支真实基于 PR234 冻结父55a8815987ed463a29a232fefe2f86979836c058，不复制 helper；共享当前 source gate 保留父操作并集再加入本对账操作。曾准入的历史 owner 来源即使被分类加入当前 logical review list，仍不能授权物理停止对账。实际当前 native 输入可成功，原请求沿相同 ID 回放；这不等于恢复没有关闭证明的原 Run。旧审查通过不能替代这次 shared helper 组合验证。
