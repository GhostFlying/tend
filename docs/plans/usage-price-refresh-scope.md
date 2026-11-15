# 用量报价刷新只重估受影响模型

原目标是刷新最新公共报价后，现有用量页面立即得到正确估价，测量原件、覆盖、归属和报价抓取时间保持真实。已有一次慢写4.903秒仅是观察，精确历史成本未知，不据此扩大SQLite改造。

当前实际调用为serverkit定期抓取→App.RecordUsagePriceCatalog→latest prices UPSERT→价格触发器标记全部current请求→App事务末refreshUsageProjection。正常目录顺序变化、无历史用量模型新增或费率变化，也会重建所有贡献和摘要。这是可控fixture能复现的重复工作；先计数实际重估／写入，不以设备wall-clock阈值判定。

计划：增加语义等价的既有模型选择比较，保留exact/casefold/唯一slug/歧义和override顺序语义。追加迁移移除无差别全量报价trigger，增加共享model/context索引。原App事务读取上次目录，更新单例当前目录和抓取时刻，仅标记选中模型/费率发生变化的已存测量；事务末沿现有投影流程结算，不增持久报价历史、缓存或后台对账账本。

原故障起点到目标终点测试：真实记录不同模型用量、安装初始报价，重复抓取/调整未使用目录项应保留全部测量与估价且不重建贡献；更新已使用费率、模型移除和唯一slug转歧义应正确重估相关请求；未关联模型不重写，缺失计数保持未定价，取消失败不替换有效目录。检查索引查询计划，不把本地成本倍率当生产收益。所有变化仍单事务，失败可保留旧有效目录并在原定期入口下次正常抓取；无新增审批、deadline或生产探测。

Refs: D245。

复用现有slow-write observer补固定usage_prices.refresh operation和catalog/projection阶段；这些诊断不记录原始报价、token或凭据，失败不改变业务控制。

审阅修正：固定阶段必须进入既有 Tx.ObservePhase allowlist。沿真实 store 写观察生命周期分别覆盖旧请求阶段与报价/投影阶段，在成功、callback失败、commit失败时保留实际阶段及inclusive时长、来源和连接释放，不让诊断静默丢弃。
