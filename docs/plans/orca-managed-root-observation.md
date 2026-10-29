# Orca 受管根 turn 的归属与完成观测

Refs: D63、D66、D67、D124。

## 已确认问题与范围

真实 Codex 实现 run 出现两种独立错误。A 的原生根 session 正常完成并保存了工具结果，但 Orca pane 聚合 prompt 是后台会话文本，Tend 在首次 session 绑定前将它认作人工接管。B 在原生根 turn 开始前被 pane 的 done 提前认作完成，保存空 worker result、撤下 bridge，后续清理发现活动又转为人工控制。这里的聚合 prompt/state 不能证明当前根 turn 的输入、完成或控制权。

本片修正既有 Orca adapter 的观测，不新增 provider、业务权限、后台巡逻或文本白名单，不改变 task/spec/run 的身份或代次。原生 API 提供完整根 turn 时复用原接口；旧 Codex TUI 利用已有 providerSession 的原生 JSONL 只读字段补齐根事实，不依赖 Orca 升级。

## 最小合同

1. 原启动记录的 runtime、terminal/pane、launch marker 与实际 provider session/根 turn 必须建立正向绑定，绑定在业务完成/接管判定前持久化。原生记录的 session_meta 必须匹配所指 session，实际用户输入必须对应原 run/launch；不能仅凭最新 pane 文本绑定。
2. 同一根会话的实际受管 turn、用户输入、正常结束/中断及未结束工具调用来自原生事实。后台会话的 pane 聚合 prompt/state 不能覆盖它们；没有本轮根事实时保持 unknown，bridge 与原执行保留，不提前 worker_done 或报告人工接管。
3. 真实 runtime/pane/launch/session 更换，以及原 session 中未经 Tend 发送的新用户输入/新根 turn，仍触发原接管边界。读不到或未完整的原生记录不能授权重放 launch/prompt、伪造完成或关闭终端。
4. 同一根事实用于运行、重启后的重新绑定及历史完成清理。沿既有 request/receipt、冻结执行与 handback 语义，不通过忽略某段后台 prompt 绕过归属判断。

## 回归与恢复

使用脱敏、最小化的真实形状 fixture：A 聚合文本变化但原 session/launch/turn 正常完成，原工具结果仍可返回；B 聚合 done 出现在根 turn 的首输入前，不得完成或撤桥，真实根 turn 后来开始并能提交原结果。另覆盖同 session 的真实 owner 输入、session/runtime/launch 更换、未完成工具调用、截断/缺失记录、重启绑定及历史清理。fixture 不含凭据、真实本地路径或账号。

原生产 run 在修正、独立复审、完整检查、CI 与发布前保持不变。A 保全原桥结果和上传 receipt，恢复不得重新 launch 或重复提交未知结果。B 已有不成立的空 worker_done，不能声称普通 handback 一定会重新开桥；需依据原生开始/结束时间及原缓存结果明确核对原 run 的恢复路径，不能直接改 journal、重建 run、伪造 artifact 或提交 verdict。实际恢复由唯一运行操作者在明确授权后执行。

## 独立复审收口

原生根结束与工具调用返回不能覆盖已观测的 live 未结束 child；保留原 inventory，并等待其明确 settled 后完成或释放。原生 user-role 项按宿主逐项元数据区分实际输入与注入上下文，缺失或不完整分类仍按实际输入处理，不按文本豁免。launch runtime 只证明启动身份，不证明旧版能力；原受管 send 尚未出现新 native turn 时保留旧根正向身份，先以原 request/text 对账，旧 turn 不算新轮完成。回归同时补齐配置为 Codex 的 E2E 原生输入/中断记录，保留原 journal 重启、同 run/session 与 owner CLI 只读断言。

缺少聚合 inventory 的旧 Orca 仍使用原 session 已持久化的 child/process 状态及身份；tool call/output 配对仅说明调用返回。原 child/process 的明确终态才能结束对应未完成事实，晚到回执与跨受管 continuation 不丢失原身份，命令 stdout 不充当宿主执行回执。
