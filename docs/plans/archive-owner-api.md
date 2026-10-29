# P8c：owner 历史档案只读 API

状态：实施候选。Refs D126；依赖离线 archive capture/reader，不等于在线维护排空、真实切换或 Web 页面已交付。单 owner 及其本机配置可信，不增加 registry/凭据体系、watcher、磁盘反篡改或每请求证明链。

## 合同与接点

当前实例本机配置 `archives` 将明确 archive ID 映射到封存目录。cmd 启动时复用 cfgfile.Path，逐条验证并打开专用只读 Reader 一次，注入 owner API 并在生命周期结束时关闭。API 请求不重新 Open/Verify/hash 全档案，不调用旧 store.Open、app、agent、outbox 或 worker。缺失/损坏/unsupported 档案安全状态可见，不阻断其他档案和当前业务；源目录和内部错误不进入 HTTP 响应。仅展示 manifest 实际有的 metadata，未知 source_build 等留空。修正材料后显式重启，不新增后台刷新框架。

所有目录配置仅本机存在。HTTP 只接受已配置 archive ID、固定历史 kind、opaque对象ID、资源ID与文件序号，不接受本机路径/SQL。当前 owner 身份及 bearer scope 才能看目录/metadata/历史记录；不赋 agent 读取权限。下载直接复用当前 owner/filePrincipal/signFileGrant 与5分钟TTL，既有grant.Kind含无歧义archive作用域，签名绑定archiveID、原件类型或resource序号及ID/inline，避免跨档案重放；不改变旧文件grant格式/默认语义。

新增 GET/HEAD：`/archives`、`/archives/{archive}`、`/archives/{archive}/records/{kind}`、`/archives/{archive}/records/{kind}/{id}`、`/archives/{archive}/originals/{kind}`、`/archives/{archive}/originals/{kind}/{id}/metadata`、对应原件download-url/下载，以及resources分页、资源files/{index}的download-url/下载。原件kind为attachment/image/deliverable。历史记录按schema/kind/raw data返回，不转换为当前活动Issue。原件/resource投影不含本机目录或manifest payload路径；资源保留实际lineage/scope/coverage/reason，未采集不是空文件或排空证据。

原件沿既有sandbox/no-store/nosniff/no-referrer/Disposition与ServeContent策略；raster可inline，deliverable HTML仍禁止script/connect/form与同源能力，native/journal始终attachment。下载流逐请求关闭；共享Reader不由handler关闭。Close不冒充既有HTTP Shutdown已成功排空，P8b仍负责实际失败观察、准入/排空与停writer。

## 实施边界

- protocol定义公共投影并列入真实APITypes roots；由现命令生成schema/TS，不能手改。归档历史data保留raw语义，protocol不import archive/driver。
- api仅静态只读句柄、routes、投影、错误映射与既有下载鉴权复用。错误使用typed边界，不解析内部错误字符串；400非法参数、404未配置/不存在、503配置档案暂不可读，metadata/错误均先鉴权。
- archive只补必要只读SupportedKinds/typed unsupported边界，复用原objectTables，避免复制schema/kind真相；不改capture/seed合同或冻结依赖原件。
- cmd只做配置解析、逐条只读打开/状态/清理与API组装。P4仍会修改main/config/ownerConfig与roots，最终语义组合；保留P8a离线命令早于正常服务初始化的分派。
- 无存储迁移、runner协议/调度/factory启用、agent工具、HTTP写入或恢复可写入口；server/runner wire默认语义不变。当前历史schema53–57及journal10–12证据不冒充P4的58；实际新schema须正式组合后用真实迁移fixture适配。

## 验证

真实临时capture档案与当前ownerAPI验证目录/metadata/分页/show/原件/resource、原bytes/hash/Range/安全预览、重复请求只初始化一次、缺失/损坏/unsupported状态、跨archive/kind/ID/index/inline/owner/TTL grant负控、旧下载回归及封存目录/bytes不变。确认仅当前owner可读、旧身份/退役agent拒绝，无业务执行或旧服务初始化。逐条初始化失败清理及正常Close责任明确。真实roots/schema/TS生成一致，必要定向race/vet/staticcheck；正式全量检查与规定review由协调者统一安排。不访问真实配置/库/服务或部署，不实现可见UI。

候选已通过 owner API/静态组装及原下载邻近回归的定向 race、真实 APITypes committed schema/TS 生成一致检查、TS typecheck、vet/staticcheck。验证使用临时 schema57 档案；53–57与journal10–12的离线历史证据仍由依赖候选保留，未重复无关矩阵。候选独立 review、正式组合完整检查、schema58适配、UI及真实部署/切换尚未完成；不将只读API当最终P8完成。

## 正式组合边界

与离线归档工具、真实 schema 57 fixture 一并接入当前 P4 业务树；保留 hosted API 路由与协议 roots，通过真实生成器合并 schema 和客户端类型。历史读取仍只使用已验证的只读 handle，不为当前 schema 58 扩 reader 支持。此组合不包含在线 cutover、P5/P6 或可见 UI；定向组合验证后仍须正式独立 review、完整检查与 CI。
