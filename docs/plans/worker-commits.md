# 提交归 worker（D57，修订 D39、D41 与阶段 B 计划 §6.5 的完整性检查）

**依据：阶段 C 真机验收（`docs/findings/phase-c-acceptance.md`）与 owner 的分工决定。**

## 问题

阶段 B 的设计是 runner 替 worker 提交：worker 不许 commit，run 结束时 runner 执行 `git add -A` 并以 `tend: <name>` 提交，artifact 就是这个提交。真机验收里这个设计和任务直接冲突：

- 主 Agent 写的任务包要求“交付为可审查的独立 commit”，仓库的 AGENTS.md 也规定了提交格式。worker 不许提交，满足不了；runner 的提交又不符合仓库规范。
- 验收按任务包和仓库规范评判 runner 写的提交，判 `changes_required`；实现 worker 为了满足验收把 runner 的提交 reset 掉，run 以 `git_tampered` 失败。任务在几代之间循环，无法通过。

## 决策（D57）

**提交是 worker 的工作。** 要不要提交、怎么写提交信息、分几个提交，由 worker 按任务包和工作区里各仓库自己的规范决定；主 Agent 通过任务包表达要求，不直接干预提交；tend 不提交，不评判提交，也不内置任何仓库的规范。验收按任务包判断，任务要求提交就检查提交。

tend 仍然负责的只有：给 worker 一个隔离的工作区，run 结束时如实记下工作区的状态作为 artifact，验收和下一代都从这个状态出发。

## 设计

### tend 不限制 worker 怎么用 git

worker 在工作区里怎么用 git 都是它的工作：commit、amend、squash、rebase、切换分支、改 git 配置、建 worktree，tend 都不禁止，也不因此判 run 失败。阶段 B 的完整性检查（`git_tampered`）对实现 run 取消。将来如果需要，可以作为主 Agent 给单个任务设置的限制，现在不做。

tend 只如实记下 run 结束时每个 mount 的状态；mount 无法读取（例如 `.git` 被删除）时 run 失败，原因写明读不到什么。

### artifact

每个 mount 记下：

- `branch`：run 结束时 HEAD 所在的分支，detached 时为空。
- `commit_sha`：run 结束时的 HEAD，即 worker 的提交。
- `snapshot_sha`：工作树有 HEAD 之外的改动（未提交的修改、未跟踪的文件，遵守 `.gitignore`）时，runner 用临时 index 生成的快照，作为一个以 HEAD 为父的 commit 对象保存在 `refs/tend/runs/<run>/<mount>` 下；不移动分支，不改 index，不动工作树。工作树干净时为空。

`changed` 表示这个 mount 的 HEAD 或工作树和上一次记录（第一代则是基线）不同：下一代只是把上一代留下的改动提交上去，文件相同但结果不同，也算变化。所有 mount 都没变化时仍以 `no_changes` 失败。基线照原样记录；worker rebase 到更新的上游后基线不再是祖先，这也只是事实，不影响 artifact。

快照使用本机的 git 身份和固定的时间，同一个 run 重复 Collect 得到同一个对象；比较的对象是这个 run 之前的记录，重复 Collect 结果不变。

### 验收

- Prepare 确认每个 mount 仍是 artifact 记下的状态：分支和 HEAD 与记录一致。这是核对 tend 自己的记录，不是对 worker 的限制；不符时验收以 `workspace_moved` 失败（同 D41）。
- 未提交的文件不核对：prepare profile 会重新生成文件（runner 在同一个验收 run 里还会准备两次），D41 也允许验收留下构建产物，核对工作树只会误报。保证程度和 D57 之前相同。
- 验收说明：评判任务包要求的结果；`git log <base>..HEAD` 是 worker 的提交，`git status` 列出未提交的改动；不修改、不提交、不切分支（同现在）。

### 下一代实现

从上一个 run 留下的工作区继续，提交和未提交的改动都保留，和现在一样。

### 工作区说明（AGENTS.md）

- 实现：删除“不要 commit、切换分支、rebase、建 worktree”；改为“是否提交、如何提交按任务和仓库自己的规范；run 结束时 tend 记下每个 mount 的 HEAD 和工作树，验收和下一代都从这里开始”。
- 撤回 #56 中“任务分支的提交是 tend 的记录”的说明。

### 推送与交付

runner 不推送。任务要求推送或开 MR 时由 worker 完成，使用本机的 git 凭据和工具，这和 tend 无关。

## 实施

1. `protocol.ArtifactMount` 增加 `snapshot_sha`；`commit_sha` 改为表示 HEAD，`branch` 允许为空。旧 artifact 的 `commit_sha` 是 runner 的提交，没有快照，含义不变。artifact 存在 `ref_json` 里，不需要迁移；服务端校验放开空分支并检查快照的格式。
2. `gitws.Collect`：去掉 `git add -A`、commit 和完整性检查（包括 git 配置的指纹），改为记录 HEAD 与快照；去掉“认领自己未记录的提交”的逻辑。下一代的 Prepare 沿用工作区，不检查也不恢复 HEAD。`ErrTampered`（`git_tampered`）改为只用于验收的 `ErrWorkspaceMoved`（`workspace_moved`）。
3. `gitws.layoutAcceptance`：按分支和 HEAD 核对，允许 detached。
4. `harness.Brief` 给实现 worker 的 developer instructions 去掉“不要 commit、切换分支、rebase、建 worktree”，改为由 worker 按任务和仓库规范决定。`gitws` 工作区说明按上面修改；基线一节改为同时适用两个角色：`git log <base>..HEAD` 是提交，`git diff <base>` 是对已跟踪文件的全部改动，`git status` 列出未提交的新文件。
5. `tend task`：artifact 显示 HEAD，detached 和有快照时注明，包括本代没有改动、带着上一代快照的 mount。
6. `design.md` 写入 D57，并在 D39、D41 中注明被修订；阶段 B 计划 §6.5 加注。

## 测试

- `gitws`：worker 提交、改写基线之前的历史、切换分支、detach、改配置和建 worktree 后，artifact 如实记下，验收和下一代都能准备；留有未跟踪的文件时快照含有它们，HEAD 和 index 不变；重复 Collect 得到同一个 artifact；只提交上一代留下的改动也算变化；没有变化时 `no_changes`；验收在分支或 HEAD 不符时失败，profile 重新生成的文件和上一次验收留下的产物不影响验收。
- `TestFault_WorkerTamperedGit` 改为 gitws 的 `TestFault_WorkerRewritesHistory`（worker 怎么用 git 都如实记下）和 e2e 的 `TestFault_WorkspaceMovedBeforeAcceptance`（验收前工作区变了：验收 run 失败，不产生结论）。
