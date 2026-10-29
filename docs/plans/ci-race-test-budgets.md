# CI race 测试预算与具名池回归等待

Refs: D123。首次组合 CI 的 App 包在 40 分钟累计超时，现场栈仍在新 fixture 的 SQLite 迁移；同一源码此前在本地 60 分钟包预算下完整通过。将 Makefile 默认包预算与已验证的 60 分钟一致，不改变 App/store 或迁移。

具名池回归同时有 15 秒运行 context 与共享 10 秒 eventually，不能一致表达“等待两个真实逻辑 turn 完成”的阶段预算。先核实 pool 的一秒扫描及原五秒 retry 下限和失败现场，再用原三种 admission 场景做受控 race 对照。候选仅改本 fixture：采用同一有界阶段 context 等待，保留禁用/不可用不消费新输入、v1 pinned 配置、恰好一次 repair 等断言，并核对正常 v2 的实际 logical turn 已提交完成。记录真实状态诊断，不用 Turn 已启动代替业务完成。

只运行三种目标场景的 race 对照及必要格式/vet，保留首次 CI 失败。完整复检由协调者授权后在既有物理检查路径执行，先保留原 b551 源码和结果以复用未变化 App 缓存。不引入测试框架、全局等待变更或产品行为变化。

正常 v2 断言绑定提交成功返回的原“new owner goal” message_id：真实 StartTurn 的 UserMessageID 必须相同，成功 commit 后记录该 logical ID，并经现有 App 读取确认 completed 且 Messages 含此原消息。不把 assignment 后台事件或任意 v2 turn 当作原 owner 完成。
