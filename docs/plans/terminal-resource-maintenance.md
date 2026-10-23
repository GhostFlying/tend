# 终态资源隔离与 runner 维护

## 问题与不变量

终态任务的旧失败 run 即使本地 done，也可能没有原生停止证明。资源必须保持
隔离，但空闲 runner 的替换不应依赖补齐该资源的停止证明。D84 的排空边界是
当前服务工作，而非任意历史原生进程。不得伪造成功或停止，不删除历史资源。

## 实施

1. 保留旧版空元数据快速隔离路径，不改变其不补停止证明的行为。
2. 普通资源继续先走现有对账；可证明的资源照常释放。仅在已知旧 app-server UUID 缺成功
   最终报告的静态失败路径分类隔离与全局阻塞，不把 journal 或 harness 错误
   忽略为成功。
3. 可免全局维护门禁的条件：server 明确任务终态且无开放/未知 run；所有关联
   journal run 存在、done、task/resource 身份匹配，无 held/fenced 或活动 executor，
   无待确认请求及释放意图。不按 thread、worker、provider 空值或日期识别。
4. 资源 blocked map 始终保留，日志明确仍待停止对账；不补 StoppedConfirmed、
   不创建释放/复用/清理授权。当前领取、结果交付、外部请求和资源操作继续硬阻塞。
5. 回归覆盖旧失败原生 UUID、正常可证明释放、harness/provider 不确定负例，
   缺 journal/身份冲突/非终态/held/fenced/活动 run/待确认操作等负例；验证整个维护
   流程可 Ready 但不改资源事实。执行 runner race、vet 和 diff 检查。

更新 D84 和操作边界；独立评审与 CI 通过后按维护流程发布，验证资源仍隔离且
runner 可以完成排空。此项验证不创建模型或业务任务，也不更新外部 harness。

## 独立评审修正：原生请求未知与静态缺证明分离

真实 Orca thread 可保存未确认的 launch/send；它们不在 journal requests 表中。
因此 harness 历史释放调用的任意错误不能笼统包成可豁免的停止证明不足。
本次将新增失败豁免收窄到已知旧 app-server UUID 缺成功最终报告这一静态事实；
未知 worker 身份、缺原 harness、元数据冲突及实际 harness 调用错误继续硬阻塞。
原先纯旧空元数据兼容路径不变。真实 Orca release 路径的 launch/send（完整与
不完整 terminal/pane 身份）均回归验证不 Ready、不启动/关闭/重放原生请求。

多 run 资源还须逐一检查所有未确认停止的 run 均为已知 app-server UUID 且无
provider 身份冲突，不能因第一个 run 的静态缺报告提前返回而忽略后续 run
中的原生 launch/send。已确认停止的 run 不重复要求此静态例外。回归同时覆盖
两种 run 排序及 launch/send；旧空元数据快速隔离条件保持不变。

从未尝试启动且无 thread 的 done run 与既有对账语义一致，不要求原生身份或补
停止证明；该判断仍受 task/resource 身份、held/fenced、活动执行及请求约束。
