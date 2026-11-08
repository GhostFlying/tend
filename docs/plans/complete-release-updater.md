# 完整发布与普通升级的 updater 交接

状态：实施中。Refs: D198。

## R1 完整发布

每个通过完整 CI 的 main 提交发布 Linux amd64、arm64 完整包，包含 server、
runner、updater、CLI 与同源 Web。增加发布集清单，绑定源码与全部组件摘要；
原 service manifest 保持格式 1。暂存、分发和验证不得遗漏 updater 与 CLI。
发布使用 main 加完整 commit 的不可变 prerelease；重跑验证原资产，禁止覆盖。
同时组合两个平台为 fleet 包，兼容既有混合架构主机选择。先建立固定 Git tag，
使用支持 draft 的查询恢复首次/中断发布；全部资产核验完成才公开。

## R2 普通 updater 交接

先交接对应 updater，再切换受管服务；runner 沿用逐台维护和备用主机规则。
独立 installer 保存原请求、发布集、配置、进程及锁身份，确认旧 updater 退出后
取得同一锁并收养仍运行的服务。共享文件安装串行，完成证明逐实例产生。
不借用故障恢复授权，未知只对账原 attempt。补齐 CLI、能力协商、状态与恢复。
首次旧部署由新产物的独立 installer bootstrap；生产部署另行确认。

## 验证与交付

覆盖两个架构完整性、源码身份、损坏与缺失、部分发布恢复及失败 CI 不发布；
覆盖服务 PID 保持、每阶段中断、锁冲突、PID 复用、共享文件与丢失回执。
保留历史清单/journal 与 N/N−1；聚焦验证、最终 make check、独立 review、
required CI 和最新 main rebase 后经 PR 合入。发布不等于生产部署。

### R1 首次 main 发布验证修正

首次 main 构建与聚合检查成功，但发布查询使用了 GraphQL Release 不支持的 targetCommitish。改用实际 schema 的 tagCommit.oid 核对已固定标签，并让发布恢复 fixture 校验查询字段；原源码身份、draft 与资产完整性规则保持。
