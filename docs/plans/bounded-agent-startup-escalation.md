# 负责人启动故障的有界上报

依据：D150 的逐级责任模型、D169 的原回执安全边界。作者仅负责尚未 ready 的会话启动路径；manager 失租处理另行提交。

## 范围

1. 先补实际失败测试：重复的已知 pre-ready 启动故障不能永久只退避；重启仍保留失败预算；父 Issue、根主协调及主协调自己的 owner fallback 均可达。
2. 复用现有 AgentRecoveryPolicy 的 MaxAttempts/MaxElapsed 作为启动技术恢复预算，保留现有30秒暂态退避。使用独立持久启动失败事件记录预算，不把未提交的输入伪装成 outbox 投递次数或 native turn；不新增 schema 或 runner wire。
3. 达到预算后，同事务保存去重 action Attention、原输入引用与上级 background source。Issue manager 自身故障交给父 Issue，根交 main；main 自身故障直接生成 owner 可处理的 action，不依赖再次打开坏 main。
4. 原 pending 输入、原 request ID、执行 pin 和未知 native 回执保持事实。上级处理技术问题前暂停该输入的自动启动；技术问题明确结清后允许原输入继续，并开启新的有界恢复窗口。启动成功清理本窗口的失败状态，不修改历史。
5. 只跑相关 app/mainthread 目标测试，使用独立临时 cache、GOMAXPROCS=2、包并行1。禁止 full/CI/model/生产操作；保留 RED/GREEN 原始日志。

## 验证

- 一次暂态启动失败仅退避，不产生上级问题，也不产生 native turn。
- 相同原输入的重复启动故障达到预算后只产生一个问题与持久通知；重启、重复扫描不会重复升级。
- manager 故障交父级、无父交 main；main 启动故障交 owner，owner 能看见原输入引用。
- 原输入在问题待处理时仍 pending且身份不变，明确处理后可继续；不能靠新请求替换旧问题或重放未知业务。

## 不在范围

- manager lease expiry、原生 reader 协议、强制释放或恢复 unknown。
- 新 timer、运行时探活、泛工作流、模型切换、生产状态修改。

