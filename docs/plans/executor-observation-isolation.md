# 原执行器观察与原生探测隔离

Refs: D241。

## 实际缺口与目标

旧架构 PR 中仅保留本问题：Pool.Observe 和 ObserveRetained 在原生探测持有
适配器锁时返回成功的空执行器集合；heartbeat 随后将其上报为真实空 inventory。
这不是一次性历史迁移，而是普通 manager 探测与心跳并发时会再次触发的运行路径。

确定性回归先保持真实接受配置、worker 与 manager 原 pin，再用同步点暂停一次
manager readiness probe；检查同次心跳仍能读取准确身份及最后已发布的标量可用性。
新增配置不能借用旧 snapshot 的准备状态，原拒绝必须马上撤下对应可用性。

## 最小实现

- 适配器创建、原生 IO 仍由原 mutex 串行化，不启动更多 native probe。
- 按已接受 snapshot 发布不可变的标量观察，独立小读锁不等待 native IO；不暴露
  工厂、配置可变对象或当前 Task 身份。managed category 布尔值按发布时复制。
- 初次看到新 snapshot 时只读取定义，未探测者保持不可用，不回退到旧配置。
- retained pin 使用原集合及对应 snapshot 的已发布拒绝状态；原资源/租约校验不变。

## 验证

保持 probe 实际悬停时读取 current/retained 两种观察，覆盖首次探测、新配置、明确
拒绝与失败后恢复；用 race 检查发布和读取。本次不恢复旧 PR 的其他维护控制或
Sandbox 补丁。原数据与历史请求不迁移，业务恢复仍用现有入口。
