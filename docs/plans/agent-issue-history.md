# Agent 当前与历史 Issue 关联

Refs: D142、D160。

## 身份与事实

只读关联始终使用canonical Agent identity和执行当时run_agent_executions中的冻结pin，
不使用显示名或当前模型配置倒推。关联来源为冻结的run_issue_responsibilities、
issue_manager_tasks和任务创建时主Issue来源；缺少权威执行关联时显式说明覆盖缺口，不补造历史。

1. API按环境/local ID、当前/历史、Issue状态、标题检索与稳定Issue ID游标分页，
   同Issue去重，返回真实Run、角色、任务、原pin及观测状态。当前表示仍有未结Run或
   原执行结构化未结问题，不把unknown认作零、不用当前配置替代历史revision。
2. 专属protocol/views/api与完整CLI命令同PR实现，参数验证、帮助、能力目录、分页及
   JSON输出同步；只读不新增业务写权限、不改定义/pin/任务控制或数据库历史。
3. Agent详情加入当前与历史Issue入口、来源/具体执行展开、链接及加载/空/错误状态。
   Hosted用途和类别分别说明，缺少实时executor观测不写成离线或健康。
4. 真实SQLite和HTTP/CLI回归覆盖同名跨主机、历史revision、多run去重、unknown、
   未决决定、终态、分页/筛选和无关联；Web验证真实组件渲染与跳转，不调用生产。
5. 独立review及适当检查覆盖最终head，完整本地与CI按既有并行预算；最终经PR由
   集成责任合入，保持旧生产关闭并集中fresh部署。

## 独立审查后的响应式共享状态

Catalog作为唯一owner持有选中Agent的reader和筛选/分页controller，桌面与手机详情只共享
渲染此状态，不由CSS隐藏的面板重复发请求。新增实际浏览器断点切换验证：初次仅一次
读取，当前/历史与游标在1399px两侧保持，切换Agent或离开详情才关闭原reader。

全页导航回归的严格 API fixture 同步提供新关联读取路由，不把缺失 fixture 的 404
当成产品空历史。保留未登记路由拒绝，并运行 tour 与 Issue surfaces 的实际客户端导航。

## 问题归属审查收口

当前分类只由实际未结 Run 或结构化 worker question 的原 Run/action/spec/责任归属驱动。
普通任务级 open 问题没有 Run provenance，单独输出为任务的未结记录数，明确适用性未确认，
不归派给新 spec 的已完成 Run，也不以 AskedRevision 不同推定失效。回归覆盖暂停仍保留
原执行问题、旧 spec 普通问题不污染新执行分类、问题原账本保持 open 不被读取修改。
