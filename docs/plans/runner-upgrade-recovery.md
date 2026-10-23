# Runner 升级评审修复

依据 D82–D85，补充受控升级计划的恢复约束。

- 新维护尝试清理旧下载状态，异步旧尝试的进度及失败不能污染新控制 revision。
- 授权 journal 持久区分明确拒绝与未确定回执：只有明确拒绝且控制 revision 增大才能替换 request ID；晚到旧结果不能覆盖新请求。
- 固定本机 updater socket 的 status 提供终态回退证明，经 helper 和当前进程实例/哈希验证后由 runtime 心跳发送；读取失败不伪造回退。
- 首次确认维护授权时按完整请求 RTT 计算保守 server 时钟截止下界。同 ID 首次确认后的本地截止及 server 下界均保持固定，新进程必须重新 hello。
- 覆盖旧下载失败重试、异步晚到结果、明确拒绝和未知投递跨重启、回执身份、时钟偏移及延迟重复授权，运行 runner/journal race 和 vet。

## 第二轮评审：明确取消的本机 staged 操作

- 只依据持久 `CancelledUpgradeID` 同步取消；控制临时清空或 attention 不代表取消。
- 通过固定 updater socket 检查逻辑实例、批次与 staged phase，取消请求在 supervisor 锁内再次确认 phase，避免读取后的阶段竞争。
- 异步短超时并与下载串行；确认旧操作已终态或不存在后缓存完成标记，避免阻塞下一批次。重启后重新从 journal 的持久标记核对。
- 使用真实 Unix stub 验证投递失败跨重启、下一批次可 stage，以及非 staged/不同实例/无明确标记不发送取消。

取消确认还必须包含按批次 ID 持久保存的 terminal receipt。操作暂时不存在不构成证明：显式取消写 tombstone，阻止此前超时的旧 `/upgrade` 在后续抵达时重新 stage。历史 committed/rolled_back/cancelled 凭据均可证明旧 ID 已不可重做；后续批次不能清除这些身份。

## 第三轮评审：恢复 normal 指令的确认窗口

- 编排区分可用主机与仅等待本批次 normal 控制确认的在线主机。后者必须有正槽位、fresh 心跳、normal 控制及严格较旧 applied revision；不把它当作可用主机授权或开始维护。
- 等待不推进阶段、不重置截止时间；离线、别批次维护、控制仍维护或当前 revision 已确认但仍维护继续进入 attention。
- 独立测试覆盖 server 恢复 normal 后的心跳窗口、单主机 restored 清 ID 后的元信息确认窗口及负例。

## 两阶段恢复接单

健康观察与恢复接单分离：协调器累计两次有效心跳后，仅向 updater 确认
候选健康；在收到匹配原候选 binding、当前进程及目标 hash 的持久 committed
receipt 前保持维护。持久保存提交证明，候选同版本重启时保留原 binding，
重新累计新实例健康。取消必须遵守同一提交门禁；尚未提交的候选即便已健康
仍允许凭完整回滚证明恢复。健康接口同时报告精确控制版本及未接单维护 fence。
回归覆盖无证明、错误证明、重启、延迟证明、取消与回滚边界，不续期既有授权。
