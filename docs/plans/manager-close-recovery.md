# 已关闭 manager 在停止后的原回执恢复

## 问题与范围

原 manager 的 native close 和物理停止已在 runner journal 完成，停止 envelope
也已持久化；但收尾先无限重试等待业务事件应用的 close reply，导致物理 stopped
排在它后面，server 无法释放原运行并安排原回执读取。相关 started 恢复还会把已
取消 attempt 的 context 传给原 close 查询。两者都只涉及原事实续传，不应重新
打开 native，也不能用停止事实伪造业务已完成。

## 实施步骤

1. 用既有 journal/runner/App fixture 先复现 releasing/stopped 已确认但 close-first
   阻挡原 stopped envelope，再覆盖原 completed close、停止/取消 attempt、
   未应用原事件和未确认 close envelope；保留原请求，确认没有新 native/provider。
2. 已完成 close 的结果恢复使用有界 runner lifetime context，先于 attempt 停止处理。
   只读取已有原 close；原启动、retained 和新业务准入保持。物理 stopped 事实与原
   close/业务回执分别持久化和发送，原事件按顺序上传，原 close 游标保持不变。
   普通恢复与受控更新的事实重放都遵循事件、真实 stopped、原 close reply 的顺序。
   每条 ACK 只消费自身请求；正式 Stop 已允许的本地归档保留未 ACK 的原 close，
   不把物理退出当作该 close 或业务执行的成功回执。
3. 覆盖 retained 到正式 Stop 的交接、未确认/错误 close、事件/停止/关闭暂不可确认、
   重启后的同一请求恢复及 reader 后续；不能因取消而重复 native 或制造完成。
4. 更新 D210；不改变 wire、API、schema 或 owner 权限。定向 regular/race/vet 后
   提交供现有协调者执行完整检查、独立 review、CI 与受控部署。

## 边界

已完成的原 close 是物理停止事实，unknown/失败或缺失 close 继续原核验。恢复只
发送原 sequenced events、同一个 native close 的原回执和真实停止事实；不能把业务
未决当作成功，不新建业务 Task/Run、不修改旧事实或手写数据库。
原责任的既有 receipt-only 传输可在物理容量释放后自动续收原 thread/turn；
这是原历史读取，不是重复执行原业务。
