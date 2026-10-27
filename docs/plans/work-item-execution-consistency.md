# 工作项执行事实一致性实施计划

## 目标与边界

本轮修复工作项只读投影的两个一致性缺口：逻辑终态任务不能遮蔽尚未物理收束的 run；同一计划 revision 下，较旧事务快照不能覆盖较新的执行事实。工作项阶段仍是可编辑计划，任务 lifecycle 仍是业务结果，run observed state 仍是执行事实，follow-on 与完成核对仍表达持续责任。修复不自动改写阶段、关闭工作项或伪造计划 revision。

这是 owner API 的向后兼容加字段和 Web 合并规则，不改变 server/runner 通信协议。

## 实现

1. 调整任务执行投影的优先级：先读取 run。`running`、`waiting`、`starting`、`interrupted` 或 `unknown` 的 run 继续暴露 run ID、角色和物理执行状态；只有全部 run 已为 `finished`/`failed`，或从未分配 run 时，才用 `completed`/`cancelled` 表达逻辑终态。
2. 聚合和白板列只消费同一任务执行投影。未收束 run 计入运行、暂停或待核对；停止事实收敛后才计入完成或取消。计划阶段和持续责任均不在读取路径中被修改。
3. 在同一 SQLite 读事务中读取 `events.event_seq` 水位，并随当前 `WorkItemView` 返回 `facts_seq`。所有会改变当前投影的事务（包括 delivery query reservation）都记录事实事件，使水位覆盖可见变化。它只排序动态事实快照，不替代或递增计划 revision；历史计划读取不带当前事实水位。
4. Web 以 `(revision, facts_seq)` 合并同一工作项：更高计划 revision 优先；同 revision 时更高执行事实水位优先。board、overview、detail、按 ID lookup 和共享引用都先经过同一合并器，旧响应可以完成自己的请求，但不能让任何可见 store 倒退。
5. 保留 `WorkItemRead` 的 generation、AbortController、dirty follow-up 和退避重试语义；合并只发生在一次已获准提交的响应内。

## 验证

- 真实 App/SQLite 流程覆盖：running 后取消、lease 过期为 unknown 后取消、停止确认后收敛；并保护普通排队取消、完成/失败、验收和重试代次。
- views/API 测试确认 `facts_seq` 来自同一读事务，计划 revision 不因执行变化而改变，overview 与 detail 均携带可比较水位。
- Web 测试用两个独立 deferred reader 覆盖旧 overview 开始、新 detail 提交、旧 overview 后返回；再覆盖相反顺序、新 revision、重复旧响应、lookup、board/overview、refresh dirty、abort、retry 和分页形态页面。
- 运行聚焦 race 测试、Web 单测、`make check`、Linux/Windows/macOS 交叉编译，以及仓库规定的独立 `codex review`。

## 交付

仅提交本计划、必要源码、生成协议和测试；使用现有 Git 身份推送当前分支并创建 PR。合并、部署和生产账本变更不在本轮范围。
