# 缺失进程归属记录的执行边界对账

Refs: D234；延续 D203 的本机 `worker-close`，不新增业务迁移。

## 故障路径与目标

历史 app-server Run 在进程归属文件写出前失败，journal 已记录一次尝试和继承的
thread。缺失文件不能证明没有启动进程；正常关闭因此保留原 Run、workspace 和
lease，同 Task 续行被挡住。现有 `worker-close` 也要求该文件，无法获取新的真实
关闭事实。目标是先关闭原执行边界，再沿原 runner 回执释放资源，交还原 owner。

## 实施

1. 扩展本机 intent 格式，显式区分“记录 SHA 绑定”和“准备时记录缺失”。缺失模式
   要求原 Run/thread/已有 journal attempt/workspace/state directory、精确存活的 runner PID/start/boot，
   以及操作员确认包含原执行与全部后代的独立 systemd 单元。不得从 PID 消失或
   单独缺失文件推断关闭。旧 intent/proof 的序列化及读取保持兼容。
2. prepare 在停止前绑定真实内核 cgroup 目录和根目录身份，两次确认归属文件仍
   缺失。只创建独立的不可变 intent；不补造 owned 或环境归属记录。confirm 仍要求
   原 runner 已停止和两次原边界关闭观察；systemd v1 只能接受原内核目录已移除。
   后来出现 owned 文件、错误身份、变化的边界或观察失败都保留 unknown。
3. traex 在确实缺失 owned 文件时才读取这种独立证明，绑定原 WorkerSpec 的全部
   身份后返回物理关闭事实；不宣称 native Close 成功。不创建 owned 记录。
   runner 沿已有 ConfirmStopped、原失败结果和 RunsRelease 路径处理，不启动模型。
4. CLI 同步完整参数、帮助与操作文档。没有新 grant、在线 schema/wire、业务
   状态修改接口或并行恢复账本；只复用现有本机关闭证明。

## 验证

- CLI 回归从缺失 state/owned 开始：活边界拒绝确认、真实关闭 fixture 后确认、
  固定请求重读不变、原文件始终缺失；保留已有 v1/v2 边界和历史格式用例。
- 覆盖错误 Run/thread/路径、晚到或损坏的 owned、同 Run 后续 attempt 仍活着时拒绝旧证明、PID 复用、未移除 v1 边界。
- 实际 traex stopper 消费证明；runner 重开原 journal 后产生显式失败和正常释放，
  保留原 Run/Task/lease/thread/attempt，不重放 native 输入。fixture 不冒充现场证明。
- 定向检查后提交确定源码并构建完整发布集；按已授权维护取得现场真实边界证明，
  核对原 Run 的正常资源回执和同 Task 续行。独立全 PR 审阅、最终完整检查和 CI
  可并行于受控恢复，合入前全部满足。部署本身不等于原业务完成。

独立审阅复现了固定文件名会让旧缺失证明覆盖后续尝试的缺陷。证明与文件名均绑定
现有 journal attempt，runner 停止路径传递同一 WorkerSpec.Attempt。NextAttempt
推进后旧证明不再适用，无需新增迁移、计数器或删除历史证明。
