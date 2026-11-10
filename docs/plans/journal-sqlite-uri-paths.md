# Journal SQLite URI 的真实文件路径

## 问题与边界

D216：journal 先按真实路径取得 writer 文件锁，再将未经编码的路径拼入 SQLite
`file:` URI。路径中的 `#`、`?`、`%` 可被解释为 URI fragment、query 或转义，
使 SQLite 打开另一个文件，绕过私有测试目录并与 writer 锁的 inode 不一致。
旧版本拒绝只是实际读错历史数据库的后果，不能通过改测试名、删历史文件或
放宽版本检查解决。

## 实施

1. 先用独立临时根构造绝对/相对字面特殊字符路径，在原 URI 会误开的文件中
   写入较新版本，确认原 Open 的确定失败。所有种子文件均归本测试，不读写
   外部历史孤儿数据库。
2. 使用标准库 `net/url` 构造完整 file URI，原 pragma/事务选项作为 RawQuery。
   保持调用方相对/绝对文件路径语义，SQLite 主库与 acquireWriter 对应同一
   实际文件。进程代次数据库使用同一编码方式，仍保持独立持久文件。
3. Store 读写池的 dsn 也使用相同标准库编码语义，保持原 WAL/foreign_keys、
   busy_timeout/synchronous、writer immediate 和 reader query_only；不增加共用
   package。用真实 dsn/sql.Open 的极小表验证，避免重复整套迁移。
4. 不变更版本、迁移、wire、预算、锁协议或身份；不删除或合并已存在的其他
   文件，不扩展数据库性能或存储布局功能。

## 验证

实际读取 `PRAGMA database_list`，将主库文件与 writer 文件的设备/inode 比对；
记录请求并关闭重开，确认原请求及独立递增进程代次保留，误开文件仍保持原
较新版本且没有 journal 表。覆盖 `#`、`?`、`%` 的绝对与相对路径。运行新回归、
Store 读写池、原空 writer 用例及已有 writer ownership/进程退出边界的
targeted regular/race/vet。
定向验证不替代最终候选的完整 `make check`、独立审阅和 CI，由协调者统一执行；
原失败原件和孤儿文件保留。


## 独立审阅后的 Windows fixture 修正

当前 Go 1.27 的 URL.String 已为 OmitHost 的前导双斜线编码，纯 URI 消费端
回归在初版产品上实际通过，不能将旧版序列化行为当作本次 UNC authority
回归。保留产品 Path + ToSlash + OmitHost 构造；ToSlash 是 Windows 的合法
分隔符规范化，不要求反斜线逐字保留。

实际修正限于测试 fixture：seed 也使用有编码且无 authority 的 file URI，
避免 Windows 原生 drive 路径被默认 URL.Path 序列化成 host；Windows 明确
跳过非法 `?` 文件名，`#`/`%` 仍执行。新增纯 URI roundtrip 同时接受 Path 与
Opaque，核对合法平台分隔符语义、空 Host 及不变 query；覆盖 drive/两种 UNC
表示与 Unix 绝对/相对路径。原 Linux 文件、锁、请求和代次检查保持。
这些 Linux 与纯 URI 结果不冒充 Windows 原生文件系统或网络共享已实测。
