# Capability 候选的维护装配超时有界调查

唯一写入者：原 Task 指定 implementer。承接原已答问题的 continue_now；不重答或替代原 Task。
保留起点 `8efbd8ebbeb613d4c66a15a8342045b4406f6454`、前三个提交、整轮失败和一次通过样本。

## 已知与未知

整轮 make check 仅两项 serverkit 维护装配用例在原 25 秒总预算内失败，均 configure/initialize
已执行且 model_opens=0；同候选后续单次双例样本通过。不能从此推导负载根因，亦不能排除
Capability 的间接影响。原日志缺少具体失败阶段。调查只覆盖这两项和解释失败所需的关联路径。

## 有限步骤及停止边界

1. 复核工作树、保留 manifest、原检查结果和新观察；旧失败不改写。
2. 只读跟踪装配至本机 maintenance health、readiness、独立 updater link 及 receipt startup。
   核对候选差异，区分数据库迁移/配置、进程核验、HTTP、readiness 等阶段。
3. 先只给原测试增加阶段耗时、最后完成阶段及本机请求诊断，原 25 秒 deadline、2 秒 client
   timeout和生产路径不改变，不记录凭据或完整请求。执行至多一次带诊断的双例检查。
4. 如阶段证据尚不足，可再选一种有界区分实验：一次 scoped CPU profile，或任务 base 的同用例
   对照（源码临时副本仅放 scratch，不建 worktree、不切换生产）。实验仍保留单例 25 秒。
   不做反复 full、重试筛选或无限采样；到此形成已证/推断/未知结论。
5. 只有证据支持候选直接/间接回归时才最小修复，并先记录根因与预期验证。若是相关测试或关联
   路径的确定缺陷，修复必须保持真实 production assembly、物理身份与恢复门槛。若需实际服务/
   runner 环境维护或方法仍不确定，按原责任链提交具体 blocker，不自行部署或维护切换。
6. 故障结论或修复证据形成后，才安排必要定向和最终完整收口检查。报告所有整轮历史失败；
   单次通过不覆盖失败。完整检查沿原自动 CPU 预算并保存原逐例 JSON，不增加全机队列。

不调整原 Sandbox 十分钟、fake429 九十秒、resolver 五秒及这两例二十五秒预算；
不扩展 UI、Capability 整合范围或 Agent/model/effort；最终仍交指定独立 reviewer。
无 merge/deploy 授权。Refs: D183。
