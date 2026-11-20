# 从未结回执查询 manager Run 历史

- 在两个历史入口先读 `native_turn_receipts_pending`，按请求 ID 对本地和 runner 两张执行表分别做主键查询。只从命中行解析原 Run ID，再按 Run 主键关联。保留视图其余调用方的查询。
- 保留 manager 按 Task 取最近有效 Run、runner 按 Issue 取最早有效历史 Task 的顺序，以及物理关闭和 activity 一致性条件。manager 仍接受只有未应用事件的 Run，runner 的历史回执分支仍要求未结回执。保留 runner 的活跃回执优先级，沿用现有归属、冻结身份及原回执对账检查。
- 用正常恢复和 runner 选择入口验证两张执行表、有无匹配、仅事件、unknown、停机证明和原 Run／租约。用仓库 Go SQLite 驱动和实际迁移比较旧、新查询在不同已结历史与未结回执规模下的结果、执行计划和耗时。
