# 部署准备事实的同源受限读取

## 问题与边界

只读部署准备 Task 已明确获得 `observation.deployment=true`，但当前观察只提供实际 server、执行节点自身的心跳及最近一次普通升级简表。负责人和主协调同样缺少完整固定目标、逐节点安装回执及相关并发责任的规范读面，无法把现有真实数据用于准备报告。worker 已有界退出并上报问题；不通过扩大安装权限、重复创建探针任务或要求全局 unknown 清零解决。

## 实施范围

1. **P1：同源准备投影。** 给 `ObservedDeployment` 增加可选 `preparation` 响应。读取本服务配置的公有受控实例标识、当前完整活动目标节点、逐目标运行事实与匹配的安装回执、已结清基线以及目标相关维护和交付阻塞。记录来源、观察时间、事件水位、控制版本和明确的缺失或未知；心跳、实际进程与安装证明分开表达。复用现有 predicate 和回执校验，不复制一套部署准入规则，不暴露路径、凭据、聊天或无关 Issue 业务内容。

   已准备的受控 runner 接管可能没有普通 upgrade 行。此路径只读取该节点最近一份真实 bounded 授权事件，复用现有后继运行匹配及完整 committed receipt 条件；原授权的绑定、当前实例和 hash 都须一致，不能把仅实例相同的运行匹配简化分支当安装证明。原 grant 和安装记录均不修改，不为读取加载节点全部授权历史。规范 Issue 的 delivery 先复用规范视图；只有真正没有规范控制的历史工作项才保留旧观察兼容。
2. **P2：复用现有授权。** Run 仍须具有原冻结部署观察 scope 和当前有效 lease；provider 事实只来自该 scope 已允许 Issue 的登记交付。具名 Issue/Main 只读入口使用原 Issue/后代校验。完整事实不是部署授权，普通报告不因此得到 `delivery_authorization`。本次不新增必填目标选择器，不要求原 Task 为同一已授权范围机械改版。
3. **P3：完整用户入口。** owner API 增加规范只读路由，CLI 增加 `issue deployment-preparation <issue>`，Main/Issue 工具增加 `issues_deployment_preparation_read`；共用投影与输入语义，补齐 schema、帮助、能力目录和入口验证。既有 `read_observation` 自然返回可选字段。读取不探测 provider，不启动升级、不取得目标锁、不激活围栏；需要更新 provider 事实时沿既有显式 refresh 和有界 server job。
4. **P4：真实接续与交付。** 保留原 Task/spec/Run/decision、已释放租约和历史观察。技术答案只能沿正式同 Task 接续路径送入新的有效 Run，不复活旧 lease，不将 released question-input grant 当文件捷径。源码经独立 review、最终完整检查与 required CI 后合入；部署另核具体范围与实际固定 updater。原 Issue 继续由 Tend 负责。

## 必要回归

- 两目标都进入同一准备结果；安装回执缺失、绑定不匹配、旧实例和心跳自报不被提升为确认安装。
- 配置实例缺失、provider 记录缺失或陈旧、runner-only 最新升级、未结安装和相关并发责任均如实表达；无关 unknown 不成为全局报告门槛。
- `deployment=false`、缺失 scope、旧 Run/lease、无关 Issue 均拒绝或不暴露投影；普通准备即使事实完整也不能启动升级。
- Main/Issue/owner CLI 的同源输出、范围、帮助与能力目录完整；读取没有 provider 请求、写事务、锁定或调度副作用。
- 准备后目标或基线变化仍由现有 prepare/start 的 CAS 和权限校验拒绝，不重发结果不明的动作。

这是可选响应和只读入口的增加，不改变旧 wire 请求、默认执行或安装授权语义；若实现发现实际不兼容收紧，则按 N/N−1 协议规则重新评估。Refs: D100、D142、D150、D152、D187、D189。
