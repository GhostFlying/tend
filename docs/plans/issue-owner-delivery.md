# P1b3、P1b4 与 P1c：owner Issue API/CLI 完整交付

状态：正式组合已纳入，固定 base `7b0edfb4ce7d340a4f2e26546d4e5d0608de92e5`；原各候选预审及正式 r1 定向检查通过。r1 规定 CLI review 两项 P2 已采纳修复，r1 完整检查主动停止未完成；r2 已正式应用，精确红绿、选定 race/vet、独立增量复核及最终完整 make check 均通过。CI/合并/部署未完成。Refs: D121、D123、D124、D126。

## 一次交付的真实行为

本片组合已审 primary/no-stage 读模型、canonical task 实现/独立验收/owner 核对闭环和 owner API/CLI。用户通过 `tend issue` 或 owner HTTP API 创建/查看/编辑递归 Issue、按精确 revision/assignment epoch 控制生命周期、处置来源份额、创建无 stage 或显式 stage task、刷新冻结任务合同、核对正式完成并关闭 Issue。task/run/artifact/spec/action 的独立状态机和未知结果对账沿原合同，不能把 Issue 结束代替正式验收。

b3 的 primary 身份使用现有 task_work_item_owners；secondary result/stage 引用不授控制范围。b4 对 primary Issue/祖先状态、显式完成依赖、真正来源及冻结继承语义进行新 claim 校验，不用任意 revision 阻挡现有合同。活跃 run 仍按冻结合同回执；完成必须经过原实现产物及独立验收。owner 负责的 completion 持久待核对，不启用旧 background manager。

OwnerTaskSpec 是允许 owner 输入的字段投影，app 显式映射现有 domain.Spec，不搬迁 runner/domain 表示，不允许输入 server-frozen shared_context/文件元数据。合同刷新委托现有 UpdateIntent，同事务检查 task control revision 与 primary Issue 生命周期/epoch。

## 集成顺序及兼容

1. 先纳入 b3 的实际 15 文件差异。其未提交候选 migration 0055 重定为 0056；已提交 0055 Agent inventory 保持不变，不填空迁移或重写历史。
2. 纳入 b4 r2 的实际 19 文件差异，保留之前正式 b2b r2 的 terminal late-share 修正与最新 P2 行为。原 r1 的两项已采纳问题（completion review 被误作 no-op、祖先 Intent/Background 更新后新 claim 未要求明确刷新）使用已独立复核的 r2。
3. 纳入 P1c 21 文件差异。与 P2 共用的 API ops/routes、CLI 分派、protocol APITypes/APIEnums 做语义合并，重新生成 schema 和 TS；不整文件覆盖较新的 P2。旧 work-item facade/history 保留，canonical 身份不自动采用旧 ledger。
4. 检查组合真实消费者，执行必要定向 race/vet、schema roots 与生成类型检查、真实 CLI→HTTP 路径；冻结相对固定 base 的完整 patch 和文件 hash，交独立规定 CLI review 与唯一完整 make check。

新的 `issues.*` owner API/CLI 只开放已实现行为，不注册 assign、manager 工具或 main 执行入口。CLI 自动请求 ID 在发送前可见；transport 重试沿同 Envelope，手工恢复使用原 ID/原 CAS/原 JSON 或显式导出的原 Envelope，不自动换 revision，不默认写第二请求磁盘账本。来源是 trusted owner 显式引用资料，保留真实 author；不变成 agent 权限、不承接共享 main turn 全部执行责任。

P2 Category/安全库存、本机配置与原请求 journal 行为保持；本片不启用新 Agent factory、命名 worker 选择（P3）或 issue manager runtime（P4）。无可见 UI，唯一 Web 差异来自真实 schema 生成；不访问真实数据、模型或部署。

## 预审与最终验收边界

