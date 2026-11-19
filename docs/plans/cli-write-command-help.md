# CLI 写命令命名与帮助

## 问题与契约

排查 Issue 依赖时，`tend issue dependencies ID --json` 看起来像查询，实际经
`runIssue` 构建 `issues.dependencies` 信封。服务端要求显式 `depends_on` 数组并
全量替换依赖；缺少该字段的原调用已被拒绝。查询沿用
`tend issue show "$ISSUE_ID" --json` 的 `depends_on`。修复从 CLI 发现和
帮助入口开始，目标是让调用者在准备信封前识别读写、范围及参数。

## 实施与核对范围

- 在现有 Issue 路由和能力目录中加入 `issue set-dependencies`。保留
  `issue dependencies` 为兼容入口，两种名称走相同信封和服务端校验。
- `issue reparent` 与 `issue update-progress` 的动作名称已有动词，但子命令
  帮助只有共用参数。分别说明移到根级的空父级和清除进展的显式空字符串。
  `issue refresh-delivery` 会排队刷新观察记录，`issue deliveries` 才是只读查询。
  给这组入口加上参数、示例和请求恢复说明，并在 Issue 总帮助指向对应的读写入口。
- `host maintenance` 同样用名词触发写入，实际修改单台 runner 的新分配准入。
  用同一能力路由增加 `host set-maintenance`，保留旧名并在帮助指向 `host show`。
- 仓库内未找到调用旧依赖命令的脚本；能力覆盖表登记了旧名称。旧主机命令出现在
  能力覆盖表和干跑示例中，两处文档改用新名称。保留所有旧入口以兼容仓库外调用。
- 单列后续审计范围。`issue reuse` 会申请复用上下文或工作资源，配套查询是
  `issue reuse-status`。`issue followup <action>` 使用独立的规则目标、版本及规格，
  要区分列表、历史和调度动作。`maintenance config` 同一入口根据参数读取或写入，
  属于本机维护控制流程。上述流程各有独立合同和帮助缺口。

## 验证

通过实际 CLI 分发验证帮助无须连接服务、新旧命令信封相同、读取走 GET、
显式控制值下干跑不发 HTTP，以及缺省控制值时最多查询当前状态。使用本地
owner API 验证依赖替换与清空、缺省或 null 被拒绝、原 revision/epoch 冲突及
权限，并验证丢失写回执后查询原 Issue。运行能力覆盖与仓库影响检查，保留运行原件。

## 独立审阅后的修正

- 主机维护写入回执丢失时，从 `host show --json` 读取 `control_revision`
  和可选的 `maintenance.mode`、`maintenance.phase`。维护视图会合并服务端
  控制和 runner 上次上报。帮助说明初始修订值、缺省对象，以及读回不足以确认
  原请求时如何保留未知。用真实 owner API 和 CLI 验证常态、维护及旧上报投影。
- PR 绑定进展需要匹配同一条观察。帮助要求复制其中已有的全部基线字段。
  继续使用原候选比较和 CAS，补齐帮助断言并复用该候选的服务端语义检查。
