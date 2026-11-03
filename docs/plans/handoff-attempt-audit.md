# 摘要与交接的有界事实审计

Refs: D170。原 G 组需要区分摘要尝试、摘要结果和原生 thread 交接。当前 summary attempt 表按 source snapshot 复用，同一行不是一生只发生一次；旧 events body 为空，不能据此重建全部 source/target 或失败原因。D164 原报告授权和 PR224 的屏显标签不代替这些后台事实。

## 最小实现

- 新增只读、按事件序号向前分页的 typed audit，要求明确 instance，支持 after/limit，返回稳定 event_id/seq/kind/time 和当时记录的身份。没有写入，不触发模型、receipt 或原生 thread。
- 保留旧事件原文。旧无身份 payload 显示 observation_unavailable，不以当前 binding 或后来 target 假回填；可用旧 object_id 与已有不可变关系选择该 instance 的记录，但不声称当时身份已知。
- 新 summary 开始、失败、成功结果事件同一事务冻结 instance/source thread/source version/generation；summary 原文和 prompt 不返回。新 handoff 准备、phase、refresh 重置/retarget、target 确认、commit 事件同事务冻结当前已知的 source/target 与 handoff_id，未知 target 留空。
- 原事件类型保留；新增的 conversation.* 审计事件不影响内容覆盖水位。重复成功调用不新增已发生事实。审计项是事件，不将 started 直接称 provider 排队，也不将跨页条数当完整历史交接总数。
- 完整 CLI（instance/after/limit、帮助、能力目录）与 Main-only 只读工具随批提供。无 schema、runner 协议或摘要执行策略变化。

## 验证

真实数据库验证两 instance 隔离、旧事件 unknown、同 source 重试两个开始事件不合并、真实 handoff phase/target 不提前披露、重复操作不重复事件、分页无漏/重、非法参数、取消 context、重开数据库后事实不变；原摘要覆盖和 handoff 故障回归保持。真实 owner HTTP、CLI 参数与工具 schema 验证。生产保持停机，不以审计读取冒运行恢复。

失败原因、原 provider 物理排队时长、首次记录前的缺失历史和完整 lifetime count 均无足够证据，本批明确不提供。
