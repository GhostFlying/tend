# 问答 handoff 的显式等待与续跑准入

## 范围与现状

先核对 `6438629c9a9a2cd38a9395f70746ba55a4914568` 的现状，实施显式 stack 在其后
冻结的 `6ad5f9a6291d5ba3fb6d4cb29314fc1d8c1b01a8`，保留完整的既有迁移 0088。
原始计划与 RED 基线为更早的同栈版本；集成时只更新上游修复，不改本计划的业务范围。
本计划落实已核对的
owner 原缺陷来源和隔离 follow-up 授权；精确来源与授权原件保存在机器私有证据中。
仅修改 AnswerDecision 到 question handoff continuation 的剩余合同，不重做执行器、
retained-run 恢复、CLI transport、用量观测、Capability 或 UI 工作，不操作线上旧运行。

现有 `AnswerDecision.Apply` 保存答复后调用 `resumeAnsweredQuestion`；
`YieldQuestion` 保存原执行停止回执后也调用同一函数。后者只检查 answered、stopped、
原合同有效且没有后继 action，因而把条件答复也当成立即续跑授权。
原 action、spec、generation、artifact、Issue epoch/owner 和 request 去重机制应复用。

## 有界合同

1. AnswerDecision 增加可选的 typed continuation disposition：`continue_now`、`wait`、
   `dependency`。答复始终是独立事实；option/text 不参与续跑准入，也不做自然语言推断。
   `wait` 必须带原因，恢复条件为独立显式恢复；`dependency` 必须绑定一项普通 execution Task（拒绝 manager Task）及其
   spec version，以该 exact spec 的真实 completed 状态作为机器就绪证据。
   不实现任意脚本、外部 HTTP 条件或通用 workflow。
2. 追加独立持久表，保存原 decision、answer request、disposition、revision、等待原因、
   dependency identity、ready/continued 状态和独立恢复 request。原 answer/audit 不改写。
   没有 typed disposition 的 handoff 答复保守等待显式恢复；历史 answered 的未知条件
   不补判 ready。已生成的历史 continuation/action 不撤销、不重发。
3. `continue_now` 只是准入意图：原 stopped 回执及全部现有合同校验仍须成立。
   `wait` 仅由独立显式恢复动作变为 ready；dependency 的 reconcile 只读取真实账本证据，
   不接受调用者提供的 `ready=true`。就绪未知、原进程未停或合同变化时不创建后继。
4. 恢复与 reconcile 使用自身 request_id、continuation revision CAS、当前 task control
   revision 和原责任/source 准入。重复 answer 不能借同文新请求偷偷更改 disposition；
   重复/并发恢复只能创建一个原 decision 的 continuation action。
5. dependency 自动唤醒只处理本事务真实 Task completion 事件指向的索引等待项；
   不新增每次写入的全表轮询。显式 reconcile 支持重启后的有界补对账。
6. 保留 Task 等待态并释放 worker，query 同时展示机器合同、等待原因及阻塞条件。
   unknown 与等待分开，等待不降回 todo，不赋予 merge/deploy 权限。

## 接线与兼容

- 新增 typed continuation query、resume、reconcile 的 owner API、CLI、协调工具、帮助与
  能力目录；CLI 保留完整输入、CAS、原 request 查询/显式同 ID replay 语义。
- 原 server/runner N、N−1 字段、租约、yield 请求和已冻结 pin 不变；新准入是服务端
  owner 答复事实与后继调度的分离，不向旧 runner 发送新必填字段或新 verdict。
  旧 owner 输入仍能保存答复，但不会把未声明的机器恢复意图推断为 true；通过明确的
  新恢复动作处理旧 answered。增加两 wire versions 的保守等待/合法恢复回归。
- 新存储迁移使用独立序号 `0089`，基线已包含并行 retained-run 的真实 `0088`。
  migrator 强制从 1 连续编号，并从已应用 MAX(version) 继续，不允许制造空洞或假迁移。
  生成 schema/TypeScript 仅通过仓库生成命令更新。
- 采用核对整份文档后分配的 D174 记录设计变化；不填既有编号空洞，不改写已有决策。

## 实施与验证顺序

1. 先独立提交本计划。以现有真实 handoff harness 建立 baseline RED：同样的条件答复
   已记录且原 worker 已停止，但没有显式准入时不得创建 continuation。
2. 实现 typed 存储和最小事务状态机，保留原作用域与 stopped/current guards；
   加入 answer-first/exit-first、两个连续 decision/action 链、duplicate/CAS、unknown、
   原进程未停、spec/epoch/pin 变化与重启恢复测试，得到目标 GREEN。
3. 补齐 query/CLI/API/tool/help/catalog 与自动生成产物，验证完整 typed 输入和拒绝原因，
   并检查旧 answered、已提交后继、N/N−1 的兼容边界。
4. 目标测试使用两核、package parallel 1、低优先级及私有短临时目录；不争抢另一候选
   的唯一 full 预算，不调用真实模型或生产控制。按 session 既有流程安排最终 full、
   独立规定 review 与 required CI；可并行提交/PR CI，但合入前门必须覆盖最终候选。

本分支仅交付代码和可审查证据；原未知执行、follow-up Task、其工作区和线上控制均不动。
