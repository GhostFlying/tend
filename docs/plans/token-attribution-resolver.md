# Token 归属的请求内批量解析

Refs: D158。

## 问题与约束

用量主 SQL 已有覆盖索引，但每个独立 turn 的归属仍反复扫描 run_logs 与本地 actor
来源。真实 schema84 的有界 fixture 已将这两类查询与主 SQL/JSON 分开计时，确认重复
扫描成本；生产 Token 单个样本仍约4.36秒。离线证据不替代真实部署读回。

## 最小实现

在同一次 TokenUsageFiltered 和同一只读事务内创建 resolver，不使用全局或跨请求缓存。
保留原字段、归属优先级与全部输出。run_logs 以 SQLite 原 JSON 表达式投影一次，按本次
current token 的 thread/turn 键保存唯一 Run 或多 Run 歧义，不缓存无关历史日志。缺失/
NULL/非文本 JSON 键保留原 SQLite 与字符串参数的比较语义，空字符串不等于 NULL。
本地 actor 来源也用原 SQLite 投影一次；direct thread 优先，parent/session 匹配随后按
原非 NULL 排序优先于 NULL。只有最高优先级投影唯一（或完全相同）时直接复用；同rank
不同actor时仍执行原 SQL，并只按完整 SQL 参数在本请求缓存其结果，不新造 tie-break。
任何批量读取失败都丢弃部分缓存并回退原查询，不将错误变成确定归属。

内存只保当前 token 的日志键与本地来源投影，不保存全部 run_logs；上下文继续控制阻塞
读取。原请求去重、计量事实、模型计费、未知计数和跨 Issue 过滤均不改，不增加迁移。

## 验证

复用冻结真实 actor 基线，比较完整视图：direct/parent/session、多匹配不同actor、NULL、
unknown、冲突、跨 Issue/Agent/Session；同thread多turn与独立thread分别测量，不将共享
thread 的缓存收益冒充独立thread收益。验证第二次请求能看到新的来源事实，并保留
批量错误的原 SQL fallback。私有 N100/200 实验每个measurement3秒封顶，不访问生产。
独立审查、定向race后先按已授权clean本地受控部署；正式 PR 完整检查与CI并行。
