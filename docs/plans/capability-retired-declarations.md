# 退役定义与已接受 Agent 声明的兼容修复

唯一写入者：本 Task 的指定 implementer；独立验收保持原职责。

## 起点与真实剩余

本次接续工作树干净，候选为 `17f8be4159d17f147733338eef4597076615fac7`。
只读远端 main 核对仍为任务 base `bd51f1288aaf1652fe25db138a7701666211d1dc`。
PR244@`2ae5b26ca715c27365cb4a351d6c098ea6432eb7` 仍只作历史源码来源。
原[整合矩阵和迁移方案](capability-current-main.md)继续适用。

| 范围 | 当前候选已有 | 本次剩余 |
| --- | --- | --- |
| 定义/修订、双 role required、冻结匹配、查询/API/CLI/调度 | 声明式闭环及相应回归 | 保留，重新执行必要完整检查 |
| Agent 显式声明 | 全历史 revision fence、注册 request replay/CAS | 退役后无关 Agent 更新错误地重验旧声明；只修复此准入区分 |
| 迁移、无声明/无 required、维护恢复 | 追加 server 0098/journal 0019；原 115 份迁移保持不变 | 无数据结构变化，不新增或修改迁移；复核原迁移与预算完整性 |

## 编码前方案

1. 先实际运行独立审查提供的 scratch overlay 复现，保留失败输出。
2. 在 `RegisterAgentInventory.Apply` 原事务内保留 revision fence。以完整
   execution environment / local Agent identity / Agent revision 标识已经接受的声明。
   校验声明不可变后，历史身份/revision 的相同声明无需再次通过当前 active 准入。
   查询全部已接受历史，不能只查询当前 inventory，否则移除后恢复会错误失败。
3. 新身份或新 Agent revision 仍须逐条校验精确 definition revision 与当前定义都 active。
   相同 revision 的声明改变仍是 conflict；不能借历史身份接受替换引用。
   原 canonical 校验、协商、local revision、server CAS、request replay 和写事务保持原序。
4. 回归覆盖退役后 beta 更新而 alpha 声明未变、原请求重放及 local snapshot 幂等、
   stale CAS 拒绝、移除再恢复历史声明、历史 snapshot 不变、相同 revision 修改拒绝、
   新身份/新 Agent revision/替换精确引用的 retired 准入拒绝，以及 active 新引用成功。
5. 更新 D183 的历史声明语义，执行定向回归及默认自动预算的完整 `make check`。
   Sandbox 十分钟、fake429 九十秒、resolver 五秒及其他既有超时均不变。

## 交付边界

六输入原字节及全部阅读视图续办复核；既有完整语义阅读记录、旧失败和停止证明保留。
本次读取复核不冒称新一轮完整语义阅读。当前范围仍优先，image_gen 仅标签。
不更换 Agent/model/effort，不调查 Backlog UI，不改运行部署、不 merge/deploy。
报告绑定最终 head/base/tree 与真实检查回执，source/CI、部署、执行及历史 unknown 分开。

Refs: D183。
