# 维护装配第二次 deadline 失败的有界诊断

唯一写入者：原 Task 指定 implementer。承接已答问题
`dec_01M6TEFSX2B6JDE7M907G66A2G` v2；不重答该问题，不恢复或重写旧失败。
起点 `2aa0678b14b652e3aff52b0d7e0f4a7f915b5ba2`，工作树干净。
Maple 指出的退役声明兼容修复及其定向/race 通过证据保留，本轮不重复修改该修复。

## 已证与待区分

失败原始阶段为 fresh store.Open 19.48 秒、production Run 20.00 秒、initialized 22.02 秒；
首个 health 请求耗尽原两秒 HTTP timeout，随后 uncertain compaction 如预期阻止 readiness，
25 秒总 context 在解除该事实后耗尽。此前的轮询相位修复没有移除 fresh 迁移或核验门槛。
不能从阶段 wall time 推导 CPU、I/O、scheduler 负载或某条迁移是根因。

可证伪假设：fresh migration 的 CPU 执行占绝大多数启动成本；相反，如果 profile CPU
远少于 wall time 或主要热点在进程核验/其它路径，则需要修正这一假设。
隔离样本若通过，只证明该次样本，不覆盖实际整轮失败或建立正常性能基线。

## 有限步骤与退出条件

1. 复核原检查 receipt/log SHA、原停止证明、新账本观察、当前 head/base 与 dirty 状态。
2. 只读跟踪 seed Open/migrate、真实 Run、health 的物理身份检查与 RecoveryReady。
   不改变迁移历史、readiness、进程 hash、请求身份、CAS 或未知事实。
3. 最多一次当前源码的原单例 race CPU profile。将测试二进制编译到 scratch，以与原父测试
   相同的隔离子进程环境执行真正 helper；原25秒 context、2秒HTTP timeout、真实数据库迁移
   与所有断言不变。保留 stdout、CPU profile、wall/child CPU、exit code、源码 head/tree。
   不增加人为压力、全机队列、全机巡检、runner维护或服务重启；不重跑full来挑绿色。
4. 汇总 profile 与原失败阶段可共同支持的事实，区分证实/推断/未知。仅当源码缺陷及其最小
   修复由这些证据支持时更新计划后编码，并至多一次对应的原预算验证，再安排一次有依据的
   最终完整检查。不得移动25秒起点、放宽任何超时、缓存伪造 readiness/物理身份或删去断言。
5. 如果样本不能解释原失败，或需要改变测试预算/条件、通用迁移实现、真实环境维护等额外
   范围，停止并沿原责任链呈报已试方法、具体缺口与建议动作；不凭推断实施更广优化。

Sandbox 十分钟、fake429九十秒、resolver五秒保持原样。六 inputs、固定具名 Agent/model/
effort、独立 Maple 验收和所有历史 unknown/原生停止证明继续保留。无 merge/deploy 授权。

Refs: D133、D183。
