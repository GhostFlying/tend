# 候选目录的声明与执行器状态

## 目标与边界

候选目录当前把 Agent 自身声明当作任务要求，再把空声明无条件显示为
`pending/no_declaration`。普通审阅等未要求额外 capability 的任务因此容易被误导。
本次只修只读目录和具体 Task 的解释；不修改调度选择、Agent 配置、身份 pin、
续接合同、资源准入或首次分配策略。交付到 PR 合入，不部署。

## 实施

1. 在只读候选中分别返回声明状态（declared/empty）和普通执行器观察
   （ready/unavailable/unknown），附可读说明及观察时间。目录的 `current.required`
   为空，按普通执行器观察映射 eligible/pending；不把自己的声明当作任务需求。
   没有足够观察时明确 unknown，不推断空声明使执行器不可用；目录 ready 不承诺
   任意 Task 的 host、slot、租约、workspace 或共享资源准入。
2. 具体 Task 继续读取 exact spec/role 的冻结 required，通过原 scheduler 检查
   category、pin、backend、依赖、容量和资源。补充同样的声明与执行器字段，保留
   `current` 的能力匹配及 `scheduling_reason` 的任务准入解释。去除 legacy 无要求
   情况无条件 eligible 的覆盖，避免把实际不可调度解释成可执行。
3. 同步 CLI 帮助、能力文档和设计决策；输出使用现有完整 JSON 路径；仅增加
   server 只读视图字段，不修改 runner 通信、存储或冻结 hash。
4. 回归覆盖目录空/非空声明与 ready/不可用/未知、筛选/分页；具体 Task 空/非空
   要求与实际 claim 一致，保留故障恢复和原 pin 约束。故障起点是缺少或不可用的
   executor 观察；终点是恢复观察后查询与原 claim 一致，不产生额外副作用。
5. 针对性验证后提交候选、准备 draft PR，完整 make check、独立全 PR 审阅、
   required CI 满足后 fresh-main rebase，正常 rebase merge。报告包含背景、结果、
   实际证据与未部署边界。

## 独立审阅修正

原候选的独立审阅发现两项 P2，原轮次与复现原件保留：

- 接受配置已经明确禁用绑定 runtime 时，目录应返回 unavailable/binding_disabled，
  不能将已有的确定事实降为缺少观察的 unknown。按精确 runtime ID/revision 读取
  接受配置，补充正式注册禁用及重新启用、重新观察的回归。
- 没有具名 inventory 的普通 legacy Task 也可能只是 slot 等待。沿原 scheduler
  用仅恢复 transient readiness/容量的 host 检查恢复性，保持真实 requirements、
  依赖、workspace、原 pin 和续接限制；只把可恢复的普通 legacy 路径显示为 pending，
  不恢复无条件 eligible。补充没有 inventory 的实际 claim 阻塞/恢复及永久要求不符回归。

旧候选未结束的本地检查已按精确私有进程范围停止并保留，不能算通过。
修正后自测、完整验证及同一 reviewer Task 复核最终候选；不另建审阅责任。
