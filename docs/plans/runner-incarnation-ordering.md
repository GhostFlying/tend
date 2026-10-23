# Runner 进程代次排序

## 问题与边界

随机 instance ID 不表示启动顺序。旧进程首次 hello 延迟到新进程之后时，
不能将新进程退休。故障模型是崩溃、网络延迟与并发启动，不依赖墙钟排序。

## 实施

- Runtime 增加正整数 Generation。同一 instance 的代次固定；只有更高代次的
  不同 instance hello 可以替换当前实例。sequence 继续仅排序单进程观察。
- journal 包在独立 `.incarnation` SQLite 文件中原子分配代次，FULL 同步。
  文件位于 journal 旁但不属于可回滚业务 journal 的备份恢复集合，不能随着
  journal 降级恢复倒退。每个 runner 进程首次加载控制时仅分配一次。
- 服务端沿用 Runtime JSON 持久化当前高水位，不新增服务端表。协议 0 的无
  Runtime 首次兼容不变，已经登记版本化 Runtime 后不能降级抢占。
- 测试覆盖延迟首次 hello、相同代次不同实例、同实例代次篡变、并发分配、
  重开文件与业务 journal 恢复；更新 fake hello 的显式启动代次。

## 运维与丢失恢复

`<journal>.incarnation` 必须跨版本、候选回滚和业务 journal 恢复保留。
不得将它加入 updater 的业务 DataFiles 恢复集合，也不得通过删除它重置身份。
文件丢失/损坏时先停止该 runner/updater 的启动重试，保留业务 journal 和现有
companion 证据。优先恢复该 companion 的较新备份；若没有可用备份，管理员在
确认该服务所有进程停止后，读取服务端当前 Runtime.Generation，将 companion
计数显式恢复至不小于该高水位，再启动（下一次原子分配为高水位加一）。
这是本机进程身份元数据的离线恢复，不修改任务账本，不重新发送业务动作。
服务端拒绝低代次时不得自动向上重试，否则真正迟到的旧进程也会复活。

旧 pending claim 按原 envelope/request ID 回读，不改 payload/hash。正常启动先
resendClaims 后 hello，因此旧实例尚为当前实例时仍可回读；已经被别的实例退休
的旧请求继续按既有规则拒绝，不用新代次伪装重发。hello 的原有账本对账恢复
该主机已分配但本地尚未接收的 run。
