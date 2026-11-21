# 仓库集成分支与批次提升

Refs: D265。

普通 PR 经本地受影响验证和独立审阅后，从 GitHub 以线性 rebase merge 进入
`integration`。原 PR head 可落后于目标，只要 GitHub 仍允许合入且构建和 P0
通过。GitHub ruleset 和仓库文档决定何时更新分支；Tend 的通用 Issue Owner
指令只要求遵循实际目标规则、候选事实及授权。这个流程适用于普通 PR，
与所属 Issue 是否需要部署无关。

1. 给 `integration` 加单作业的必需检查，直接运行 Web 和 Go 构建以及一次
   runner 领取、执行、验收的 E2E race。给 `main` 的 PR 强制完整影响计划，
   保留 push 的完整检查和实际提交 SHA 对应的发布。
2. 首版用 CLI 操作 GitHub 上唯一的 `promote/integration` 分支。累计 3 个 PR，
   或首个待提升 PR 等满 15 分钟，从连续集成历史冻结最多 3 个 PR。逐提交
   cherry-pick 到当前 main，在独立的收据提交中记录上一截止点、此次截止点、
   原始 PR 及合入 SHA、main 基线。分支名称用于幂等对账，创建或更新带远端
   head 租约。重复调用查询同一 PR，遇到未知结果先查询事实。
3. main 的必需 CI 校验提升 PR 的收据和重放后的 tree。CLI 合入前绑定实际 head，
   合入后通过 GitHub PR 的 merge SHA 与 main 上的提交树核对。下一批只认
   已核对的 main 收据。集成分支可以继续合入。main 前移或确认回归时，维护者
   明确更新同一提升 PR；回归修复或 revert 先进入 integration，按完整的连续
   范围重试。若 main 紧急修复使历史提交无法逐一重放，先在 integration
   修复冲突的最终组合，再显式按整个范围的净差异重放并由 CI 复算。
4. 先用临时 Git 历史覆盖合批、两次连续提升、收据不匹配、冲突和结果不明。
   上线时先合工作流，再从启用点创建 integration 并加独立 ruleset。用真实
   GitHub PR 验证落后可合入、冲突或快检失败不可合入；用两批连续提升和一次
   回归恢复验证 main SHA、发布和集成并行。试点统计至少 20 个原 PR，
   验收活跃时段主干吞吐达到原先两倍且快检 P90 不超过 12 分钟。

提升过程只使用 Git、GitHub CLI 和 Python 标准库。main 继续保护线性历史、
禁止直推和强推、要求最新基线和完整 CI；发布和生产安装仍是独立动作。
