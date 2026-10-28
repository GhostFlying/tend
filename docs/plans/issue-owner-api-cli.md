# P1c：owner Issue API 与 CLI

状态：已正式纳入 owner Issue API/CLI，基于已预审的 b3、b4 r2 与正式 b2b r2；候选独立预审通过，正式组合定向 race/vet、schema/types 与真实 CLI 路径检查已通过，正式 r1 CLI 两项 P2 已修复，r1 完整检查主动未完成；r2 已正式应用，定向 race/vet、独立增量复核及完整 make check 均通过；CI/合并及发布未完成。Refs: D121。已与 P2 Agent/Category 的 API/CLI/schema roots 语义合并并重新生成；固定 base 与验证见 [组合计划](issue-owner-delivery.md)。

## 产品入口

`GET /api/v1/issues` 只枚举有 canonical controls 的 Issue，可按 project、parent_id、state、终态过滤并使用分页 cursor；旧 work_item 不被自动采用。`GET /api/v1/issues/{id}` 提供 canonical 身份、revision、assignment epoch、父项、完成依赖和计划，以及 primary task、完成核对、实际未决来源份额和其精确版本。`?revision=N` 只提供不可变历史，明确标注 historical，不拼当前 task/source 动态事实。读或浏览不处置任何责任。

写入复用 `/api/v1/ops` 和已有 App.Execute 命令：create/update、pause/resume、cancel/finish_cancel、close/reopen、reparent/dependencies、review_sources/review_completions、create_task。新增 owner 合同刷新 update_task，先在同事务校 primary Issue 生命周期与 assignment epoch，再委托已有 UpdateIntent 的 task control revision、冻结、spec/artifact/action 代次规则。来源或祖先变化后用户有合法刷新与完成路径，不需要主 agent 审批；closed/cancelling 项不能借合同刷新暗自激活执行。

仅发布当前已实现的 owner 路径。P3 命名调度与 P4 manager 尚未启用，assign 不注册，类别本身不授执行角色。旧 work-items facade、API 字段和历史读取继续兼容；最终退役、旧 ledger archive 与新空 ledger 切换按总计划单独完成。本片不修改 runner 协议、领域核心表示或存储迁移。

## payload、schema 与边界

protocol 中定义真实公共 payload 与 IssueView/IssuePage，纳入 APITypes/APIEnums，沿已有生成器生成 JSON Schema 与 TS。新 Issue 表面使用 issue_id/parent_issue_id/state，内部不改历史 opaque ID。共享的计划/证据嵌套类型继续复用当前结构；已有 task detail API 独立保留。

OwnerTaskSpec 是“允许 owner 输入的字段投影”，不是另一份持久化 Spec：不含 server-frozen shared_context，文件输入仅指定 file_id，不接收其服务器冻结身份元数据。app 显式映射到现有 domain.Spec，继续原校验、workspace host pin、role 投影与正式验收能力约束，映射有完整字段回归。实现推理只进入 implement，独立 accept 仍只读取共享事实与必要产物。信任唯一 owner 和 owner 控制的主机、配置、数据库，不另建真实性证明链或 OS 沙箱。

APIResponse 增加可选 current_issue，stale 不改原请求；旧 current_work_item/current 保留。来源引用仍只是 owner 显式资料，不改变原作者或接管共享 main turn；来源份额的明确版本处置继续由已有命令原子执行。

## CLI 与恢复

新增 `tend issue` 命令，返回 JSON，支持 `--data` inline、`--file` JSON 文件或 `--file -` stdin。首次请求可读取当前 revision/epoch；自动生成的 request_id 以及实际使用的 revision/epoch 在 HTTP 发送前可见。复用旧 owner 写入的同 Envelope transport retry，不在重试中读新 revision、换 epoch、改 payload 或生成新身份。

```sh
tend issue create --data '{"plan":{"title":"Investigate","project":"default","intent":"Clarify the goal","stages":[]}}'
tend issue list --roots
tend issue show "$ISSUE_ID"
tend issue create-task "$ISSUE_ID" --file task.json
tend issue update-task "$TASK_ID" --file refreshed-contract.json
tend issue pause "$ISSUE_ID"
tend issue request "$REQUEST_ID"
```

恢复使用原 `--request-id`、原 `--revision`、原 `--epoch` 与原 JSON 输入；指定原 ID 时缺少原控制值会在 HTTP 前拒绝，不偷偷补当前值。也可先用 `--dry-run` 显式导出完整原 Envelope，再用 `tend issue replay --file original.json` 原样重发。默认不创建磁盘请求文件或第二请求账本。最终传输失败仍可能已提交，必须查询/对账原请求，不凭超时创建另一个 Issue。stale 只显示 current_issue/current，交 owner 重新决定。

## 验证

使用临时真实 App/HTTP/CLI 验证递归身份与历史、owner 无 stage task 的实现和独立验收闭环、公开合同刷新、来源份额读与取消收尾。真实 CLI 到 HTTP 在服务器已提交后连续丢失两次响应，恢复原 ID 只产生一个 Issue；同 ID 改 payload 被拒，stale 不 GET 后重建，配置目录没有隐式请求账本。CLI 导出/重放和显式原控制值另有定向回归；owner 输入投影映射、旧 owner UX、旧 work-item API 与 schema/types 一致性作为必要兼容检查。

仅进行定向验证和 TS 类型/生成器检查。没有页面布局/交互、UI 请求、真实模型、真实业务数据或部署；正式组合仍须独立 review、规定 CLI、唯一 make check、CI 与最终整体真实 E2E。

候选定向验证已完成：API/CLI/App/schema 的选定 race 回归通过，相关五包 vet 通过；提交 schema 一致性、TS typecheck 与类型生成器测试通过。schema 增量保留原有全部定义，旧定义仅 APIResponse 新增可选 current_issue；唯一 Web 文件是生成类型。上述定向证据只覆盖原候选；候选独立预审随后通过，正式组合完整检查与 CI 尚未进行。
