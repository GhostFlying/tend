# 已失效 manager 的数据库收据回放

基线：`ddb175dea20f06689ec043c385bddc2673dba148`。Refs: D169、D155、D156。

## 问题与边界

原 manager 的 lease 失效后，active-only 选择器不再建立 attachment。即使原 close 已在 runner 完成，留在数据库中的回复 delta 也没有消费者，关闭回执因此仍被 applied guard 拒绝。D169 的真实持久化前缀确认不应要求重启物理会话或恢复原 lease。

本次只增加原责任的数据库回放路径：原 closing、pending business receipt、task/run/lease、冻结 pin/epoch、thread 与已确认 resume 必须匹配；只允许 lapsed 原 lease。新业务、模型目录查询、原生 claim、物理 resume、close、find 以及新 Run 均不由此路径创建。原业务未知、维护保护和晚到回执权限保持不变。

## 实施顺序

1. 用故障替身复现 lapsed、无 models 记录、原 cached resume 和三条未确认 delta 的入口缺失。在 BackgroundPool 的普通 claim 之前识别严格绑定原责任的只读候选；不放宽普通 claimed/selection/rebind 的 active 判定。
2. 在同一读取快照固定本次既有回复 delta 前缀，直接用本地 attachment 消费并复用 D169 保存/确认。此分支不创建 harness Session 或 RunnerSession，因此 Models、Find 及所有 native enqueue 都不可达；只在实际消费完成后调用终态正文保存及精确前缀确认。不制造 native 末事件或扩展原 close 游标。有非回复事件或原来源不完整时保持 unknown。
3. 固定读取包含待确认 item 已 applied 的原 delta 前缀，重建完整正文。数据库回放记录在原写事务中额外要求现有可见或后台正文是本次文本的前缀，不能用 suffix 覆写旧 streaming 内容；普通原生终结处理维持默认语义。回归真实消费、空/错误身份、缺少原 metadata、正文保存失败/不匹配、边界外新事件、普通 active 路径，以及原晚到 close/stopped 和未决业务不被伪结清。
4. 独立审查后的必要边界：数据库回放仍计入 pool drain，但不充当 live interrupt 目标；维护 gate 读取与注册持同一 pool 锁。允许原 interrupted 正文仅按原身份追加已读取前缀；已 completed 等终态不可替换。精确持久化证据可来自未发布的 background buffer，不结清其 batch 或业务回执。已发布的 interrupted background 前缀扩展必须同步更新原可见正文，不能只更新 published buffer；静默处置及 completed 等终态保持原规则。
5. 保持无 wire/schema/公开能力变化；若内部接口需要补充，仅用于本地读取生命周期，不放宽 claim 或 receipt authorization。

## 验证和交付

保留稳定 RED，再运行定向 race 与相关回归。独立 Sol/xhigh review 必须绑定实际主干基线，不能使用共享的旧 main ref。允许真实 PR 与 GitHub CI、本地唯一完整 make check 并行；最终候选的全部必需检查通过后才线性合入。生产由唯一 operator 受控交付，并独立核验原请求身份、原 delta 确认、晚到 close/stopped 与仍未解决的业务未知；单测或版本部署不等于业务恢复。
