# 验收后交付责任与重开恢复（D151）

## 已核实根因

当前基准的完成事件只结束执行任务，原案例材料记录的是负责人显式关闭；不能称为 TaskCompleted 自动关闭。当前 canonical endpoint 仅保护已登记的 stages、follow-ons、Attention 和 completion review，通用 review reason 可结清验收通知，却没有独立持久的真实交付核对义务。named owner 合同也没有完整继承交付确认规则。现有 reopen 只激活 Issue；named main 不具备该入口，terminal manager 又不在 current manager 查询中，因而恢复责任没有闭环。绑定调查和选定转交记录是调查证据，尚非正式验收结论；缺完整历史运行合同和原始 close receipt。

## 实施顺序

1. 追加交付责任账本：从 canonical 任务验收成功原子建立按完成事件去重的待核对记录，独立于可编辑计划、通知确认及 Attention 的展示状态；历史记录无授权，不批量创建批准或重放。
2. 提供窄的 canonical 交付核对入口与 CLI：记录实际 PR/head/base、验证引用、provider 状态、部署范围；复用 taskless versioned Attention。候选改变撤销旧问题授权；拒绝、延期及有界报告终点需要可归属证据，不能凭一般 review reason 清除交付责任。
3. 已批准只允许冻结范围内的后续交付执行；原实现 task 保持 completed。新候选/不确定 provider 状态阻止旧批准推进，交付结果仍需具体证据和后续验收。未批准不产生交付执行。
4. reopen 共用 owner/API/CLI 与 named main 合法 owner 来源入口，在同一事务恢复新一代负责人协调。保留历史任务、会话和消息；旧 manager 未结清/unknown 时明确阻断，不能复活 terminal implementation，也不因重开获得合入批准。
5. 补根因回归、并发/CAS/重放/unknown/历史测试，更新实际 named 合同、能力目录及生成 API 类型。UI 卡片属于另一个 Issue，本计划不改其实现。
6. 执行针对测试与 make check、核对最新 main、用户身份提交并准备 draft PR。独立正式验收由 Tend 的原责任链执行，不能从本执行器私自启动另一个 Agent。提交实际代码、diff、验证记录和未决项，停在 owner 合入确认前。

## owner 对自修执行责任的补充核对

同一目标的 owner 澄清要求 Tend 自己执行获批后的合入与受控自更新。核对发现已有真实 worker 和本机受控升级通道，但新交付结清检查尚未强制部署回执。补充最小实施：在确认候选中冻结 server 实例与 host 集合；交付任务明确由 Tend 执行合入、必要的候选更新后重新确认、发布构建、现有受控 upgrade 和原问题回读；部署结清须提交本次执行后开始、server 健康和全部目标安装均核验完成的升级批次。复用既有 UpgradeEvidence，不增加通用工作流或新部署器。交付执行必须交付 report，使独立验收实际看到合入/升级/readback 证据；只有 optional report 会被既有验收隔离投影隐藏，不能依赖它传递必验产物。隔离回归连接真实 Git 合入、实际任务领取、既有 StartUpgrade/授权/健康回读事务和交付结清，进程与主机观察使用测试替身；另保留现有本机 controller 到 updater socket 的执行测试。旧批次、范围不符、失败和 unknown 不能结清或重发安装。执行 worker 若在待更新 host 上，在已持久启动批次并保存回执后交付 report/释放原 run，不持有自身租约等待整机排空；后续独立验收读取完成批次及原问题行为，责任账本仍未结清。回归须实际推进两个 runner 安装，不只覆盖相同 bytes 的 skipped 路径。另须覆盖报告提交至维护控制生效之间的验收抢领竞态：复用 report.evidence 的 upgrade:<id> 原批次引用，同事务固定到 typed delivery execution；仅这种部署执行的验收在批次 running/attention 时等待，不占待更新主机。此门禁置于原 views.ApplyIssueDependencies 之后，不修改外部 context/baseline admission guard，失败终态仍允许独立角色审查。

## 验证边界

只用隔离测试中的真实 owner 答复入口验证授权，不对生产伪造答复，不修改原图片案例、不合入/部署/联系他人。保持 server/runner 已有协议 2/1；新增 owner 输入及服务端存储不改变旧 runner 必填条件。若实际实施需收紧旧 runner 通信，则先重新评估版本兼容，不能隐式改变默认语义。
