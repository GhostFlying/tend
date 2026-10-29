# 当前 Issue 公共入口退役

以前件完整依赖组合与最小 canonical context/CLI 切片为基础，当前源码独立于 root 检查、旧原件和真实运行账本。UI 对应固定后片移除旧 work_item query、旧 Details 和旧业务 reader；TaskInputs、共用读取生命周期和原 Choice 回答 Map 保留。

## 当前执行范围

- 移除 owner `/work-items`、detail/history/context/followup 旧资源与 generic 旧 followup history 路由，移除旧定义/生命周期/followup/refresh/association/open_background owner 写注册。当前 `/issues` 及其真实用例仍为唯一新责任入口，不把旧 ID 当新 Issue。
- 默认工具的 work_items 创建/编辑/关闭/重开/link、旧 context 与 followup/choice 创建安排面、tasks_create/tasks_update_intent 退出实际注册/dispatch；新调查/执行必须通过 canonical Issue 的 scoped frozen task 路径。已经具名的 main/manager 工具继续原 execution/effect/epoch/CAS 与独立验收规则。
- 保留 `/work-item-choices/{id}`、`work_item_choices.respond` 及其原 ID/version/retry/effect；现存问题不复制成 Attention。旧问题的创建/替换安排不成为新的 Issue 活动规划入口。owner 的真实 task 控制和原 Decision 答复不删。
- owner 当前对话/关联默认投影统一真实 Issue DTO，保留正文、媒体、作者、时间及 CAS。Raw LedgerEvent、独立只读档案、原 Task/Run/Spec、runner JSON 不改；当前控件不回退旧 WorkItem facade。
- server 启用 `main_agent` 时必须提供已有 `named` 配置，缺失在组装/打开业务库前明确拒绝；不以旧 native Open 承载新管理角色。省略 main_agent 的 owner-only 服务继续合法，worker 不变。示例明确为 owner-only，不生成被拒的旧默认配置。

## 测试与历史边界

真实 HTTP/Specs/Handle 调用验证退出，不把入口仅从目录隐藏。原 API 专属可写语义测试改为退役拒绝与 canonical 用例；其它功能所需的历史数据明确由测试专用 helper 直 App/原内部 handler 构造，不给通用 `e.call` 加旧名称 fallback、不回注册生产入口，也不将历史单测称为当前公共 ABI。正常 named/canonical 回归继续真实入口。

此变更不修改 runner N/N−1 通信。旧格式和已提交迁移/不可变历史继续读取，不能以空新账本跳过兼容；若真实 peer 字段变动必须完整升代，本片不做该变动。生成 owner schema 按实际服务面收口，不能误删仍由原 Choice/任务文件读取使用的定义。

先定向 caller 红→绿与必要构建/类型检查，固定后独立 review。最终固定 UI 合并与完整检查由协调方执行；当前不操作 root、Git、真实服务、P9 或部署。

## 候选验证与共同发布门槛

旧 owner 资源在非空历史 fixture 上真实返回 not_found，旧 mutation 不在 ops 注册表；默认 Specs 与实际 Handle 均拒绝旧规划名称。当前 owner 对话、消息关联、队列及 transcript 默认返回同一 canonical 投影；`projection=issues` 不再代表第二种活动业务模式。原 Choice read/respond 和实际答案效果保持。

旧工具 schema 的广告断言改为实际 named `issues_*` schema；历史 codec 由显式历史类型表验证。原 commit_seq 旧工具测试改为当前 named execution/effect_seq 跨补充消息、新 call ID、恢复实例与跨操作去重。原生回执、源版本与 request 记录仍从实际调用产生。

原“main 直接建 task”端到端替身迁为 named main 委派、具名负责人创建冻结 task、runner HTTP 实现、独立验收先要求修改再通过及完成核对回到原负责人。历史完成通知/原 Decision 回答的账本 fixture 单列：旧 baseline 用具名 test-only App 准备，背景评估读取当轮实际注入的冻结 context revision，不能拿后来当前版本冒充已交付版本；不恢复已退工具或通过通用 callback 假成功。历史跟进 fixture 必须仍用其真实 owner 命令身份，系统 fixture 不能省略旧来源必需条件。

全包仅编译、integration tag 仅编译、相关 caller race、vet、静态检查及 schema 再生成分别留证。定向首轮暴露的历史 fixture 身份与已退 owner 关联调用已经按实际来源/当前入口修正，失败原记录保留。真实模型集成入口改为实际 named main/manager 与现 CLI 角色限制，但本轮没有发起真实模型或运行服务。后继独立源码全包 race 预检与独立 review 仍待执行，不等于含可见 UI 的 make check。

最终 roots/旧 facade 退役必须与固定 UI 消费者及其真实 CI 浏览器脚本迁移同批提交；当前后端片不单独发布破坏原 UI。共享 owner 生成类型不新增别名，ExecutionCounts 保留原通用含义。原迁移、原生历史、runner 协议与任务/验收隔离不随 owner 展示接口退役删除或改写。

## 固定Go预检后的已知差异收口

入口退役原54路径候选已独立复核。其固定source的Go-only race预检自然退出1、无超时，六项失败原记录保留；不把它记为完整make check或通过。两API用例仍依赖旧规划入口/旧鉴权顺序，改用真实canonical owner任务入口和原owner/退休agent身份的401/404预期，不恢复工具或放宽鉴权。P6注入来源fixture先结算assignment通知，再真实投递该原消息后审核，仍保外部作者非owner与无task副作用。两个unknown维护摘要丢历史事实的生产问题沿已审r19修正，仍不改变普通source准入。

Orca未投递launch的恢复fixture原把HTTP全集长度等于唯一release；新增native_identity在首次Start的OnStartupEvent已经持久并flush/ack。现按真实时序保存重启前观察请求，恢复不得改写/重传其前缀，仅追加唯一原release，并核原request ID/payload hash；launch1/send0/close2、停止证明和原失败记录不变。生产runner与恢复规则未修改。已知六项在组合copy定向验证（API/App/mainthread通过，runner fixture后继修正再单独通过），首轮新增断言误把已ack观察当pending的失败亦保留。当前只待该实际组合窄审与后续唯一正式检查，不另重复全仓预检。
