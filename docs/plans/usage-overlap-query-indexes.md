# Token 重叠证据查询的反向索引

Refs: D157。

## 已证范围

真实 schema82 的隔离 SQLite 复现中，原 Token 查询对每条 current fact 的重叠检查
反复扫描 old_current。legacy/native 各 1k 到各 2k 时主查询约从 2.13 秒增至
9.42 秒；达到单级十秒预算后停止扩大。该证据解释用量详情的独立性能缺陷，
不把它等同于 Board/snapshot 的 manager delivery 扇出。

## 实施与验证

1. 仅追加迁移83，为 legacy candidates 的 native source/request 反向访问与 current
   的 fact sequence/conflict 关联增加覆盖索引；不改 usage SQL、原始事实或迁移80至82。
2. 隔离复现中保持同一 SQL、完整 row 集与用量视图，核对两索引前后结果相同。
   原生匹配去重、冲突后恢复 legacy、未决重叠及未知计数的覆盖语义必须保持。
3. 正式回归使用小规模真实迁移升级，确认原用量/旧迁移记录不变、完整视图等价，
   EXPLAIN 使用新增覆盖索引并避免相关子查询逐行扫描 old_current。不把慢基准放入单测。
4. 与 manager delivery 最小修复统一构建及受控验证，正式合入仍执行既有检查。
   本地离线加速不代表生产详情延迟或全部页面恢复已获验证。
