# Owner Issue交接与hosted目录CLI补齐

Refs: D121、D123。已有owner HTTP/app/协议不变，仅补真实CLI可发现入口：`tend issue assign`、`tend issue finish-assignment`映射现`issues.assign`/`issues.finish_assignment`，沿原Issue Envelope、首次读当前revision/epoch、发送前显示request ID和显式原ID/CAS重试；stale不偷偷刷新。交接payload与原owner API完全相同，不新增业务状态或自动完成交接。

`tend agents hosted`只读原`GET /api/v1/hosted-agents`安全投影；保`tend agents show --host`。目录登记不冒称runtime可用。本机worker`agents reload`已有socket/accepted snapshot语义，hosted仍按已批准合同仅启动/重启接受配置，不新增热reload/设置源/权限层。

实际CLI请求测试覆盖首次CAS、两操作Envelope/原身份重放、stale无隐藏GET、hosted目录/读取错误及帮助可发现。只定向CLI race/vet；候选独审及正式完整检查另记。
