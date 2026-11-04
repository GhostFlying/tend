# Agent 单入口、多角色与冻结策略实施计划

编码前计划。基线为本任务起点；原只读调查四文件作为不可变输入消费，不重新调查线上配置。本轮仅准备源码、迁移审阅与可审查提交，禁止合入、部署和修改线上定义。

## 决策与边界

- Agent identity、已有 env、local_id、name、定义 revision 不因职责变化；唯一入口、CLI、model、effort 来自原 primary。四元组可被不同 Agent 共用。
- 新格式禁止第二 owner 入口；旧格式及已冻结 pin 作为历史专用读取/续接，不改写旧 revision、hash 或在途运行。配置迁移必须显式产生新修订。
- 多 category 只是委托角色；显式 Agent 指派不以分类 membership 授权。自动发现可按角色筛选。server 的主身份与普通 owner 职责分开。
- implementation 的 provider/economy 变体合并为 implementation，formal-review 的 provider 变体合并为 formal-review；保留旧分类历史。调查结果和是否需要业务独立验收仅由冻结 creator policy 决定。
- Orca primary 保持 Orca；同入口 owner 协调能力未实现时明确不可用，不广告成功、不回落 app-server。独立 Capability 基建和详情 UI 设计不在本轮范围。
- 内部共享只允许精确不可变 artifact/file 及任务树责任；修复读/下载授权差异和内部 attention 路由，不扩大兄弟/整 Issue 权限，不自行处理原 attention。

## 顺序

1. P1：核验四文件 size/SHA、读取报告/CSV/JSON/证据包；scratch 记录全部 15 项保留 primary 的映射、角色合并与确切未知，纠正全量人工重选推论。
2. P2：增加明确的新配置代际与单入口校验、历史 adapter；同步 public inventory、登记、catalog、pool 与 retained pin。评估通信 breaking；若升级到 3，支持 3/2 并退役 1，登记兼容边界与拒绝测试。
3. P3：解耦显式 Agent 指派与 category，贯通 API、named CLI、工具 schema/prompt 与必要 Web 读面；保持 request_id/CAS/事务、预算和独立 reviewer 隔离。
4. P4：从任务冻结策略沿 assignment/journal/WorkerSpec 到实际 Brief；覆盖 required false/true/legacy，保持报告合法和代码 PR 门槛，不热改旧 run。
5. P5：修复内部精确材料交接与 attention 责任路由，回归任务树边界、撤销/来源身份、下载与读取一致性。
6. P6：运行定向回归及完整 make check；最终版本交付独立 review/CI。生成中文报告、15 项迁移表、精确 head/base/checks 与剩余责任，提交用户身份 commit。独立验收由原责任安排，本执行不另起 Agent。

## 验证要求

- 同 Agent 多角色不改变入口/profile；相同四元组的不同 Agent 合法；拒绝新定义第二入口；旧快照/运行可精确恢复。
- 具体 Agent 可不匹配/不提供 category，角色自动发现保留筛选；真实执行能力/保留身份/epoch/鉴权仍检查。
- false 策略报告只产生真实结果，不虚构 acceptance；true 与 legacy 保留独立验收要求；Issue 后改 policy 不影响任务冻结值。
- 精确共享可通过标准读/下载路径消费；内部缺材料交由 Agent/main 处理，仅真实人类事实或决策进入人工。
- 3/2 混跑、拒绝 1、重放原 payload、预算 active/unknown 与旧 pin 验证；不将调查证据包的离线校验算作产品测试。
