# Manager 控制回执关联查询

Refs: D157。

## 证据与纠正

旧运行产物的 CPU 栈返回地址通过精确 ELF 反汇编定位到 ManagerHandoffBlockers 的第三个
FollowupIDs 调用，即 hosted_coordination_effects 与 hosted_executions 的关联，再筛选
accepted/unknown request_records。此前把该返回行号归到 delivery SQL 是错误；delivery
的独立等价优化不能证明这一主热点消失，真实部署后 Board/snapshot 仍超时。
原始证据保留，纠正单独记录；不将旧 profile 冒充新产物的运行热点证明。

## 实施边界

真实 schema83 的离线 fixture 已复现相关子查询 `SCAN r EXISTS` 的二次方成本；
只追加 request_records(request_id,status) 覆盖索引即可改成精确 SEARCH，原 SQL 不动。
追加迁移0084，旧迁移与请求主键/去重规则不变。保留 issue/epoch 范围、原 request 状态、全部 local/runner
来源组合、去重及排序；不改领域条件、不减少未知责任。严格比较原新完整结果，超时的
原查询不能记为等价通过。补小型正式迁移/语义回归，保留每个 domain 的原请求状态。

## 验证与交付

独立短审查询语义和精确产物地址归属；定向 race。按已授权的本地 clean 受控验证路径
交付，正式 PR 的完整检查后补。真实页面性能须另按有界样本验证，不用离线倍率替代。
