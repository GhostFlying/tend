# runner 托管 Codex 环境按执行节点路由

Refs: D197。

## 问题与最小修正

服务端为 runner 托管的协调会话选择远端 transport 时，先检查服务端本机同名 runtime 的 Codex 环境映射。runtime ID 在两个执行节点上合法同名，这个本机映射不能作为拒绝远端连接的依据。

- runner 托管分支仅要求已有 `RunnerRunID`，沿原 run 返回 `RunnerSession`；环境绑定仍由执行 runner 校验。
- 服务端本机托管分支保留原 executable 与 Codex 环境绑定、历史执行隔离规则。
- 不增加权限、协议、迁移或依赖，不重试业务、不建立替代 Task/run。

## 实施与验证

1. 在现有 serverkit 组装路径中提取 named session opener 的内部构造接点，使用与生产相同的默认 opener 验证远端 transport，不使用 `SessionOpener` 注入绕过路由。
2. 回归覆盖服务端存在同名 runtime 映射时真实 runner proxy 可连接、没有 claim 仍拒绝，以及本机路径仍校验环境与原绑定；保存真实 RED，再移除远端的本机映射否决，执行必要定向 race/vet。
3. 更新 D197 的执行节点边界。保留原源码身份与未知事实；部署及原责任恢复由协调者在最终交付后处理。
