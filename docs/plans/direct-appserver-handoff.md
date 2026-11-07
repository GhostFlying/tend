# 直接 app-server 执行与显式人工交接

状态：实施中。Refs: D183；有界维护复用 D180，证据交付保留 D181、D182。

## 目标与边界

- 自动执行统一使用直接 app-server，workspace 使用 runner 自管的 git 后端。自动启动、判断与恢复不依赖 Orca；已有 Orca 适配保留历史及协议兼容，不作为新部署前置。
- 人工交接分为 Issue 上下文恢复与具体 Run 的原生 session 续接。前者必须可用，后者仅在原 host、环境、session 与工作区身份可核验时可用。
- 全新部署不恢复旧数据库、队列、租约、session 绑定或运行凭据。部署前必须向用户核对逐 runner 的 slots，以及 Agent category、CLI、用途和模型配置；未经本次明确确认不得创建或启动新部署。
- 生产和维护共享 CPU，允许并行；不因重检查切 runner 维护，不新增 CPU 分区、全机排队或自动跨 runner 迁移。升级仍复用已有受控维护。

## 实施批次

1. **执行基座**：复用直接 worker 与 git workspace。为启动/恢复握手设置六十秒期限，状态读取三十秒；原 session 恢复失败不隐式创建替代 thread，保留原请求与未知。保持结果、问题、证据文件和回执身份。
2. **交接与查询**：新增只读 `issue handoff-prompt` API/CLI，可指定一个属于该 Issue 的 Run；返回版本、水位、完整查询路径、已知缺口及不含凭据的 prompt。扩展 `run native-entry` 支持直接 app-server/git 资源，分别报告导航、原生身份、可续接性及原因。人工 takeover 确认原自动 writer 停止后再编辑；显式归还核对工作区后继续，Orca 状态不决定控制权。
3. **有界恢复**：补齐旧批量 coordinator 的持久整体截止，三个 target 默认二十分钟；单 component/host 沿 D180 六分钟和既有分段预算。新批次重试、重启和换阶段不续期；历史批次未记录总期限的继续按原分段期限读取和对账，不把缺失值当成新批次续期。补 runner fencing 原因与真实故障回归；调查原五秒 manager resolver 超时，不以调大预算结案。
4. **交付**：每个最终候选完成独立 review、完整本地检查、required CI 及最新主线集成；经 PR 线性合入。构建不可变发布包及配置草案，先完成用户的 slots/category/CLI 配置核对，再执行全新部署。
5. **真实验证**：主线由 Tend 推进，维护者监控并修复系统问题；修复部署后再让 Tend 继续。以原 Capability 最终范围和原两项失败测试建立新的验证任务，不篡改旧事实，不创建竞争实现。

## 已复现的过度防御与作用域修正

以下七项纳入本计划的可交付子项，沿同一 review、完整检查与 PR 门槛交付，不另起竞争实现。

1. **确定失败委派的显式纠正**：以显式 corrects_request_id 连接同一责任轮、已接纳来源的 invalid_request/stale_revision 无副作用失败与后续成功；多步纠正链的每条边均须通过授权和意图校验，未通过的边不能被传递消解。首版允许补齐空的 title/intent，不覆盖任何已设契约值。历史行缺少意图快照时不推断纠正；旧精确意图纠正保持可用。以可审计且已授权的 correction/supersession 关系连接无副作用失败与后续成功；保留旧失败事实。验证补齐必填字段后原回合可收尾，而不同意图、无关来源、未授权纠正与未知副作用不能被消解。
2. **关闭后纯读 unknown**：区分 models/find/usage 的查询未知与有副作用原生请求。仅在物理关闭、事件消费和业务回执证明齐全时允许收尾；不整体豁免 receipt method 集合，不放宽 resume/interrupt/close/tool_result/release 或任何未应用业务。
3. **对象 403 与全局身份**：对象级权限失败留在该请求；只有明确 session/owner 判定失败才改变全局身份，不猜错误文本。回归业务403、真实not-owner和401原请求恢复。
4. **usage 归因错误传播**：sql.ErrNoRows 与真实 SQL 故障分开；真实故障使投影事务回滚并保留 dirty，正常无绑定/冲突仍为 unknown。恢复读取后能够重建。
5. **局部写入的精确刷新**：按受影响 Issue/Task 刷新上下文与 manager，去重 Claim 中的重复刷新；保留显式全局配置/调度入口。以查询计数及相关/无关对象回归证明作用域，不宣称未经测量的线上性能。
6. **身份存储 fallback 一致性**：成功读取 null 尊重外部删除；失败写入的新内存值和失败删除的 tombstone 不被旧 backing 覆盖；成功存储仍可见跨标签页更新。明确恢复策略并验证所有正反场景。
7. **清理不可达 usage 分支**：复核生产构造入口后删除失效的批量加载/冲突合并分支及专属测试，保留有效按索引查询、真实冲突规则和请求内缓存；清理与归因行为变更保持可分别审阅。

以上简化不删除请求去重、CAS、执行 pin、未知副作用对账、停止证明、唯一 writer 或协议兼容。历史未知仍保持未知；未采纳事项须附当前源码证据与理由。

## 接口与兼容

- 新增 Issue prompt 只读查询和 CLI；扩展原 native-entry 的可选数据及查询语义，同步 schema、生成类型、帮助与能力覆盖。
- 控制写入沿用原 request_id、payload、CAS 和单事务。未知原生结果不得以新请求重放。旧合法输入、历史读取和 N/N−1 适配保留。
- 交接 prompt 只读，不赋予执行权限；新 session 通过 Tend 查询，而非把尾页或摘要视为完整历史。Run session 可续接性不能由 Issue 上下文就绪推导。

## 验证

- Orca 不可用时直接执行可完成领取、实现、验收、交付及 workspace 回收。
- 原 thread 恢复失败、丢回执、断连及重启不创建替代执行；有界退出不伪造停止。
- Issue 跨页历史、早期范围纠正、来源水位、缺口与越 Issue Run 均有回归；native-entry 分别验证可用、不可用和身份冲突。
- takeover/归还保证唯一 writer；未知保持隔离，其他工作与部署不因其无限等待。
- batch 整体截止、阶段剩余预算、跨恢复不续期，以及 fencing 的心跳拒绝/截止来源均有确定性故障测试。
- 原 `TestSandboxRealOwnerAPIAndRunner` 与 `TestFault_RunnerManagerRateLimitContinuesWithoutRepeatingTools` 由新的主线任务复现；fake 429、缺 manifest 与历史 fencing 未知保持原结论。