b3/b4/P1c 各自的冻结 candidate hash 和定向测试保留作来源证据，不能代替本次正式组合检查。组合后核对 P2 无关代码字节和原 schema 定义，确认唯一 schema 旧字段增量为可选 current_issue，新增 owner Issue 根类型与命令。正式验证结果及实际固定 artifact 在完成后补充；review、make check、CI、合并和 live E2E 逐项记录，不提前标为完成。

## 正式组合定向证据

选定 API/CLI、canonical task/primary/context、late terminal-share、混合依赖图、原 owner/work-item/request 去重与 P2 Category/hello 兼容的 race 检查退出 0：API 21.550s、CLI 1.074s、app 94.135s、store 13.320s、domain 1.032s、contextbuilder 1.075s、tools 3.459s、schema 1.057s；views 编译通过（该 pattern 无测试）。相关九包 vet 通过。schema 实际生成、TS typecheck、类型生成器测试与生成文件格式检查均通过。原 186 个 schema 定义全部保留，已有定义只给 APIResponse 增加可选 current_issue；新增 30 个定义。真实 CLI 丢响应/原身份恢复、owner HTTP worker 实现/独立验收/核对以及 P2 Category CLI/API 均包含在上述定向检查。此证据不代替后续唯一完整 make check 与规定 CLI 独立 review。

## r1 正式评审与 r2 最小修复

r1 规定 CLI review 正常退出，发现两项实际 P2，均采纳：issues.update 新增活动 task 到已完成 stage 缺少既有准入规则；canonical task 在 legacy 项中的次级成果引用仍被关闭和等待图消费者当作责任。r1 完整检查因此主动停止、未完成，不记为通过或测试失败。

r2 仅对新 (stage_id, task_id) 的非终态绑定复用 IssueStageAcceptsWork，已有绑定与终态成果保留。legacy 关闭使用既有责任枚举；等待图移除 canonical 跨 primary 的证据任务，保留候选新 legacy 共享绑定和真正 task dependencies，不改变持久成果引用或调度规则。实际 App 红例分别重现错误成功、错误关闭阻塞和虚假等待环；最终相同测试在原 r1 生产实现仍为红，最小修复后普通定向检查通过。新增对照确认真实 legacy 共享关闭责任/等待环、已有阶段绑定与终态成果不变。r2 选定 race 通过（app 54.403s），app vet 通过；已正式应用，独立增量复核与最终完整检查均通过。

主 Agent 转录已有 owner decision/version 选项的原路径有意保留：真实 owner 原文与原 AskedRevision/领域规则约束既定效果，不等同自主规划 canonical task，不增加权限或来源语义证明层。

旧关闭枚举与当前 helper 在实际 legacy 入口保持一致：BindIssueTask 强制既有且非空 stage；SaveIssue 追加历史 links，而 ValidateIssue 禁止删除 stage/task 历史。已有 shared-task、移除历史拒绝、真实等待环与关闭 blocker 回归均在 r2 选定 race 覆盖；不为手改数据库或不可达缺失 stage 构造 fallback。

## r2 正式收口证据

正式应用的 59 个文件精确等于原 r1 加已复核的 11 文件修复；其余文件未变化。独立 GPT-6.1-Sol xhigh 增量复核通过，并分别从 r1+delta 和完整 r2 patch 重建全部 59 个文件。r1 规定 CLI 两项 findings 均有真实红绿修正，本轮没有为局部修复重复整次 CLI。

最终 `make check` 退出 0，UTC 21:50:37 至 22:29:44；app race 2294.295s、mainthread 392.847s，Web 类型、lint、格式、测试和构建通过。完整日志 SHA-256 `5b7473966c254806082d5cbde8469637140db4bafc5b426cddd9ecc853531fd6`；完整实现 patch SHA-256 `94f9cbf123e8cffd9e05c8e4bd653458023ef99c0343c3cecb6b6fbfd769ad37`，正式 manifest SHA-256 `4f4589bac9f3c7f703c4fe9caa07f61dbe7b91a4bac4c66411f109d52a3a61bc`。检查后仅更新上述进度文档，实际实现文件保持原冻结字节。CI、合并与部署不由本地通过状态替代。
