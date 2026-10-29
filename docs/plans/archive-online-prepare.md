# 离线归档的在线准备（D126，P8b）

当前为独立候选；仅定向 fixture、race、静态检查，不表示已发布或执行真实停机。前件为 Category 执行、托管 Issue Agent、本机离线档案和 owner 只读档案 API。本片不提供 UI，不访问旧生产账本，不自动停远端进程、切换身份或部署。

## 状态与准入

追加 `archive_preparation` singleton：revision、prepared、prepared_at、updated_at。正常初始未准备；prepare/cancel 复用 app request_records、CAS 和领域事件。正常重启继续 prepared，既有升级的超时、attention 或失败不会自动恢复它。实际 migration 60 接真实 Attention migration 59；正式组合需核对迁移前件，禁止空占位或改历史文件。

本机 `tend archive prepare|status|cancel|host` 复用既有 Unix control socket 与标准库 CLI 传输，不要求 release/updater 配置。启用升级时共享同一 listener 与原 tick；无升级时只挂本机档案命令，不创建 installer、凭据、后台 daemon 或新 timer。默认沿现 `upgrades.sock`，可配置 `control_socket`；与 upgrades.control_socket 显式选择不同路径会拒绝。Windows 保留原服务启动行为，本机 Unix 控制不启用，显式配置会说明不支持。

prepare 先持久关闭服务端准入，再由 owner 明确逐 host 应用既有 HostMaintenance/CAS。不能维护的主机显示 blocker，原 gate 保持关闭；支持更新后使用新维护请求重试。重复 prepare 返回原 receipt，不重做主机控制。cancel 只解除服务端 gate；包括原先已经 maintenance 的主机在内，均不自动恢复。

HTTP 业务 mutation 在读取 body 前，用与 prepare 共用的短 mutex 做检查与进程内计数；已接纳上传与操作不持锁执行，允许结束。新请求被拒绝；GET/HEAD、登录访问设置、现档案下载继续。runner facts、续租、结果与既有回执继续。无每请求 ticket、第二账本或全局水位。

既有暂停 hook 同时检查 prepared：不领取新 run，不开始新 turn/summary/compact/handoff/recovery/repair，不把 hosted pending delivery 改为 sending，不生成新 followup occurrence。未执行输入/task/自动待验收/repair、source/Attention 与未触发期限保留原状；不为了排空标完成或取消。已接纳 native 中的回调及其事实继续，产生的未执行后续可以保留。

## 在线状态与托管进程

status 读现有事实，不刷新 presence：当前 preparation、已接纳 HTTP 写计数、hosted 启动/teardown/active/summary、每 host control 与最新 runtime、freshness、open runs、journal pending/blockers，以及持久 sending/unknown、native receipt/steer/compact/handoff/summary 和 worker supplement 状态。

每 host 须有 fresh 的当前已接受 runtime、maintenance 支持、匹配 applied control revision、Ready、open runs=0、pending claims/operations=0 且 blockers 空。retired host 的未收束 run 与已知 journal pending/blockers 也不能隐藏。未知不是空闲；纯未执行业务不要求清空。

Manager 是否 drained 不要求可用 model catalog/session/thread；确实没有外部操作、没有启动或 teardown 的未 ready manager 可以排空。准备期间停止无必要的模型启动重试。已发生的 startup、active turn、private summary 和 teardown 仍被实际观察。

BackgroundPool 在 prepared 时只为已有 admitted native/inflight 沿原 thread 与配置绑定重新 attach/read/reconcile，复用原 native receipt 路径。不创建替代 thread、不读取新模型后换配置、不发新 turn/repair；旧 thread 不可恢复或实际读取失败保留阻塞。原升级暂停且非档案准备的行为保留。

## 关闭与操作顺序

online_ready 只表示当前在线准入与已知外部操作收束，永远不自证 writer 已退出。

