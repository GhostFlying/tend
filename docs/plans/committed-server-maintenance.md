# 已提交服务的本机维护前滚

## 目标与边界

从真实已提交且受固定 updater 监督的 server 发起新的受控维护窗口。
旧的不可逆升级恢复入口与其历史保持原样；不伪造升级中状态，不结束保留业务 run，
不改其 pin、thread、决策或未知结果，也不要求先完成 runner 二进制升级。

## 实施顺序

1. 定义独立窗口、请求、收据和私有安装 journal。计划绑定当前完整 state/generation、
   Current/CommittedProof、旧 updater 与 server 的 PID/start/hash、配置、unit、原锁 inode、
   前次 handoff journal hash、候选 updater/server 及兼容范围；新动作有明确过期时间。
2. 计划固定后先完成保留旧 child 的 updater 引导，不要求旧 HostedDrained 为真。
   资源交接证明区分本机进程和远端见证。
3. 新版 CLI 校验并执行同一份私有 installer 字节。独立 installer.lock 与原 supervisor.lock
   约束单写者；停止旧 updater 后保留旧 child，再次校验完整来源与原锁，安装新 updater。
4. 新 updater 优先读取独立维护交接链，验证自身精确字节及前次 journal，正式持久化新的
   maintenance operation；不重写旧 handoff/recovery 历史，不要求旧 binary 理解新增入口。
5. 引导后复用归档准备和主机维护控制，实读 HostedDrained/owner 与 host ack；
   不排空可安全 cancel，只恢复自身 CAS。新 operation 前滚 server，停止与启动使用原请求对账，先备份并保留迁移不可逆边界。
   候选需真实两次递增健康观测和独立数据库链接，之后才提交并解除对应维护门。
6. CLI 提供 plan/apply/status 和中断续进；过期禁止新副作用，已采纳窗口的普通重启继续
   验证原责任而不重复授权。unknown 不重发为新窗口。

## 验证与交付

先在单元故障注入中验证 CAS、第二发布者拒绝、来源漂移、阶段中断、收据去重和历史保留。
随后用真实旧 updater 字节及其已 linked 的交接状态，在隔离 systemd fixture 验证第一次
安装、正常前滚和中断续进；只给新版代码写 RPC 而不能从旧状态安装，不算完成。
最终执行独立 review、本地完整检查和 PR CI；唯一运维执行者以实际合入产物部署。

设计依据 D155；SQL 三索引前置迁移 0080，独立维护账本迁移 0081；完整候选统一验证。

## 本机制候选不可用时的限定后继

仅对本机制已不可逆 checking 或 committed-but-unlinked 的父维护允许新独立前滚。
父原 stop 前实际准备/host ack/owner drain 快照持久化，子计划钉父 payload/hash、generation、
实际 candidate 内核身份及新鲜资源责任；坏候选无 API 也不构成新引导依赖。子显式继承
原 admission CAS，保存父失败和未链接收据，不把父伪装健康；版本覆盖 Current 与父迁移
可能上限。真实进程 alive/no-API/no-register 的夹具必须前滚至新的两健康、独立 linked。

## 审查故障回归

确定未启动与未知启动分开；前者以及旧进程已退出的未启动阶段可用独立请求/CAS与新鲜
资源授权续进原候选，后者只对账。授权过期不永久锁死已停止服务，也不无授权重启。
父 DB link 成功但本机确认丢失时核精确父收据，保留父已 linked 的事实。全量原始快照
保在不可变 journal，状态只引用 SHA；二十次窗口循环应维持线性大小和可读控制面。
