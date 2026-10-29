# 非可见 Web 数据层（D121、D124、D125）

本片接通已实现 Issue、Attention/Next、逻辑对话和精确 run 的 TypeScript 数据/动作适配。只修改 `.ts`、测试及设计计划，不改 JSX/TSX、CSS、可见页面布局、交互或导航；对应页面仍按已批准设计单独接入。Category/runner库存由独立只读catalog小片固定接口后另接，不在此片假造全集。

复用 ApiClient、Reader、Store、WorkItemRead、现有 SSE/reload/stop 和 DurableOp。事件仅失效读取，不在客户端重放账本业务或重算完成/调度。新读取懒启动，路径/对象切换和stop会取消旧请求，迟到响应不能覆盖当前数据。不新建缓存、持久请求或并发框架。

Issue根/子列表保留服务器分页、树关系和具体负责人；详情/历史不混用。Attention在chat/Inbox/detail保持同ID，回答使用原question version；读/归档不回答，Next保留服务器顺序，不乐观结算。owner控制Envelope固定revision/epoch，unknown恢复沿原DurableOp身份，不因重读自动重基；补齐runOp原先丢弃的current_issue/current_attention。

对话保持真实conversation_id和既有SendQueue/草稿身份；普通consultation讨论无Issue/task，前端不自行按文本触发业务，保留owner向咨询agent提出执行要求后其正式工具委派路径，不要求另加手动按钮。精确run补充不改spec/generation或转投新run；native-entry GET只读不接管。手工资料保留外部作者与authenticated提交者区分。

验证聚焦迟到覆盖、unknown重放ID/revision/epoch、current_issue、Attention原答案身份与精确run；复用已有DurableOp的部署/跨标签验证，不为简单payload逐一镜像写测试。完成定向Vitest、typecheck、lint/format及生成当前性检查，再冻结独立review；本片不作可见UI/浏览器/真实模型验收声明。

状态：核心非可见数据层候选已完成，新增和受影响原路径定向 Vitest 共 41 条通过，TypeScript、修改文件 lint/format 与 schema→TS 当前性检查通过；独立 review、正式组合完整检查、可见页面接入与部署未完成。库存适配等待固定薄 DTO 单独增量，不阻塞核心候选审查。

实际校验保留首次测试可空 items 的 TypeScript 失败及其可选链修正，不冒称首轮全绿。原问题身份与精确 run 补充通过现有 DurableOp 记录原 Envelope；unknown 时新点击不能替换目标/revision/epoch/payload，重载对账只沿原请求。withdraw 使用原 Envelope.reason 和空 payload，不伪造成 action confirm/decision answer。逻辑会话列表按已打开视图在普通 outbox/Issue/消息事件失效，读取状态仍来自服务端。


核心 r1 独立 review 完整检查 14 文件，仅一项正常路径 P2：Issue 的 primary task Execution/queueReason 会因 run/host/workspace 事实变化，而原新失效名单只包含 task 等对象。真实租约到期只发 run.unknown，runner 恢复发 run.recovery，维护/工作区也有独立事件。r2 沿原事件入口对已打开 Issue 调用既有 refresh；未打开不额外 GET，不新增 timer，不客户端重算事实。Attention/Next 当前视图仅依赖原 question/choice、planner/deadline/source 版本，不因这些执行事件新增读取。

该路径在 r1 稳定复现旧 Issue 留存 running，修后定向 Vitest 共 42 条、TypeScript、修改文件 lint/format 通过。r1 原快照保留；r2 独立增量核对与正式完整检查仍待完成。


## P2e 只读库存增量

在核心 r2 之后，以已独立审查的 owner catalog 薄 DTO 为唯一类型来源，新增 category 分页、hosted 登记和指定 host 的 public inventory/最后 executor 观测。复用原读生命周期、SSE/reload/stop，不加入 runner token、请求对账、CLI 探测或缓存/轮询。登记、executor 最后可用事实和 host 在线保持独立；本片没有配置/授权写入或可见页面变更。先写本计划，再实施和验证。

该只读增量接通 AgentCatalog 的 category 分页、hosted 投影和当前指定 host 的 RunnerAgentCatalog，并纳入原 LiveData 事件/reload/stop；未打开的读取保持懒启动。定向 Vitest 5 文件共 44 条通过，TypeScript、增量 lint/format、真实生成类型当前性检查通过；尚待独立增量 review 与正式组合完整检查，没有可见页面或部署验收。
