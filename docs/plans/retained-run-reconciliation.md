# 原运行待对账与主机恢复分批修复

Refs: D159。

## 范围与基线

复用已合入的维护保护与 unknown 事实，不重做旧升级恢复或验收准入。
本批归并原人工介入总账的 run-control / registration-recovery 与当前原运行对账缺口，
父责任仍为 wi_01M6EHB7XJX4JKVVSG8AMZQ591；不创建重复 Issue，也不把 closed 视为交付。
首个 PR 只修复一个确定根因：受保护的单个运行令整个主机 Hello 失败，进而阻塞新实例
登记、配置确认和无关运行的合法对账。原运行本身仍需独立业务对账，不能冒称已恢复执行。

## 第一批：按能力显式保留原运行

新增 opt-in runner capability 与 retain verdict。只有明确报告这一能力且协议受支持的
runner 才收到 retain；旧 runner 的原错误/拒绝路径保持，不发送其可能误当 stop 的新值。
这是协商后的可选扩展，不增加旧请求必填字段，不改变旧有效响应/默认语义，协议代际
保持 N/N−1；以新旧 peer 回归验证这一兼容性判断，若发现旧语义变化则升级协议再交付。

新 Hello 在同一事务中登记真实 runtime/control，逐个核原 host/run/lease/业务控制。
对仍有效的维护保护只返回 retain，不续租、不把 unknown 改 running、不重分配；未上报
但受保护的原运行也保持事实。无关运行继续已有规则。已终态、旧 lease、跨 host、明确
用户停止仍走原停止路径，不能用保护复活或绕过业务控制。

runner 收到 retain 时持久记录已有 fenced 状态，停止本地 attempt，但不把原业务标为
完成、不清 journal/pin、不启动/续接原 worker。原请求和物理停止按既有保护留存。
所有本地 start 入口必须尊重持久 fence，只有收到合法 Continue 后才解除；显式 Stop 的
原收尾路径仍可处理。用 runner 内存锁序列化 fence 与 start，避免先读旧状态再启动竞态。

## 后续原子批次（不混进首个 PR）

补 CLI 与主会话可用的原身份对账读面/显式动作：真实 task/run/lease/pin/native 请求，
request_id 与当前控制 CAS；请求一次检查不等于恢复成功。任何缺少原生事实的场景保持
unknown，独立退役/继续决策不能冒充旧请求成功。具体动作合同在首批状态测试后另写，
不以新宽泛 override API 绕原生进程/外部结果安全边界。过期 decision、报告共享、摘要
attempt 与授权传播分别按原案例后续处理，不纳入这一修复。

## 验证与交付

真实 App/DB：legacy peer 保原拒绝，opt-in peer 单原运行 retain、无关运行正常 Continue、
新 runtime 登记/control ACK 可完成；原 lease/status/pin/unknown/request 不变；原保护但
缺本地 journal 不自动重派；跨 host/旧 lease/用户停止/终态不被 retain 吞掉。
真实 runner/journal：retain 不启动或续接 worker、不清 journal，重启后 fence 仍生效；
并发迟到 start 不能穿透；合法 Continue 可恢复普通 fence；显式 Stop 保原真实收尾。
保持原维护物理 stop/Release/request 重放回归。先定向 race，再 prescribed review、
唯一完整检查与 GH CI 并行；全部通过才由唯一集成者合入。生产保持停止，待全批次正式
合入后一次配置-only fresh 部署，不做本批临时旧环境部署。

## 独立审查补正

运行中的 manager 收到 retain 后，其原普通 fencing 清理会发送 physical Stopped 并
删除 journal；此次显式 retain 必须阻止该清理推进到释放或删除。仍保存真实本地停止
及原回执，不把物理退出冒成业务完成。对正在上报的 manager，删除 pending 与最终
Done 同 retain 应用互斥，防止迟到清理越过保留。

成功心跳后对持久 fenced journal 触发原 Hello 对账通道，使无 active executor 的
保留运行仍能收到后续明确 Stop；不把它加入心跳续租、不自动 Continue 或重新执行。
用真实 manager attach→retain→本地退出→重启→心跳触发的 Stop 检验这一生命周期。

第二轮审查扩大同一不变量到所有本地结算出口：worker 的 submitting/releasing 与
manager 的 close/replay/Stopped 都通过共享 settlement guard，和 retain 应用互斥；
已在途 Artifact/Verdict/物理回执仍按原 request 上报，retained 时不替换请求、不清理
workspace、不推进 Done。权威 Stop 后新清理执行器可结算，真实已完成的结果不重跑。
retained active 从 heartbeat refs 排除；协商该能力的 server heartbeat 同样不续受保护
原 lease，防止迟到 refs 延长租约。legacy 保持旧行为。按角色、阶段、迟到结果、
重启、Stop 与 heartbeat 做有限生命周期矩阵，不再只测 started 入口。

第三轮补正把 Continue、Stop 与 settled-snapshot 纳入同一结算事件门。权威 Continue
同时解除 durable fence 与 active retained；被旧 retain 取消的执行器退出后主动请求
对账，既有报告仍按原请求结算。权威 Stop 允许真实收尾，但不自动续租/重新执行。
迟到 retain 遇到已结算条目时，只在历史 journal 的原身份和 Done 均确认后局部
忽略并再对账，不能中断其它 verdict；存储错误、身份改变仍拒绝。旧执行器的
迟到 fenced 写入不得覆盖较新的 Continue/Stop，未知结果不能被此事件转换成成功。
