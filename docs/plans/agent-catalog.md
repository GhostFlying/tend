# P2e：只读 Agent Catalog

目标：owner HTTP/CLI、实际 named main 与 Issue manager 读取同一全局分类列表和指定 host 安全库存/实际 executor 观测。以已冻结 P6-only-on-final-P8 为固定前件，在隔离副本实施；无新表、配置源、协议握手、调度、provider 探测或 UI。

## 合同和调用点

- owner `GET /api/v1/categories?state=active|retired|all&after=&limit=` 默认 active/50、最多100；按 category ID 稳定升序，先取每分类最新 revision 再过滤状态；items/more/next，不改变历史。
- owner `GET /api/v1/hosts/{id}/agents` 返回薄 `RunnerAgentCatalog`：host_id、现有登记 receipt/public projection、最后已接受的 executor 观测、online 和 last heartbeat。没有 runner request/reconciliation 状态。未知 host not_found；已知无库存为 null，退役/过期/offline 保留历史而 online=false。runtime Available 是原观测，不代表现场模型永远可用；catalog 不改变该值。
- app 共享只读 helper 读取现有 host、host_runtime 和库存；注入时钟、复用现有 RunnerOfflineAfter/runtimeSupportsAgentExecution。原 runner inventory_status 保留鉴权/原 request 去重读取及旧字段/默认，只复用公共段，不收紧旧请求。
- named `agent_categories_list` 与 `agents_runner_read` 复用 app 视图；沿真实 hosted execution/thread/角色检查，仅 main/Issue manager。本机分类不授业务角色，咨询不新增目录能力。named main 只读分派保留 r6 先接纳 steered source 的顺序，不落 legacy owner-source 写封装。
- owner CLI `category list` 与 `agents show --host`，纯 HTTP 读取，仅 stdlib+protocol；原 category put/show 原样。真实 roots 生成 schema/TS，无手写 web。

## 验证和交付

分类最新版本/分页边界、登记/未登记/未知 host、在线/过期/退役对照经真实 app 注册/hello 构造；实际 owner HTTP/CLI、实际 pooled main/manager 工具正向调用；必要非 owner/咨询负控，读前后请求/event/outbox/heartbeat不变。定向 race/vet/staticcheck、schema/TS生成与工具 schema 检查；不重复全检/恶意 owner 矩阵。独立冻结 patch/manifest 交协调者，不提交/部署。

## 候选结果与前件

owner HTTP/CLI、实际 named main 与 pooled Issue manager 均接通同一只读目录；薄 DTO 不含 runner 请求对账字段。定向 race 通过分类分页/最新版本、登记与 executor/presence 分离、无库存/未知/离线/退役、owner 权限、真实 CLI 读取和咨询不新增目录能力。旧 runner inventory status、条件 capability ack 与原 named pin 恢复回归通过；原 request receipt 和 wire1/0 默认未改。schema/TS正常生成、typecheck、相关 vet、非 app 包 staticcheck 与兼容检查通过。

app staticcheck 在本片和固定前件均只报 P5 两条原告警（S1016/SA4006），不伪称整组通过；等价修正由独立 P5 两文件补丁提供，不混入本片。P4 r7 的原普通重试/父 manager 子项只读修复仍为后续冻结依赖，本片不实现或扩大该范围。没有完整 make check、规定 CLI review、CI、合并、部署或真实 provider 验证。
