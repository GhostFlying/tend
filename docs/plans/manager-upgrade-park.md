# 显式升级窗口的空闲 manager park 与续接

Refs: D166。复用 upgrades.state_json 的可选 park 条目，不新增迁移。

## 问题与边界

普通升级要求 server 无未知运行、runner 安装前服务端 open runs 为零且本机 journal
为空。长驻 manager 不能靠 maintenance 自然结束，部署发起者会等待自身占用。已有
retain 对账只保留未知责任，不提供正常安装豁免。本批复用原生 close 与物理停止
证明，再结束该 manager Run，保留 Issue、Task 与原 conversation 逻辑责任，升级后
以新的 Run/lease 承接；不发明 Desired 值、不降低现有安装门槛。

## 显式合同

UpgradeStartRequest 新增可选 park_idle_managers（omitempty，默认 false）。旧请求
省略字段后规范 payload/hash 与去重回放必须逐字保持；true 是新业务动作和新 ID。
start 原子登记升级窗口、待 park 的 exact Task/Run/lease/close request，以及阻止
refresh、新输入和 followup 领取该 manager 的窗口 fence。已有 running/attention
窗口仍拒第二次启动，原 ID 的 unknown 只按原 revision/payload 对账。

仅在原运行没有活动 logical turn、待执行 native business 或未知业务 receipt 时
发出已有 native close；未确认关闭不能标 stopped 或结束 Run。close 回执及末序号
经过原完成条件校验，以 FinishManagerTurn 结束旧 Run/完成 action/Task waiting。
真实 journal 清空和 server open-run 零仍是安装前置；不把逻辑 park 视作物理证明。

原发起者获得异步 accepted；当前 manager turn 返回后才能 park，不等待其自身关闭。
server 主会话/升级控制器完成窗口不依赖被 park 的业务执行器。窗口 cancel/fail 使用
CAS 解除本窗口 fence，保留历史回执，再经已有 refresh 创建新代次/新 Run；linked 或
安装完成亦只允许新 Run 承接责任，不重新执行旧请求。未知原生结果保持 unknown。

CLI 与主 Agent 工具同 PR 提供完整显式选项、请求恢复与状态；主工具通过已验证本机
release 控制接口，不能把远程字符串直接当任意本机路径或跳过 delivery 授权。

## 验证与交付

覆盖默认省略 hash 与旧回放、两个无关 Issue 同时部署仅一个窗口、发起者异步返回
后才 close、忙/unknown 拒 park、物理 proof 与最后事件顺序、cancel/fail 恢复责任且
只有新 Run 续接，原成功/未知请求事实不可变。检查 N/N−1 已有 close 通信兼容。
按原子 PR 定向 race、独立规定 review、本地完整检查与 GH CI；生产保持停机。

## 候选与授权兼容收口

只有显式 opt-in 才探测候选的 --capabilities：该入口在配置、DB、listener 之前
输出 feature 与实际自进程 executable SHA。controller 有界调用固定 artifact，核
SHA/feature，切换前按原 bundle 身份复核；旧程序未知 flag 拒绝，不回退普通启动。
manifest 不新增字段，旧 parser 与旧 runner 保持兼容；默认 false 完全不探测。
CLI 提供 typed upgrade capabilities 诊断同一原候选与预期 SHA。精确可执行文件自证
在 Linux 使用 /proc/self/exe；不具备该证明的平台明确拒本 opt-in，不影响旧流程。

交付授权的 deployment 同样冻结 park_idle_managers（默认省略 false）；已批准旧
范围不能在 start 静默扩成关闭跨 Issue manager。直接本机 operator 的显式新动作
保留既有管理员边界；admitted main 工具只允许带实际已批准 delivery execution。

## 独立审查后的边界补全

候选能力探测只在实际 delivery 批准、当前执行租约与固定源通过 App 预检后执行；
正式准入事务再次核验。已记录的原 system request 以完整 Envelope hash 回放，不再
执行 probe。主工具在 upgrade state 冻结真实 initiating execution ID；完成 turn 不得创建新
升级，但可对原已记录输入返回原结果。origin 不进入业务 Envelope/hash，下一 turn
恢复仍沿同一原输入，不把新 turn ID 写入旧请求。

terminal manager 也必须保留 pending/started/unknown native 操作对账门，物理退出
不是未知外部效果完成证明。park 期间只允许本窗口捕获的 exact manager Run/lease
以 ReceiptOnly 选择重接原事件/回执，不启动新 turn；原业务 fence 继续有效。
取消旧窗口不许可换 ID 重复尚未确认的 close，新窗口冻结复用该原物理请求。

第二轮审查把重启与 teardown 纳入同一窗口不变量：捕获的原 Run 无论 waiting-idle
还是 closing，都只以 receipt-only 恢复原 thread，不允许 start/fork/新 turn。所有
coordinator close 入口受同一窗口管理：窗口尚未排空时 teardown 不得先发物理 close；
窗口已登记 close 时任何 attachment 只引用该原请求。曾参与 park 的同 Run 若已有
未确认 close，即使窗口取消，也先沿原 ID 读结果，不因 teardown 或新窗口换 ID 重发。
正常默认关闭的 upgrade/manager 请求保持原行为，原 request 结果不改写。

最终复查补全取消后的同 Run fence：completed 且 non-rejected error 的 close 仍是物理结果
未决，窗口取消不能把它当成安全继续。使用同一 exact park-history/Run/lease 的未决
close 选择器，同时驱动 native business 准入、原 receipt-only attachment 与 teardown
原 ID 复用。只有成功关闭或明确未执行的 rejected 事实才不再属于该未知分支；不把
无关 Run 或整台主机锁住。原 receipt 原文不改，取消只解除本窗口的新工作全局 fence。

旧重启回归也按同一事实判断：原 close 回执因未应用事件被拒提交，记录仍是 started；取消窗口后仅原 attachment 继续 receipt-only 对账，不因取消丢失物理停止证明缺口。已确定拒执行或尚未发起的 close 才恢复原普通准入。
