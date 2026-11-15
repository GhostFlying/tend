# 升级替换与恢复正常路径修复

共同起点为本批已固定 main；只修正常 server cutover 和备份路径。

1. 让空闲 rollout-preview GET 通过既有 Stopping 信号退出；选择/交付 work 与停止并发时不吞 work，POST 和真实业务请求保持 drain。
2. 每次备份只读取一次历史 checkpoint 集合并构建 prefix 索引；共享对象仓，区分只读打开与需要创建目录的持久初始化。源/hash、prefix/parts、恢复验证及清单/state 发布顺序不弱化。
3. 对真正 server replacement 后原 fleet 的 normal ACK 恢复增加有界等待；旧 offline/stale 不算可用，实际 fresh normal 当前代 ACK 才能 drain。原 stage/overall deadline 不延长，错批/缺失/其他操作等否定场景保持拒绝。
4. 先保存 focused RED，再以事件、注入时钟和 IO 调用计数验证 GREEN；不设置机器性能 SLA，不启动 full/PR/CI。
5. 形成原子提交及设计片段后交统一集成者；公共 design 与 AGENTS 由其唯一修改。禁止生产部署、历史升级/DB/任务回放；T6不实施。

业务故障起点：旧 server 收到停止信号；目标终点：一致冷备份并启动新 server 后，原 fleet 实际恢复 normal ACK，随后仅一台 runner 开始 drain。调用路径涵盖 runnerapi preview → app preview wait、updater backup → blobstore、app server reconcile → runner availability/normal ACK gate。回归必须走真实转换，不能仅 fixture 预置阶段，并覆盖故障后等待/恢复及不重复副作用。

Refs: D244（由唯一集成者在同批公共设计中写入）。

实际回归：空preview HTTP请求在已发Stopping下退出；真实owner read先排入preview work，停机不取走pending，已选work在stop后仍完成reply；Linux inotify按本次文件打开/目录自身打开事件计数，每operation每历史清单读取一次且只初始化一个共享写仓，下一operation包含新checkpoint；已有parts/hash/prefix/restore/cancel用例保留。Cutover用真实Start、维护ACK与授权后推进注入时钟超过60秒，再通过两次server健康观察恢复normal；App重建、单台ACK、双台ACK、原期限、缺失/错代/错批/历史无cohort与原有普通offline负例均覆盖。

增量核对：真实 cutover 仅 30 秒时，先验证 normal 已发但尚未 ACK 的实际行为，再推进到旧 heartbeat 的 60 秒边界；若观察窗口错误中断，只对首次授权冻结的原 cohort 追加恢复等待。原 stage/overall deadline、错代/错批/缺失/无 cohort 与实际恢复后再次离线的拒绝仍需覆盖；保留此前冻结提交及 RED/GREEN 原件。

审阅修正：维护控制的实际 runner 在资源对账后会报告 ready；原 cohort 与 pending 等待均接受 maintenance/ready 两种合法观察，仍固定原批、实例、代次与控制版本。新增回归使用真实 runner 控制应用、资源维护与 runtimeObservation，再交给 App 的真实 Hello/heartbeat、Start/授权、替换健康与 normal ACK 路径，不只预置维护观察。

组合全量修正：旧普通可用性诊断用例必须明确区分历史无 cohort 行与当前真实 cutover 等待；保留原 Text/JSON 快照、失败谓词和原期限断言。新 cutover 的实际等待、正常 ACK 与拒绝各记有界分级诊断，包含已经读取的主机、运行/控制身份、失败谓词及原预算；不把等待声明为可用，不改变授权、恢复和 drain 条件。回归从真实 Start/维护 ACK/授权/替换健康观察进入，并覆盖原主机首次 ACK 前 offline/stale 仅等待、错误身份/控制和实际恢复后再失联仍拒绝。
