# P1a：Issue 内部 Go 命名整理

状态：内部改名代码、本地验证和独立 GPT-6.1-Sol xhigh review 已完成；尚未完成本批 PR CI、合并或部署。Refs: D121；Plan: P1a。固定 base 为 `5b20a8ad4536cb70341607066452a07e487a702e`，依赖 P0 设计合同（PR #155）。总计划见 [Issue 与 Agent 重构](issue-agent-refactor.md)。

独立评审和验证对应的冻结实现快照 SHA-256 为 `b94229bf4efe0dc91932358af1091004951c7b101be73442fb3dc288863b218f`；随后仅更新本计划的状态记录及总进度，代码未改变。普通和 integration tag 全包编译、既有定向测试、完整 make check 均通过。文件改名进入索引后单独重跑格式检查，确认现存路径完整覆盖；API schema、Web 类型和工具定义重新生成/独立比对均保持原字节。此记录不代表后续 Issue 行为已实现或发布。

## 目的与边界

将内部领域规则、存储函数、app 命令类型和视图函数统一为 Issue 命名，为后续递归责任与 assignment 行为建立可读调用面。此批是机械整理，不新增树、责任人、生命周期、调度权限或公开 Issue API；不把改名视为领域行为完成。

保留 protocol 的 WorkItem DTO 声明及 JSON 字段、schema 反射名、现有 API/工具名、operation/event 字符串、SQL/表名、ID、迁移和不可变历史。现有 schema generator 使用 Go 反射类型名，工具导出也识别 WorkItemEvidence definition；本批不改变这些外部契约，不写 aliases 充当完成证明。Web 源码和生成类型应保持原字节。

## 确定的改名

| 层 | 实际改名 |
| --- | --- |
| domain | ValidateWorkItem 及 evidence/closure/status/disposition/dependency 规则改为 ValidateIssue 系列；WorkItemPlanChanged、TaskPlanChanged、TaskRetired、StageAcceptsWork 及内部 helper 改为 Issue 系列 |
| store | Get/GetRevision/List/ListTask/SaveWorkItem 和内部查询改为 Issue 系列；GetTaskWorkItemOwner、TaskWorkItemLabel、InsertTaskWorkItemOwner 改为 TaskIssue 系列 |
| app | Create/Update/Close/ReopenWorkItem、LinkWorkItemTask、BindWorkItemTask 改为 Issue 命令；读写、来源、证据、disposition、closure、task context/dependency 和通知 helper 同步改名 |
| views | WorkItem、WorkItems、WorkItemColumn、WorkItemContext 和 ApplyWorkItemDependencies 改为 Issue 系列；内部投影 helper 同步改名 |
| 调用方 | app、store、domain、views 内部调用和 API/tools/contextbuilder/mainthread 测试/组装使用新 Go 名；外部路由和工具身份保持原值 |
| 文件 | domain/store/app/views 的 work_items 源码及相应测试改为 issues；app task_work_items、work_item_notice 和 views work_item_context 改为 task_issues、issue_notice、issue_context |

标识符变更由显式映射和 Go AST 位置确定，不在文本字符串中替换名称。字段 selector、结构字面量 key、外部 protocol DTO 及被局部变量遮蔽的 import 不改。导出函数/命令的关联 Go doc 开头名称随声明改名；其他注释和用户可见文字保持。

现有 task_work_item_owners 是创建时 primary provenance，本批只改 Go 访问函数名，不另建 ownership 表、不根据 stage 或当前链接重算归属。递归树、无 stage 的 primary 领取/关闭和 canonical controls 属于 P1b，必须另写计划、追加 migration 和行为测试。

## 验证

1. 固定 base，保存生成 API schema、Web API 类型和导出工具定义的原字节/hash。
2. AST 转换逐文件检查非标识符 token 完整相同；Go doc 名称的受限修正单独核对。代码没有字符串、字段、控制流或历史 migration 变化。
3. 重新生成 API schema/Web 类型并独立比对原字节；导出工具的完整 JSON 与 base 相同。Protocol、迁移、Web 源码无 diff。
4. 编译全部包和 integration tag 测试代码但不运行真实模型测试；运行相关原有 domain/store/app/views 和 API/tools/contextbuilder/mainthread 测试，不添加只镜像改名的测试。
5. 完整 make check、独立 GPT-6.1-Sol xhigh review、PR CI（包括 Windows/macOS 交叉编译）；基于固定 base/head 记录评审和验证，不沿用另一个 head 的结果。

## 兼容退役与交付

此批未新增 runner 通信适配或 wire 默认语义，不需因 Go 函数名改变提升通信协议。外部 WorkItem DTO 是当前仍在使用的正式契约，不是已切换 Issue API 的伪装；它的活动入口按总计划 P6/P7/P8 就绪时退役，历史 archive reader 保留。若后续新增协议适配，另登记支持代次及退役版本。

本批独立 PR、原子可构建提交，不部署。实施者完成后冻结 patch 供另一 reviewer 评审，处理意见和完整检查通过后由主协调推进提交/推送与线性合并。
