# 原 repair 回复前缀的内部消费确认

基线：`9da3e49b1e16a9ec89c805a5e440b161608bb13c`。Refs: D169、D155、D156。

## 现场语义与范围

已失效 manager 的数据库回放入口能够识别原责任，但原 turn 属于 logical repair。普通回复路径会刻意抑制 repair 文案；因此它既不产生用户可见正文，也不满足普通回复的持久化前缀确认。不能用发布内部 repair 文案、伪造 native 终态或清理业务未知来解除原 close 的守卫。

本次仅补数据库回放中 repair delta 的真实消费边界。原始事件已经持久化，消费者必须实际读完固定前缀；随后在同一写事务重核原 run/thread/turn/item、原 pending execution 的 repair 身份与实际消费序列，保存不含正文的内部消费审计（来源、游标集合、累计内容摘要），再确认原事件。普通回复仍必须有精确持久化终态正文。原生回执继续沿原信封自然重放；pending business receipt、原业务 unknown、维护保护及 lease 状态不因此改变。

## 实施步骤

1. 复用真实 BackgroundPool 故障回归，把原执行设为 repair。保留稳定 RED：固定尾部 delta 无消费者完成确认；用户正文缺失本身是抑制策略的预期。
2. 为 database-only attachment 增加内部 repair-prefix 确认，不创建 harness Session，不执行 Models/native RPC，不新建 Run、不续租、不替换 pin，不制造末事件或扩大原 close 游标。
3. 确认必须绑定原持久化 repair execution 和原 native delta，拒绝错误 run/thread/turn/item、缺失序列、非 repair、伪累计内容和未实际消费事件；审计与 applied 标记原子提交，重试不重复制造审计事实。原始 delta 保留。
4. 回归正常回复、内部 repair 的用户输出抑制、原 close 同 ID/末游标、业务 pending 保留、晚到边界外事件、写失败及身份负例。设计变更同提交补充 D169。

## 验证与交付

先故障 RED，再定向 race。独立 Sol/xhigh review 绑定实际新主干；所有真实 finding 在最终候选修正。PR 可与 CI、本地完整 make check 并行，合入必须完整本地检查、规定独立 review 和精确候选所需 CI 全部通过，再 fresh main / rebase 线性合入。唯一 operator 受控更新 server，复用兼容 runner/updater；验收内部消费审计与原始事件确认、原 close/reply/stopped 自然收敛，并保真报告仍未对清的业务未知。用户可见设计产物不由 repair 文案替代。
