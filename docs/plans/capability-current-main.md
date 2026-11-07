# 声明式 Capability 当前主线整合计划

唯一写入者：本 Task 的指定 implementer。实现与 formal-review 独立。

## 来源、优先级与起点

本次任务起点及只读远端 main 核验均为
`bd51f1288aaf1652fe25db138a7701666211d1dc`，工作树干净。
历史源码来源为 PR244 的
`2ae5b26ca715c27365cb4a351d6c098ea6432eb7`，共同基线
`e07b809823193c09040ea924e3792956bd39e0e3`。移植按该基线的实际差异逐项审查，
不恢复旧部署、不导入历史身份，也不整体替换当前文件。

六份冻结输入及上一执行的完整阅读正文、643 条对话、合同 1–7 的无损读取证据
保留在任务证据中；续办首先复核原字节和读取视图可重建性。
当前范围文件优先于旧维护状态；旧 native 资格、工具 inventory/TTL、真实生图、
旧诊断期限和旧资源 HOLD 不转化为本次产品条件。

## 已合入与真实剩余矩阵

| 范围 | main 已有 | 本次真实剩余与对应路径 |
| --- | --- | --- |
| 执行基础 | direct app-server、runner 自管 git、具名 Agent/runtime/model/effort pin、独立验收 | 保留，不改变执行 provider 或角色 |
| 定义/修订 | 无业务定义；现有 capability 名称为协议协商或 CLI 覆盖目录 | protocol/executor_capabilities、store/executor_capabilities、app/executor_capabilities：追加定义历史、请求去重、CAS、active/retired |
| 显式声明 | Agent 配置、公开投影、注册及历史 snapshot | agentconfig、protocol/agent_inventory、agentregistry、app/agent_inventory：可选声明、规范化、同 Agent revision 不可改声明、旧 snapshot 可读 |
| 双角色 required | TaskAgentSelection、implementation/acceptance 冻结 | protocol/agent_execution、domain/task、app/tasks/owner_task_spec：两角色独立引用，新增引用检查，保留的历史引用不重验为新引用 |
| 冻结匹配 | scheduler 及普通 resolver/Workers.Start、retained resume | scheduler/agent_execution、views/agent_continuation、app/runner、runner/capability_launch：精确 definition revision 与声明；普通可用性；claim 固定绑定；恢复不换 pin，检查先于 NextAttempt |
| 查询/CLI/API/调度 | 已有路由及 CLI 能力目录、cursor、共享资源预算 | definition list/show/history/put、Agent declaration list/show/revisions、Task capability-explain/filter；共享 scheduler 解释、pending/unavailable、双角色等待通知 |
| 维护/恢复 | server schema 1–97、journal 1–18；隔离、停止证明、验收文件持久化 | 原迁移保持逐字不变，兼容测试覆盖最新实际源；不得回退恢复路径 |

## 追加迁移及兼容方案

1. **server 0098_declarative_capabilities.sql**：定义历史、不可变 run binding、等待通知。
   `image_gen@1` 仅为声明标签；无工具名、inventory 或资格数据。Agent 声明沿已有
   agent_inventory_versions 保存，不另造身份。旧 Task/spec/Run/请求、维护隔离、
   stop/unknown 和验收文件的全部已提交表及迁移均不修改。
2. **journal 0019_capability_declaration_receipts.sql**：仅追加原 assignment 的声明绑定
   receipt，复用已有 assignment/run/request 结构，不引入 native probe 状态机。
   CurrentVersion 19；保留 17 quarantine、18 acceptance evidence 的 writer 防护与恢复。
3. 无声明 Agent 的规范化结果仍为空，旧配置/snapshot/hash 不变；无 required 的旧任务
   完全保持原调度。新增 required 必须引用 active 定义；同角色原有 exact reference
   在定义退役后仍可保持；跨角色搬移、新 revision 视为新引用。
4. 新协商标记 `executor-capability-declarations-v1` 只授权可选声明合同；无协商的
   peer 不领取有 required 的执行，无 required 的既有协议 2/1 语义不变。
   本部署未导入 PR244 数据；不新增其从未进入 main 的 evidence/TTL/探测恢复通路。
   不接受把不相干旧迁移身份解释为本次追加迁移。

## 实施顺序

- P1：先提交本计划，再从历史差异中选择声明相关配置、协议、store/app、scheduler、
  views、API/CLI/tools 代码与有意义回归。逐项整合当前重叠路径，生成文件用正式生成器。
- P2：剔除历史候选的 inventory/TTL、verify/local-control、harness probe 和真实生图路径。
  本次不修改私有 Issue 详情、sandbox 或两个 Backlog UI 问题。
- P3：新增 0098/0019；用当前 schema97/journal18 数据迁移验证原请求、unknown、pin、
  quarantine 与验收文件不丢失，并检查全部既有迁移字节。
- P4：定向验证定义 CAS/history、Agent revision fencing、双角色引用更新及冻结匹配、
  claim/reconnect、普通 Start 与 stop proof、query/pagination/CLI/API/调度一致性。
  再用 make check 的自动 CPU 预算执行必要完整检查，不引入全机队列。
- P5：提交实际代码与精确 head/base 报告、原件读取证据和原始检查日志，交独立 reviewer。
  本任务不执行 merge/deploy；远端 CI 未执行则明确标未验证。

## 验证及证据边界

完整检查保持 TestSandboxRealOwnerAPIAndRunner 十分钟、
TestFault_RunnerManagerRateLimitContinuesWithoutRepeatingTools 九十秒及 resolver 五秒原预算。
fake 429 只属测试注入。失败不自动扩大预算，不重放历史 unknown。
部署观察按 read_observation 的 observed_at/seq/source 保存；账本 running 与实际进程
分别陈述，当前 Run 证明 Tend 执行这次工作，不证明新实现已部署或浏览器验收通过。
当前运行部署仍为任务起点时，明确报告新 Capability 尚未线上验证。

Refs: D183。