1. `tend archive prepare --socket "$TEND_CONTROL_SOCKET"`，按返回 request ID/revision 保留重试身份。
2. 对 status 列出的主机，逐个 `tend archive host --host runner-a --socket "$TEND_CONTROL_SOCKET"`；等待 status 的实际 fresh Ready 与 pending/blockers 收束。失败或未知不换 request ID 隐藏；同一内容重试使用原 ID/原 revision，新维护尝试使用新 ID。
3. 阻止原 supervisor/updater 自动重启，按现进程管理停止 writers；独立观察服务退出。HTTP Shutdown 超时会记录错误，继续等已接纳请求结束，不强 Close、不取消上传，原 deadline 失败在最终返回中保留。档案 Reader 与 store 只在 HTTP 真正结束后 Close。
4. 独立确认 server/runner writer 均已退出后，才用离线 capture/verify。保留 DB/WAL/SHM、journal、原文件及回退材料；online HTTP status 不代替这个条件。
5. 放弃切换时明确 cancel；只对需要恢复的主机显式 `archive host --mode normal`。新实例已有外部副作用后不能盲切旧库，先停新写、核对并保存新数据。

档案 reader 仍只声明真实已测 schema 53–57；本片不放宽到 58–60。切换前应读取旧实际 schema，超范围时先补所需真实适配。历史测试库通过真实前 57 个迁移创建；当前新库仍走真实 store.Open（本候选 schema 60），不修改最新库的版本标记。

## 验证与组合

定向测试覆盖重放/CAS/并发准备、重启保留 gate、原 claim receipt 与 lease、普通 pending/followup 不清空、慢上传完成和新 body 零读取、fresh maintenance/unknown、无模型可排空与真实 startup、原 thread 及 background receipt 只读恢复、本机实际 CLI/socket/host CAS、旧升级入口、Shutdown 超时后实际读完、历史 API/下载/静态 seed 的旧身份隔离。owner schema/TS 从真实 roots 正常生成。

不改变 runner wire 1/0 或旧无准备默认。P4 本机定义、权限与执行隔离不改。组合时语义合并 server main、owner API roots/handler 与 P8c Reader 关闭顺序；固定 Attention migration 59 必须为真实前件。未来 Attention/deadline 新 occurrence 入口须接同一个持久准入 gate，不能复制准备状态；本片未包含 P5 活动用例源码。

## 与 P4/P5 固定候选组合

本片组合 P4 r6、P5 r3、离线归档及 owner 只读 API 的既有合同，server schema 58、59、60 均为真实追加迁移。P4 普通 native receipt 的独立发现和原 30 秒持久轮询退避保留；prepare 期间旧 receipt/事实继续沿原身份对账，新回合、repair/recovery 和 followup 准入仍暂停，不建第二队列或计时器。

新增组合回归使用临时真实数据库关闭/重开：已围栏 manager 的普通 receipt 没有 agent_recoveries 行也能被发现，退避在重启后保持，原 native 结果可入库但不开放新派发。Deadline 的未发、排队、claimed-unknown 三种状态跨 prepare/重启后保留原 occurrence 身份；显式取消 prepare 后仅补当前有效提醒，不重写未知结果、不重发过时 near。既有归档、本机 CLI/socket、owner Attention 回答、manager 唤醒、独立 worker 答案身份和下载路径一并做定向回归。

组合定向 race、vet、schema/type 生成一致性与 TypeScript 检查通过；独立组合 review、正式完整检查、CI/合并和真实切换验收仍是后续门槛。此次没有真实 prepare、capture、seed 或部署。单独的 P5 正式批仍只依赖 P4 与 schema 59，不依赖本片 archive 包或迁移 60。


## 正式 review 修正：未准入请求体与事件跟进

本次仅修两个真实准入缺口：HTTP/1.x prepare拒绝在真实transport上立即返回503并结束未接纳连接，不等待慢body drain；已接纳上传保持原完成/Shutdown等待语义。任务原验收结果继续入库，共享followup occurrence准入点在prepared时不新建消息/occurrence、不推进规则；明确cancel只将active condition的既有next_due调度索引置为可补查，由既有有界sweep重新读取已提交事实；未满足条件恢复原到期索引，不改公开rule/revision、不形成热扫。已结束/到期的原unknown occurrence保持原身份，不重发，不新建timer或队列。原完整检查已通过；正式review发现后按窄red/green与race/vet验证，最终组合检查和CI仍单列。
