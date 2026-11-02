# 消费 token 读取（D117）

消费用量与上下文窗口占用分别采集。`GET /api/v1/token-usage` 需要 owner 身份，支持 `window=day|week|month|24h|7d|30d|total`、日历周期的 `offset`（0 或负数）、`model`、`subject` 和请求明细的 `before` 游标。`subject` 使用 `run:<run_id>` 或 `conversation:<conversation_id>`。一个读事务返回全部聚合、请求明细、数据截止和账本 revision；明细每页最多 50 条，聚合始终覆盖整个筛选范围。

自然日、周、月固定在 Asia/Shanghai（UTC+08），周一开始，窗口为半开区间；浏览器时区不改变归属。日历周期结束点可以晚于数据截止。最近窗口以查询时刻结束。最终校正替换原请求贡献，不以校正接收日期重新归属。趋势只展示具有发生时点的已知请求桶；没有时间证据时不制造趋势。

## 请求、字段与去重

持久化执行以原生 `token_usage_record.usage` 为事实。分页格式使用 provider、原生 thread 和 ordinal 标识记录；旧格式使用 provider、thread、turn、response 和原生时间。另存原生内容哈希，同身份不同内容进入冲突核对。模型、Tend 归属、上报进程和接收时间都不产生新的消费身份。完整行才推进游标，重扫、恢复、压缩和续接文件中重复出现的原记录只贡献一次。

输入 Token 包含缓存明细；缓存不能再次加到总量，reasoning 也不再次加入输出或总量。可空字段保留未提供，明确提供的零仍为零；原生格式默认补出的零不冒充供应商实测。父 thread/turn 累计和 contextUsage 不进入请求贡献。显式 fork 的新原生 Session 独立统计并保留来源关系，同一 Session 的子 thread 合并。

执行模型来自对应原生 turn 上下文或当时的执行快照，并保留来源；不以当前配置回填历史。时间使用原生完成记录时间，采集时间另存。缺失或异常时间归入未知桶，在全时段统计中参与贡献，在精确时间窗口与趋势中排除。请求 final、执行结束与来源已对账分别表达，不由其中一个推断另外两个。

## 运行采集与兼容

主会话、咨询、Runner 执行及内部摘要每两秒增量追读，恢复时补读，writer 停止后继续对账。来源、游标和待上传批次持久化；重试只读取或上传，不重新执行模型，也不占 Agent 执行槽。跨主机 central-native 的原生文件由服务端采集，Runner 只处理本机已登记来源。

能力协商启用 `native-usage-ledger-v1` 后，Runner 用 host 身份独立登记来源并上传事实。未确认能力时保留来源，继续旧 `usage.request` 兼容路径；旧服务器升级后自动补发登记。旧事件适配器仍以 source/request_id 和 revision 接收完整快照，最终校正不能被 provisional 覆盖。新旧记录通过可证明的身份与数值关联排除重复，保留旧事实及修复证据；歧义不静默相加或清零。

`experimentalRawEvents` 只用于未支持原生账本能力的旧路径。该通知在恢复时的覆盖、模型和时间信息均有局限，不能作为持久化执行的完整计量依据。Orca 配额、上下文占用和会话累计不作为请求消费接口。缺失的原生文件、已消失的临时摘要和无法证明 Tend 归属的历史均明确披露，不能声称覆盖供应商全部消费。

## 验证

默认测试使用隔离数据库与脱敏计数 fixture，不联网、不调用付费模型。真实 Codex、TraeX 连接本地模拟模型服务的探针为 `go test -tags integration ./internal/harness/traex -run '^TestIntegration_LocalNativeUsage$' -v`；central-native 的跨主机边界探针为 `go test -tags integration ./internal/harness/remote -run '^TestNativeRemoteLedgerOwnedByServer$' -v`。Web 的 `pnpm --dir web test:usage` 构建真实应用并以隔离 API 检查两种视角、窄屏、筛选、分页、键盘与失败恢复；数字为虚构数据。

## 原生账本与修复

Codex、TraeX 的持久化执行以原生 `token_usage_record.usage` 为消费事实；上下文占用、turn/thread 累计值与原始通知均不再产生第二份贡献。恢复、压缩和同 Session 子 thread 使用同一读取器补采。旧事件保留审计，通过关联排除重复贡献；冲突和无法证明归属的记录单列。请求时间使用原生完成记录时间，采集截止另行展示。

`GET /api/v1/token-usage` 新增可选 `group_by=issue|session`、`agent`、`role`、`issue`、`session`、`sort=recent|total`；`details=1` 读取当前筛选的执行分布。无分组参数保留旧接口行为。CLI 同步支持这些筛选，例如 `tend usage --group-by issue --window total --json`；`tend usage sources --json` 列出已登记来源，`tend usage preview --file native-batch.json --json` 预览该批原生记录的有效增量（事务回滚，不提交事实），`tend usage reconcile <conversation_id> --file native-batch.json --request-id <original_request_id>` 应用会话用量并保留原请求用于恢复。JSON 批次需包含 `root_thread_id`、`session_id`、`samples` 和相应会话身份；服务端本地执行使用 `tend usage reconcile-run <run_id> --file native-batch.json`，禁止导入无登记的个人会话。

在来源主机执行 `tend-runner usage reconcile --native-home <provider-home> --provider codex --config <runner-config>` 默认预览；确认范围和差额后添加 `--apply`。托管会话改用 `--owner` 读取已有 owner 凭据。应用先持久化批次再推进游标，重启沿用待确认 request_id，不重放模型请求。输出分别提供新增、旧事实关联、冲突、缺失来源、Token 差额及应用后核对。

页面默认按 Issue，Session 为第二个视角；同一 Session 的多 thread 合并。列表在一个格中紧凑展示输入、缓存、输出和总量，所有数字使用千分位。价格按 OpenRouter 报价逐请求估算，以美元显示，缓存从普通输入中扣除后单独计价；模型或计价字段不完整时标记部分估算或暂无报价。它不代表供应商账单。详情提供执行模型、CLI 名称及版本、历史 Agent 名称、任务与业务跳转。

配置了内嵌 tailnet 的 Runner，维护命令通过运行中 Runner 的私有 Unix 控制 socket 转发允许的用量操作，复用现有网络与 host 身份；不注册第二个 tsnet 节点、不占用原节点状态目录。应用直接复用 `<runner-home>/runner/journal.db`。未配置 updater 时，Runner 在其私有状态目录创建专用用量 socket。正常 Runner 必须先升级并运行；此通道不允许代理模型、业务操作或任意 URL。

CLI 还提供无需 owner 登录的来源主机诊断：`tend usage host-sources <host_id> --socket <runner-control-socket> --json`；同一私有通道支持 `usage host-preview` 和 `usage host-upload`，使用 `--file <native-batch.json>` 输入明确的 Run、原生 root thread 和样本。上传支持 `--dry-run` 导出原 Envelope，重试保留 `--request-id`。这些命令通过运行中 Runner 的 host 身份发送，仅处理用量，不创建或恢复业务执行。完整目录和选项见各命令 `--help`。
