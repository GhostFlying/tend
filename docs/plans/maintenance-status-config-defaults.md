# 维护状态查询的配置默认值

Refs: D180。

原配置可省略 stop/health 默认超时。serve 与 bounded command 已先补齐默认值，再将配置摘要写入 updater-only scope；原 maintenance status 直接使用未补齐的读取结果，导致同一配置的合法 adopted controller 被误报为 unknown。

1. 在公共的只读 CommittedHandoffStatus 入口统一补齐已有配置默认值，保持配置文件、原计划与所有持久凭据不变。
2. 用省略默认字段的真实配置覆盖 status、原请求 replay 与正常 serve/bounded 路径的摘要一致性；保留错误 manager、锁或 scope 的拒绝。
3. 只读状态仍返回真实 phase/state，包括失败与 unknown；不增加 Start、健康证明或新的维护授权。
4. 经完整检查、独立审阅与 CI 后发布更新的诊断工具。已安装的 supervisor 不需要为只读入口修改重复启动业务进程。
