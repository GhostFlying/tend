# Agent 自主读取 Issue 上下文

状态：实施中。Refs: D191。

## 目标

负责 Issue 的 Agent 默认能够自行取得完成目标所需的已保存材料。实现、验收及后继 session 使用相同的普通读取能力，不因原件未进入最初 spec.inputs、旧 Run 已结束、报告已提交或执行阶段变化而重新办理文件授权。

Issue 承载持续目标、相关材料与责任；Task/spec 和 Run 保存执行承诺及当时采用的输入。历史快照不兼任普通资料阅读白名单。读取保留来源、版本、时间和原件哈希，Agent 判断资料意义，读取本身不执行新的业务动作。

## 实现

1. 增加通用 `read_context` worker 工具及 `GET /runner/v1/context`。由当前 Run 推导所属 Issue 与已有的关联上下文，复用账本视图读取当前定义、来源及历史状态、消息、任务、产物、验收、问题与文件索引。支持明确分页及按对象读取，不把第一页冒充全部历史，也不自动把未处理消息当成新指令。
2. 元数据使用 `GET /runner/v1/files/{id}/metadata`，新增普通原件下载 `GET /runner/v1/issue-files/{id}`。元数据、索引与下载共用 Issue 材料解析。既有 Issue 消息、背景来源、继承资料及关联任务的原件自然可读；没有 message_id 的真实背景文件按其已有资料关联读取，不强求重建一条 owner answer 授权链。
3. runner 的普通 `download_file` 在初始 spec/artifact/question 元数据未命中时，查询通用资料元数据并复用大小、SHA 和原子落盘校验。新 session 重新从 Issue 取得材料，不依赖旧原生会话、旧 lease 或单次补件 grant。保留原输入快照和旧问答授权历史，不增加 grant、Assignment 标记、调度能力门或迁移表。
4. Owner API、`issue materials` CLI、Main/Issue manager 的 `issues_materials_read` 共用资料视图；已有已登记交付和部署事实作为普通关联资料可读，读取当前部署准备事实复用现有同源只读能力。Capability 定义与历史、Agent 能力声明及当前 Task 的匹配解释同样复用已有只读查询，作为正常上下文提供，不要求额外 observation 标记或逐类资料授权。沿用原件及 Task/Issue 真实关联，不把读取当作新的部署权限或要求另建调查任务。
5. 原 `read_observation` 和无新模式的文件请求保持旧语义。新增普通读取能力不收紧旧有效请求，不删除字段、不改 Assignment 或 journal 格式，协议仍为 2、journal 为 19。新 runner 面对不支持新接口的旧 server 如实报告不可用，保留旧输入读取，不转用 owner 凭据。

验收读取继续遵守已明确的 `implementation_context` 私有字段约定，其余公共资料正常可读。沿用 D90 不自动注入可选作者报告的初始上下文规则；验收 Agent 主动请求读取已经提交的报告时可自行查阅，并独立核实其中的主张，作者自述不能代替验收证据。所有已关联 Task 保留的上下文快照都参与同一原件索引，负责人和新 session 可以重新取得历史原件。

默认概览同时支持仍保留的历史工作项，以 `legacy_contexts` 返回其原身份与内容，和 canonical Issue 定义分别表示；读取不会迁移旧记录或授予新 Issue 控制权。

## 验证

真实链路覆盖：原 Task/spec 未列出的 Issue 原件可发现、可读；验收及问答后继 Run 无需改 spec 或授补件权限；背景文件无 message_id 仍能按真实关联读取；创建新 session 可以取得当前及历史资料；索引与按文件读取同源；旧请求和原输入下载保持；普通读取不修改原 spec、artifact、批准、结论或调度状态。

所有正常读取均保留实际来源与分页，缺失和失败如实返回。完整 CLI 输入、输出、帮助与请求语义同步；接口及生成类型统一生成，不手改。最终候选统一完整 `make check`、独立 review、必需 CI 和 rebase 合入后，从实际合入源码构建并受控部署服务端及 runner；随后由 Tend 沿原问题完成原验收和 Issue 收尾。

## 后续统一设计

整项独立审阅已确认：来源指纹默认停工、普通工作工具整体隔离、普通报告默认独立验收、未来 reviewer 阻止当前实现、固定 review 轮数的人审门、报告补正死路、强制阶段 Task 切分及默认 app-server 恢复缺口，需要按持续责任与 Agent 自主判断统一调整。它们不通过本次再增加专用授权状态来解决；当前先恢复普通上下文与原件能力。
