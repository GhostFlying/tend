# Compact 恢复的全部原生准入隔离

D150 的原 compact 恢复完成流已经隔离普通 Deliver 和工具服务；89a594e0 的规定审查又确认两处实际入口：idle summary 可在该附件上 fork 新 model turn；非 managed 后台会话的 ResumeThread 可在返回前调用已登记的工具 handler，而隔离标志还未设置。

先写反例：原恢复附件（含已持久结算但尚未正常重启）不能启动 idle summary，当前 live thread 的未知 compact 同样不可与新 summary 交叠；使用 ResumeThread 同步 hook 调用实际工具 handler，确认进入 adapter 之前即已隔离，不触达工具服务。依旧保留正常 idle summary 正例。

最小实现：在 compact 原 thread 的 ResumeThread/回调配置暴露前设置实例隔离标志，保持到该 session teardown；idleSummary 同时核验实例隔离和账本未知 compact 后才作 summary claim/native fork。数据库错误不转成功，完成事件/ack 与正常 toolset 重启规则不变。有限 race GREEN 后重新做完整规定独立审查；CLEAR 后才同精确候选并行最终正常 local full 与 CI。此修正不改变产品超时、runner 协议或目标业务产物。
