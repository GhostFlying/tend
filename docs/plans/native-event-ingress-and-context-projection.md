# 原生原件摄入与上下文投影

## 问题与目标

Issue context 把已保存工具原文再次内联，后续读取又成为工具原件，形成递归放大。
合法大 native event 虽已持久化在 runner spool，却被统一 RPC 小包上限拒绝。
原件首错又挡住原断连和 stopped 回执，外层立即对账重进形成密集失败。
修复须同时恢复上下文可用性、完整原件传输与真实停止/业务回执收敛，不截断原件或重放输入。

## 实施顺序

1. 先保留真实 HTTP 大包和 context 回灌 RED。大包沿原 RPC/Envelope/operation，
   先核真实 credential，再以固定缓冲暂存私有临时文件；受控并发下从文件标准解码，
   仍走原 Validate、Authorize、原件 staging、连续序号和 ACK。小控制请求不争大原件预算。
   历史 manager 预认证仅准入其原 receipt 操作，不授予其他动作。
   已知大包在读正文前预认证；未知长度先读取至多小包预算加一字节，
   按真实长度分类。小 chunked 请求不占原件处理预算，保持原精确历史回执认证；
   只有超过预算才预认证大原件并继续暂存余下正文。
2. legacy context 的工具项改为明确标注省略并指向原 message 的索引；owner 原文、
   身份、顺序、水位和分页不变。精确 materials/message 读保留完整工具原件和原 scope。
   incremental、API、CLI 与工具说明同源；不用 prompt 代替投影修正。
3. 永久拒绝保留原 pending 字节/身份，按现有 RetryDelay 有限退避后对账，
   不立即重启同一失败 executor。确认物理停止后可独立记录原 session_stop 事实，
   不宣布 Run/业务完成；native_stopped、close、EOF 保持真实有序尾部要求。
4. 真实 HTTP 覆盖 known/chunked、字段顺序、鉴权、取消、并发及幂等；
   context 覆盖多次读取不回灌、原件可读、隐私与原分页；journal 覆盖永久失败、
   物理事实和原序号恢复。仅运行必要 regular/race/vet，完整门槛由协调者执行。

## 取舍与兼容

不加手工 JSON parser、canonical hash、新 wire、grant、请求账本或另一个任意正文限额。
未知长度前缀使用原小包内联预算；大原件余下正文以固定缓冲暂存。
标准解码和既有 App staging 仍会按需物化一份真实原件，
明确 O(单原件) 的资源成本，不宣称全链恒定内存。临时包装文件不是永久原件副本。
通信窗口仍为 4/3，旧 pending Envelope 不改写，部署先安装可摄入原件的 server。
读投影增加可选 omitted/original reference；完整原件入口不变。

Refs: D230。

## 状态

实现与必要定向验证已完成。真实 HTTP 大包、上下文原件引用和原生命周期对照
均保留 RED；受影响 race、真实迁移外部等待 E2E race 及 vet 已通过。
race 曾揭示新 journal fixture 的大正文超出其原一秒请求预算，保留该失败后将
该用例缩到仍超过 spool 阈值的正文；大包契约由独立真实 HTTP 用例覆盖，
不延长预算或放宽原断言。最终完整检查、独立审阅、CI、部署与原业务恢复待统一验证。

## 合入前兼容修正

未知 Content-Length 不代表大原件。新增真实小 chunked 控制并发与已结束普通 Run
消息回执回归，确保旧有效请求不被 manager 历史预认证替代。Decode 只将 JSON
语法、类型和缺失正文错误映射为 invalid_request；其他 IO 和取消错误保留传输原因。
修正仍复用原 Envelope、认证和处理预算，不改变 wire 或新增权限。

兼容修正的三项原行为均已保留真实 RED；修正后相关真实 HTTP、历史回执、
有界鉴权及 IO 分类共十个根用例的 regular/race 和 runner API vet 已通过。
原首轮冻结与失败不覆写，最终统一门槛仍由协调者执行。
