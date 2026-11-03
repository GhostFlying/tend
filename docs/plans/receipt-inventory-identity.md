# 收据恢复库存的稳定身份与真实排空观测

Refs: D156。

## 已证问题与接受集合变化

生产准备使同一批 Run 的 observed 从 running 变为 unknown，完整 RunView 与 normal
计划的比较错误地拒绝了未重绑的执行。HostedDrained 也可自然由 false 变为 true，而
native receipt 仍未知；当前恢复入口拒 true，普通入口又不提供原 Run 保护。
本修正确实扩大恢复入口的接受集合，不声称与旧谓词逻辑等价；以稳定执行身份与仍然
保留的安全门替代动态元数据限制。普通维护和旧窗口恢复语义保持不变。

## 最小实现

1. 每份库存独立验证合法 Task/Run 身份、role/generation/spec/host、五种 open 状态与
   Run 去重，以 TaskID、RunID、role、generation、spec version、host 规范化排序比较
   完整集合。Observed 与 Preparation 原文保留，不能据它们的变化重绑原执行。
2. 固定计划保持原字节；两 prepared captures 各自保留完整快照和真实 SHA，不把不同
   hash 写成相同。同 request 重试仍严格比较完整 payload。共享 validator 同时用于
   updater 授权与新 server startup，杜绝停旧服务后才被过时校验拒绝。
3. 恢复 mode 可以接纳真实 HostedDrained=true，仍要求 pending native receipts>0、
   其他已知 effect 为零、prepared CAS、owner 零、所有 host 新鲜维护 ACK 与 claim 零。
   显式 crash-stop 与原 Run/lease 保护不转成普通 graceful stop，不伪造 false。
4. Owner RunView 无 lease/pin/artifact 投影；原 RunID→Action/lease/pin 的不变性和
   candidate 在同一原 DB/WAL 事务中的全集校验继续是必要边界。已自然终态仍为权威，
   不复活。此变更不提供 executor 连续性、zero unknown 或旧 runner 自动 drain 保证。

## 验证与交付

回归真实 normal→prepared 的 observed/preparation 变化及 false→true；保留缺失/新增/
重复/terminal 输入与 role/gen/spec/host 重绑负例、同 request 改 payload 拒绝。覆盖
updater→独立 startup verifier→实际 App 注册和真实 systemd 前滚链，旧版快照自然兼容。
单 PR，指定独立审查、本地完整检查与 required CI 绑定最终 source，再由唯一运营代理
在新窗口部署。本计划不宣称此前尚未生产验证的 migration/health/link/finish 已成功。

## 候选快速退出的有证据前滚

切换前还必须封闭已知失败分支：cmd.Start 成功后候选在 Inspect 之前退出，原实现只留下
starting_successor/unknown，不能形成合法 failed-parent。仅此维护 scope 新增 owned
launch exit 事实：从固定打开并核验 SHA 的 ELF 文件启动，在 Inspect 失败时，只有本次
owned cmd.Wait 返回真实退出状态才记录 LaunchExitProof 与 launch_exited。该事实不是
ProcessIdentity，不补造 start ticks；等待未返回或只扫描到空集合仍为 unknown。

新独立 artifact/window 可引用该父完整计划、原 archive 与真实 exit proof，在 fresh
原 launch 扫描为空、旧服务确已退出、配置/锁/资源与 state CAS 门均满足时前滚。原父
记录不改，实际 Current/Proof 不回退，可能生效的 schema 上界仍须兼容。新服务启动
沿历史校验同一 proof；若父 DB row 已登记，则核对其原 Process 的 PID/launch/SHA 与
owned wait proof 一致，保留其真实 start ticks、请求与回执，不以零 Process 覆盖。
只改变 committed maintenance 的 launch；普通进程启动和旧恢复窗口保持原规则。

空子进程的 failed-parent 安装时，systemd 会删除已停止单元的空 cgroup。只在已确证
owned launch exit 的 inactive/dead、MainPID=0 分支，允许真实 ControlGroup 为空；保留
原观测，使用计划钉定的 active cgroup 计算原 policy 比较，其余 policy、unit、配置、
PID/lock、独立退出证明继续精确校验。旧 Format2 与保留 child 路径不改变摘要或规则。
实际夹具等待唯一一次合法 LastError 更新后读取 fresh CAS，不修改产品错误事实或 CAS。
