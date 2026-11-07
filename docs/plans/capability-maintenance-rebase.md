# Capability 维护后整合

本计划先于 rebase 和冲突修订。唯一写入者为本准备任务的实现执行者；独立整 PR 审阅由原 reviewer 另行执行。

## 来源与边界

- 原已验收候选：`52dc69aa12d515e4ec90624d13013ba7dd7f086f`。
- 原实现基线：`bd51f1288aaf1652fe25db138a7701666211d1dc`。
- fetch 核实的维护后 main：`d929e48880c20c8ee1b71e7380349a912549152d`。
- 只更新原 PR 的原 head 分支；本任务不执行 ready、merge、部署或运行配置变更。
- 原实现及两份独立验收报告均保留；旧验收不自动覆盖新候选或充当共享整 PR 审阅。

## 操作

1. 核实工作树干净和远端原 head，保存原候选 bundle、diff 和输入校验清单。
2. 正常 rebase 到维护后 main，逐处记录真实冲突。重叠业务代码仅整合两侧已授权语义，不改通用交付/维护机制。
3. `0098_declarative_capabilities.sql` 与维护 main blob 已相同，保留原文件，不重编号、重写或再加依赖迁移；保留所有 main 已提交迁移。
4. API/CLI 注册及 protocol/view 结构如有冲突，合并原 Capability 与维护交付字段；生成 schema 和 Web 类型由 `make gen` 重建，不手改生成文件。
5. 保留装配 setup25 + recovery25、HTTP2、parent55 和真实断言，保留已验收诊断及测试注入；Sandbox600、fake42990、resolver5 不变。维护已采用阶段预算，不能用冲突选择恢复旧单段预算。
6. 检查维护后 schema 版本对原迁移回归的影响，仅调整依赖精确旧末尾版本的测试，使其检查实际追加历史；不削弱数据、旧格式拒绝和恢复断言。
7. 生成 old→new 提交映射、main..HEAD 完整 diff/tree、迁移及受约束测试完整性证据，执行最终候选完整 `make check`，使用自动 CPU 预算并持久保存原始日志、失败和 skip。
8. 检查远端 main/head，使用精确旧 head 的 force-with-lease 更新原 PR 分支。描述如实绑定最终 head/base/tree、检查及待独立整 PR 审阅状态；保留 draft。
9. 读取一次最终服务端 PR/CI 事实。pending 或不可用如实交付，由 owner durable 观察接续；不持槽等待 CI，不自行提交 formal verdict。

## 实际整合补充

rebase 后编译发现维护新增的 `store.SharedReviewRequirements` 直接比较 `AgentRequirement` 与空结构。
Capability 的声明 slice 使该类型不再可比较。修订前确认采用既有 `AgentRequirement.IsZero()`，
保留维护的空 reviewer 判断，并在现有 shared-review digest 回归中加入相同 reviewer 的 capability
要求变化，证明该变化仍进入实际整 PR 的要求指纹。此处只修复两侧装配，不改变审阅机制或预算。

## 证据

输入、provider 回执、bundle、完整本地检查日志和 manifest 通过任务交付保存。账本、部署进程事实、源码/CI、当前执行和历史 unknown 分开报告。若存在无法归属的 dirty、远端并发改写或真实语义不确定，保留现场并向原 owner handoff。
