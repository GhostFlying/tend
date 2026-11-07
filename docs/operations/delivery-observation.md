# Server 交付观察（D185）

服务端的 `delivery_observer` 配置对象显式启用程序观察。同一 provider、repository、PR/MR 编号共用一次外部读取；各 Issue 保留自身交付引用、代次与授权，事实通过既有事件合并窗口通知当前负责人，不启动额外的轮询 Agent。

配置字段：

| 字段 | 含义 |
| --- | --- |
| `providers` | 启用 `github`、`codebase`，可同时配置。空列表将刷新结为 `provider_not_configured`。 |
| `gh_bin` | 已安装的原生 GitHub CLI 的绝对路径。 |
| `bytedcli_bin` | 已安装的原生 Codebase CLI 的绝对路径。 |
| `timeout` | 一次完整 provider 读取的总期限，默认 `30s`，允许 `1s` 至 `60s`。 |

凭据沿用服务用户的原生 CLI 登录，不复制 runner token、GitHub token 或其他用户凭据。启动时仅诊断可执行文件是否存在；鉴权与真实对象访问由实际读取证明。`tend doctor` 显示安全配置摘要；`tend issue deliveries` 查询 lifecycle、head/base/target、mergeability、CI、检查时间、失败与重试信息；`tend issue refresh-delivery` 发起一次有界刷新。CI 绑定实际检查的 head，跳过检查单列，分页截断、未知结论或候选漂移不当作通过。Codebase 辅助检查失败时保留已验证的 PR/MR 生命周期，将 CI 标为 unknown 并保留错误类别。

单次读取的持久租约为 90 秒，读取与保存重试均受其预算约束。瞬态失败最多进行三次 provider 尝试，间隔 15 秒、30 秒；耗尽后清除 pending，显示失败并冷却 15 分钟。缺 provider、CLI 或登录失败直接结束本次刷新，修复后可显式刷新。普通开放交付每五分钟读取；CI 仍在执行时每三十秒读取；终态交付停止自动读取，显式刷新仍可执行。每批最多向一百个关联 Issue 分发，较大的关联组继续复用原事实与原检查时间。共享读取开始前已排队的显式刷新也共享结果；之后新来的刷新以持久单调版本区分，保留为下一次读取，不依赖毫秒时间猜测先后。

配置对象缺省时保留旧部署的 runner 协议行为。启用 server 观察后，旧 runner poll 仍是合法请求并返回空分配；已有 query 先等原截止，再保存其 nonce、host、instance、generation 和未知结果，随后由 server 接管。旧报告保留为迟到历史，不覆盖新 server 事实；同协议请求结构及既有有效输入保持。切换无需先启用 runner observer，也不要求没有普通任务或待确认 claim。

数据库迁移只追加第 101 号迁移。已有 Issue 的交付引用一次性回填，此后仅在交付字段或关闭状态改变时维护索引；后台从到期索引选取一份共享读取，进入 archive 准备或 server 升级阶段后停止新领取，已有读取仍可保存原回执。空队列不进入写事务，不按周期重新读取所有 Issue。
