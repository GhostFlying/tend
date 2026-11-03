# Manager handoff delivery 查询的重复执行扇出

Refs: D157；不改变 D156 的恢复保护。

一次受控 Board 请求的有界 CPU 采样定位到 ManagerHandoffBlockers 的 delivery SQL。
该查询把同一 logical turn 的执行历史自连接，再以 DISTINCT 去重；大量续接历史可能
先形成执行数乘积。已有 logical turn 索引不能消除这个语义上不必要的重复连接。

1. 使用真实迁移至 schema82 的独立现代 SQLite 合成库，为同一个 logical turn 生成
   1k/2k/4k 原执行与未决 delivery；逐规模有界计时、记录原始 EXPLAIN 与完整结果。
2. 先将属于旧 assignment 的 logical turn 去重，再读取其未决 delivery。保留 sending/
   unknown、operation 白名单、conversation 分支、epoch 边界及结果排序，核对严格结果
   等价。仅有本地证据才改正式 SQL，不用生产库或新增请求猜测。
3. 回归多个 logical turn、无 hosted link、不同 issue/epoch、已终态 outbox 及两分支
   去重，避免把重复消除变成责任或未知语义变更。
4. 由最小独立审查、必要定向及真实本地构建验证后，沿现有受控部署入口验证；正式 PR
   合入仍须最终完整检查与 required CI。若实证需要新增索引则追加迁移83，不改已应用
   迁移80至82；与 Token 查询子任务协调编号。
