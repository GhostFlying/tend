# 消费 token 读取（D117）

消费用量与上下文窗口占用分别采集。`GET /api/v1/token-usage` 需要 owner 身份，支持 `window=day|week|month|24h|7d|30d|total`、日历周期的 `offset`（0 或负数）、`model`、`subject` 和请求明细的 `before` 游标。`subject` 使用 `run:<run_id>` 或 `conversation:<conversation_id>`。一个读事务返回全部聚合、请求明细、数据截止和账本 revision；明细每页最多 50 条，聚合始终覆盖整个筛选范围。

自然日、周、月固定在 Asia/Shanghai（UTC+08），周一开始，窗口为半开区间；浏览器时区不改变归属。日历周期结束点可以晚于数据截止。最近窗口以查询时刻结束。最终校正替换原请求贡献，不以校正接收日期重新归属。趋势只展示具有发生时点的已知请求桶；没有时间证据时不制造趋势。

## 请求、字段与去重

`TokenUsageSample` 是单一原生请求的完整快照；`source` 与 `request_id` 构成身份，模型、Tend subject、角色和进程不构成新的消费身份。`revision` 大于已有快照才可替换，final 不能被 provisional 覆盖。相同内容重传不增加消费；同 revision 内容冲突或相同原生请求被不同 subject/role 报告会保留追加事实、标记冲突并退出已知聚合，等待核对。当前没有自动清除冲突或人工改数的接口。

输入 token 包含缓存明细，显示顺序为输入（缓存读取、提供时的缓存写入）、输出。缓存不再加到总量；reasoning 不再次加入输出或总量。本版本不派生无法证明互斥关系的非缓存输入桶。可空字段保留未提供，明确提供的零仍为零；聚合同时返回每个字段的已知请求数。主数是已知子集，不表示全部平台消费。没有正文长度估算、费用换算或历史导入。

父 thread/turn 累计和 contextUsage 不进入请求贡献。原生子 Agent 只有在 thread/started 给出明确 parent_thread_id 时才归入所属 Tend 来源，并单列 subagent 角色；无归属来源不会猜测挂到某个 run。fork/resume 的继承历史不会再次计为新请求。主 Agent 的私有交接摘要 fork 同样只计新请求。

## 当前采集范围

已通过真实原生会话验证 TraeX 0.211.1 新建 thread 显式开启 experimentalRawEvents 后的 rawResponse/completed。fork 也显式请求该能力。记录规范化的输入、输出和总量，缓存明细只有原生 usageMetadata.metadata 中的原始 Responses detail 明确提供时才记为已知，不能把执行器默认补的零当作上游实测。任意 metadata 不作为日志或账本原文保存。

该原生通知未承诺实际模型与请求发生时点，本适配器不使用配置模型或接收时点冒充。未知模型分组为“实际模型未提供”；未知时点请求单列，在总共中计入已知数值，在精确日历/滚动窗口主数和趋势中排除。接收时间仅供同步核对。来源缺少总量时仍保存已识别请求及明确的字段缺失。

当前原生版本的 thread/resume 没有 experimentalRawEvents 参数，不能宣称恢复会话已具备同等覆盖。Orca 的账户额度和会话累计也不是请求消费接口，本版本未将其接成请求事实。未观测的子 Agent、恢复区间、未支持执行器和采集前历史均在覆盖之外；界面明确提示这些边界，不显示虚假的平台覆盖百分比。请求 final 只代表这条请求，不代表 run 已完整结算。

## 验证

默认测试使用隔离数据库与原生通知 fixture。真实请求探针为 `go test -tags integration ./internal/harness/traex -run '^TestIntegration_RequestUsage$' -v`，使用已有允许的模型配置，不读取生产会话。Web 的 `pnpm --dir web test:usage` 构建真实应用并使用本地隔离 API 检查窄屏、时间筛选、顺序、未知、校正和 rollout 分页；它的数字全部虚构。
