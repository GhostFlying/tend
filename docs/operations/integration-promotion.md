# 集成与主干提升

仓库管理者先合入快检和提升工具，再从该版本的 `main` 创建 `integration`。
为精确匹配 `refs/heads/integration` 的活动 ruleset 启用禁止删除和强推、线性历史、
必须经 PR 且只允许 rebase merge。必需检查选 GitHub Actions 的
`integration-check`，关闭「合入前必须与目标分支保持最新」。核对 `main` 的
strict `check`。开启 intake 前用试点 PR 确认规则与实际检查一致。

普通 PR 显式指定 `gh pr create --base integration`，用
`CHECK_BASE=origin/integration make check` 和针对该分支的独立 review。
先确认实际 head、必需检查及合入授权，再以 GitHub rebase & merge 合入。
如果 GitHub 拒绝合并有冲突的候选，解决冲突并重新检查。

## 维护者操作

从可信的仓库 checkout 执行，`origin` 指向 GitHub，Git 使用维护者现有身份。
有 3 个待提升 PR 或首个等待满 15 分钟时运行：

```sh
python3 .github/scripts/promote-integration.py prepare
```

命令冻结按合入顺序排列的最多 3 个 PR，重放到当前 main。它只维护一个
`promote/integration` 分支。若上次推送完成而创建 PR 结果未知，先查询现有
PR，再尝试完成同一候选。保留原 PR，直到明确知道此前操作结果。其余
`integration` PR 可在全量运行期间继续合入。

全量 CI 和独立审阅通过且已有授权时，使用真实 PR 编号：

```sh
python3 .github/scripts/promote-integration.py merge 123
```

命令用当前 head 提交合入，回读实际 main 提交和 tree；重新运行同一编号可对账。
GitHub 合入后的 main push 另跑完整 CI，发布以其 SHA 为准。发布从
[受控升级](controlled-upgrades.md#发布与执行) 流程进入单独授权的安装。

## 恢复

- 若 main 前移，将原候选重放到新基线并更新同一 PR。旧检查保留为历史事实，
  新 head 等新的完整 CI。若 main 紧急修复与某个历史集成提交冲突，在
  `integration` 合入明确兼容两边的修复，再用 `prepare --reconcile --through 456`
  提升修复 PR 456 及之前的连续范围。有待更新的提升 PR 时同时传 `--replace 123`。
  此模式按整个已修复范围的净差异重放，收据仍保留每个来源 PR。完整 CI 复算该差异。
- 若全量发现可复现回归，暂停正常接收 PR，先在 `integration` 合入修复或 revert。
  维护者用 `prepare --replace 123 --through 456` 保留提升 PR 123 并把截止点
  移到修复 PR 456，期间所有已接受的改动都进入新候选。仅更新 main 基线时省去
  `--through`。GitHub lease 防止覆盖其他人推进的 head。
- 若提升 PR 已合入但远端提升分支保留，命令核对 main 收据后用原 head 租约复用分支。
  若 PR 已关闭且未合入，先查原 PR 和远端 ref，再处置已明确放弃的旧提升分支。
- 若需要暂停集成试点，停止接收新 integration PR，保留待提升范围和收据，
  后续改动可经 main 的完整检查。已有 PR 修改目标分支后推送新 head，触发
  新目标的检查。修改 PR 标题和描述会保留当前的 CI。
