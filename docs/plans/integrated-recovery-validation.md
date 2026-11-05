# 恢复修复的最终集成与验证

Refs: D159、D174、D175、D149。

## 范围与来源

本批将四个尚未合入的修复保留为原子提交，在一个最终 PR 上完成实际完整本地检查、整栈独立 review 和 required CI。分项候选的定向验证、review 与失败记录保留，不将最终组合的检查称为各前缀候选的独立完整检查。

冻结共同基线为 `8caed77e2fab1b2bbd01831601b1a39be4099ef1`。来源如下；整合时保留来源映射，并核对实际提交数与补丁内容。

| 来源 PR | 冻结 head | 来源范围 | 自有提交数 |
| --- | --- | --- | --- |
| #247 | `3db286a22005d72c6cd93821bd3741443a92841a` | 原身份的 retained ordinary worker 继续、严格附着、authoritative stop 与结算 | 9 |
| #249 | `2a938b61edcb3f02c9690e8ebf1eb5a4b0c810d4` | 显式 typed decision continuation、legacy 答复兼容、callback 身份与依赖环拒绝；已包含上述 #247 | 6 |
| #248 | `cf52f82243453462787754fcb659b6acdc60879e` | TraeX 原 rollout 的只读终态事实补证，隔离于生命周期与恢复授权 | 5 |
| #250 | `9d1734fa6818556e39f82b546f46038435cae480` | 检查预算、显式串行理由、阶段时间与使用约定 | 2 |

## 整合步骤

1. 从冻结 #249 建立独立集成分支，保留其 #247/#249 原始提交；先提交本计划。
2. 按原顺序纳入 #248 的五个自有提交，再纳入 #250 的两个自有提交。逐层记录原提交到新提交、stable patch ID、range-diff 与路径摘要。产品冲突须由原作者确认；文档合并保留全部已确认决策，禁止按单方版本覆盖。
3. 仅做必要的源码布局、迁移连续性、生成输入与格式轻验。现有唯一完整检查尚未终态时，不写它的缓存、不启动第二套 full；现有分项 review 不被打断。
4. 创建最终集成 draft PR，说明四项最终行为与完整门槛尚未完成。分项 PR 保持 open；实际最终集成合入后再标明替代关系。

## 最终候选验收

- 新 head/tree/base 冻结后，由协调者在现有完整检查自然结束并完成资源实际交接后，分配一次最终集成完整本地检查。CI 与最终整栈规定 review 可并行；旧 review 只证明原范围，不自动覆盖新组合。
- 使用实际 CPU 预算与新的官方检查入口。八 CPU 下 jobs=2、App/suite 各两片、child=2、package=1、E2E 独占；不改变测试集合、原超时、原请求/lease/pin 与业务时钟。
- `TEST_COUNT=0` 省略 Go 的 `-count`，不跳过测试；跨工作树和此前显式 count=1 不保证结果缓存命中，不承诺完整检查耗时。
- 每次启动保存完整身份、有效参数、阶段起止和逐包结果，终态记录真实退出/异常与源码绑定；失败与 unknown 不改写，未执行的阶段不记通过。
- 本地完整、整栈独立 review、required CI 与 fresh main/head/base/protection 全部满足后，使用 expected-head rebase & merge；实际 main tree 必须等于最终测试树。不直接 push main，不 squash 或生成 merge commit。
- 正式更新由独立受控交付与真实 worker 安全门决定，不因 PR 标签或合入自动重启服务。

## 整合记录

四项来源共22个自有提交：#247与#249的15个原提交直接保留，#248的5个与#250的2个按原顺序重放。另有本批实施计划与整合记录提交。文档冲突将 D174 与 D175 两段全部保留；D159 的原严格继续边界不改。

`recovery.go` 两次上下文冲突已获两位来源作者确认：中间版本先保留 retained-resume guard，再保留当时的 transcript-supplement guard；D175 原后继最终撤销后者。最终 `recovery.go`、`client.go`、`historical.go`、`root_turn.go` 全字节等于整合前 #249；`worker.go` 相对 #249 仅切换原观测记录调用到事实补证 helper。该 helper 不改变生命周期 Agent 或恢复/完成授权。中间历史提交不是发布候选。

后续验收只绑定最终集成 head/tree；来源候选原有CI失败、review及未完成full均不改写。本记录不声称完整检查或最终整栈review已完成。
