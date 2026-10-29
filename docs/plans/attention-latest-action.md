# Attention 最晚行动时间（D125）

Attention 的 latest_action_at 是该问题/行动的最晚处理时间，区别于 planner.expected_at 和 Issue deadline。原Attention body/revision/history增加可选instant毫秒与timezone；create省略为未知，update省略保留、0显式清除。timezone仅显示变化且instant相同不重建提醒；read/archive不是回答，不取消业务期限。当前owner命令就是授权，main转录仍沿原owner来源规则，原decision/task/assignment语义不变。

完整复用 followup target/occurrence/Sweep/outbox。增加attention target的现有规则，默认近24h和到期唤醒main；非Issue Attention也可使用，不造假Issue。仅期限/提醒配置真实变化重建未来规则；已claimed/unknown保留原身份对账、known-unsent可取消，停机跨近/到期窗口跳过过时未发near只due一次。prepare不新派提醒，恢复沿原规则继续；回答/withdraw清未来提醒，负责人换人不重置时钟。

实际基线已含 P6 的0061，追加0062迁移只将原 followup_rules/occurrences Issue FK可空。SQLite事务在FK开启时仅defer并重建父表会失败，因此同事务按依赖复制/重建原五表，旧字段/history/index/unique/FK逐值保留，仅真实无Issue Attention写NULL。先核真实旧60与当前61schema所有foreign_key_list，实际非空旧库升级/外键拒绝/历史值对照验证；不改历史0040/0059或migrator/FK开关，不泛化新target框架。原generic规则控制仍要求原Issue，Attention-owned时钟只经Attention命令控制。

实际store nullable扫描、Sweep与event reconcile在GetIssue前按attention target分流；既有Issue deadline和普通followup路径不改。API/CLI/hosted schema/生成类型沿同命令增加字段，先真实无Issue near/due/restart/unknown/取消/prepare及旧规则回归，再冻结独立review。UI仅获得真实字段合同，当前不在此片修改可见组件。最终依赖组合还须纳入独立CLEAN r15及全部既有union；本片不改root/旧freeze/原P9 stopped账本。


当前隔离片已落地 owner API/CLI/hosted 的同一创建/更新 payload、AttentionView 读取、真实 nullable store 与原 followup 消费者；尚未正式纳入、完整检查、CI/合并或部署。生成 schema 仅 AttentionView/CreateAttentionPayload/UpdateAttentionPayload 三定义变化，未改 runner wire、原历史或 UI 布局。

提醒配置 latest_action_reminders 是输出的显式布尔值；首次给出正时间默认开启，可明确关闭。latest_action_at 未知时输出省略，update 省略保留、0 清除。时区只改展示，不改原 instant/规则身份。引用已有 decision/choice 时继续返回同一 Attention；若本次企图修改其时间，需改用该对象 current revision 的 update，不成功忽略输入。

旧 unknown 回执阻止新 occurrence，原索引 next_due 只延期30秒并在重启后保留，不更改旧 occurrence 或发新通知。Sweep 对确有此延期的事务必须提交无通知结果；不能走会回滚的 NoEffect。原 Issue deadline 与 generic followup 的既有暂停/对账语义保持，单列定向回归。独立 review 应核这处事务收尾，以及迁移五表原值/FK/unique/history保全；不将纯存储字段或服务启动当真实产品验收。

定向 race 已通过实际旧60/61非空升级、无Issue near/due主路由、真实store重启与prepare、跨两窗只due、原native unknown/receipt对账、assignment和时区不重设、0清除、原decision回答取消、托管main创建、CLI/HTTP与回放，以及原deadline/generic跟进。vet、目标staticcheck、TS类型和生成一致性检查通过。候选等待独立复核；最终还需组合 r15/r16 和当前其它已审前件，旧 P9 stopped 原责任不在此片恢复。
