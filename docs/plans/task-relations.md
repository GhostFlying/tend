# 任务后续与关联（D77）

人工检查已完成交付后仍可能提出修复。保留原任务的完成事实，创建有显式关系的新任务，独立实现与验收。关系属于任务定义中的元数据，不改变原任务 spec 或重新打开终态。

## 模型与写入

- 新增关系表：声明任务、目标任务、kind（follow_up 或 related）、创建时间；同一声明任务的 kind/目标唯一，禁止自身引用，目标必须存在。
- follow_up 表示声明任务跟进目标任务，允许多个前置任务，拒绝形成环；related 是普通关联引用，两个方向均可查阅，不承诺调度依赖。
- tasks.create 可携带 relations 数组，任务、关系、事件、请求去重同一事务；tasks.update_intent 可携带 relations，替换该任务主动声明的关系（空数组清除）；不删除其他任务声明的反向关系。
- 更新必须校验 expected_control_revision；真实关系变更递增 revision、记录 task.relations_updated，关系相同则不变。终态允许修改关系元数据，但仍禁止更改 spec、评级、项目或重新执行。关系不自动继承 workspace、分支或 base，也不形成完成/取消传播。
- task detail 返回关系 kind、direction（outgoing/incoming）和另一任务的 TaskSummary。保留双向可见性与声明者身份；不递归展开整张关系图。无历史关系回灌。

## 工具、上下文和界面

- 主 Agent 的 tasks_create / tasks_update_intent 支持同一 relations 字段；tasks_read、owner API 读取同一视图。上下文摘要包含任务关系，delta 含关系变化；主 Agent 根据原任务的真实产物确定新 spec 与明确 base，不假定原分支已经合入。
- 已完成详情增加“跟进 / 修改”：将原任务引用放入聊天草稿并聚焦，owner 补充问题后发送；不在点击时创建空白任务。详情显示前置任务、后续任务与普通关联，可双向跳转。
- 主 Agent 指令规定跟进请求创建 follow_up 关系的新任务；先查原 spec/产物/验收，再将修改目标和验收明确到新 spec。

## 验证

- app 覆盖已完成任务的新 follow_up、普通关联双向读取、缺失目标/非法类型/自身/重复/环、创建与更新回滚、去重、过期 revision、替换/清除与终态不重开；迁移保留既有数据。
- tools 验证 schema 与参数一致并实际创建/更新关系；API 与上下文验证结构化关联可读。
- 生产构建浏览器验证完成入口、草稿保留、关系标签/跳转和移动端布局；make check、最终独立 review、CI 后 rebase 合入，部署读回。
