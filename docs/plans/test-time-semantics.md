# 功能时间与测试执行预算分离

Refs: D25、D130（测试策略；不改变产品行为）。

## 故障与契约

原故障路径为 `make check` / Actions race 分组 → serverkit/Sandbox 功能用例 → 空 SQLite 迁移及服务 seed → 功能断言。部分短 context 在迁移前启动，机器速度决定功能能否进入断言。session 的业务契约是注入时钟加 token TTL；它不要求空库迁移在一分钟内完成。Sandbox 的功能契约是实际 owner/runner 协作、取消和保留身份重开，不是固定设备的八分钟 SLA。

区分五类时间：业务时间用现有注入时钟；并发同步等待真实事件/状态；环境初始化独立有限 watchdog；真实冷迁移/重开保留专用覆盖；性能 SLA 另行定义硬件与负载。所有功能测试仍有包级总超时与独立阶段防卡死上限，不以无限等待替代失败。

## 实现边界

1. 复用 `storefixture.Template` 的真实迁移、checkpoint、私有独立副本，为 serverkit 非迁移功能用例预备当前 schema。fixture 准备和功能动作各自有明确、可诊断的 context；不注入伪造 Store、不修改迁移 SQL、不改变产品 `Run`。
2. Sandbox 端到端场景保留真实空库启动和真实 retained restart。分开准备/首次 ready、场景、重开 ready/恢复与清理的有限预算。服务生命周期的 context 不被短场景初始化耗时消耗；启动失败仍被原完成通道传播，退出仍 join 实际 goroutine。不得缓存或跳过这两个真实生命周期路径。
3. 增加小型共享 testphase 诊断辅助，区分阶段名、实际耗时、deadline、取消原因；业务时间不依赖它。用可控虚拟时间验证慢 setup 不耗尽之后的 scenario、deadline/父取消/真实错误不被吞并。
4. 本地完整入口与 Actions 使用相同 `make test`/racecheck、完整动态发现和分片。默认保留 `TEST_COUNT=0` 的 Go 原生结果缓存；新鲜定向回归、测量或逐例执行证明显式 `TEST_COUNT=1`，缓存命中不得宣称为本次实际执行。保留模块/编译缓存；原生 JSON 报告持久化且 Actions 在失败时也上传原件。验证命令覆盖、失败 peer 和所有新测试分组归属。
5. 审计既有短 sleep/poll 和取消测试；仅修复与已暴露初始化耦合相同故障链，不进行全仓机械超时替换。保留真正的生产重试/租约/清理语义、错误与 unknown。

## 验收与限界

先保存基线旧墙钟耦合的确定 RED（虚拟时间/受控初始化，不消耗一分钟真实等待），然后 GREEN 验证分阶段上下文、父取消、setup 错误和场景失败；实际 race 跑相关 serverkit 功能、冷迁移、Sandbox 两个生命周期（按资源协调），运行检查入口/覆盖及工作流静态验证。保持 race、断言、真正空库迁移和 retained 重开，不新增 skip 或自动重试。

本轮仅独立本地源码提交，不 push/PR/CI dispatch/retry/cancel/merge/deploy；不修改生产状态或当前业务候选。最终需集成后的独立审阅、完整本地检查、Actions 和受控部署，不能用本轮定向测试或静态工作流验证冒充。
