# P2c–P2d：本机 Agent 配置与服务端登记

状态：代码及本地验收完成。Category/server 库存、本机配置控制与实际登记/恢复的 P2c/P2d1/P2d2 候选均已完成独立 review 及定向验证；本片纳入实际组合，正式组合定向 race/vet、真实 roots 生成/类型检查通过，规定 CLI 独立 review 和完整 make check 均已通过，CI/合并/部署未完成。Refs: D123；依赖 [P2a 配置基础](agent-definition-config.md)、[P2b 不可变快照及 journal CAS](agent-registry.md)。正式固定 base：`2ed83de7944773d5a0a416666bcb35884c62fe56`；已包含完成本地检查的 P1b2b 及已合并 P0 基线。

## 本批可交付行为

Owner 能通过 CLI 建立全局 Category；runner 能从固定本机来源严格校验并接受 Agent 配置，发布版本化安全投影，读回登记结果，并在丢失回复和进程重启后按原请求对账。这是本机配置到服务端元数据登记的完整链路，不是 Agent 已经具备运行能力。

命令沿现有入口提供：

- `tend category put <id> --title <text> [--description <text>] [--state active|retired]` 和 `tend category show <id>`。
- `tend-runner agents status|validate|reload|reconcile|register|registration --config <file>`。本机接受重试沿原 `--request-id` 和 `--revision`；reconcile 只需原 ID，不依赖源文件现在仍有效。

`status` / `reload` 表达本机 last accepted；`registration` / `register` 表达服务端登记。新增 bootstrap `agents.source_file` 与本机 installations 引用；未配置时不打开 registry、不增加本机 Agent handler、不广告能力，旧 worker 配置与执行缺省不变。

本批不部署，也不修改 UI。新 Agent 定义不进入 worker factory、run 选择或 P3 调度；所有公共定义仍 staged/unavailable。现有 legacy worker 路径保持原样，不能将旧 worker 正常执行当作新定义生效。

## 身份、分类与单一配置来源

稳定 Agent 身份是现有执行环境身份加本机 local ID；本片 runner 使用现有 host ID，instance/generation 只围栏进程，不改 Agent 身份。一个环境可有多个 Agent，一个 Agent 可有多个 categories。RuntimeBinding 是定义引用；改绑定不换 Agent 的稳定身份。Category 是全局分类元数据，不授予 task/run 的实施或验收角色。

私有 Config、来源路径、可执行/安装引用与凭据只在本机。公共 DTO 唯一定义在 protocol；agentconfig.Public 显式构造该类型，规范化/hash 共用 protocol 的纯值函数。初始公共格式仍为 1，私有 identity 与公共 hash 保持区分；不增加迁移别名或第二套公共结构。

Server 只收安全投影、来源类型、local revision、opaque snapshot ID、公共 hash、Category 当时修订与原请求来源，不接收原配置、路径、指令或凭据。每 host 按最大 server revision 读取 current；请求回执仍由现有 app/request_records 事务持有。Server 不是第二配置源，不回写本机定义。

本片只接 worker runner 的元数据，runner 不得登记 hosted-manager。Hosted main/issue/consultation 的受限运行登记仍待后片，不能用本批 CLI Agent 声称已实现 hosted 禁执行隔离。

## 本机接受与单 writer

`internal/runner/agentcontrol` 负责读取 bootstrap 固定来源，调用 P2a Decode/校验和 P2b Prepare；只有它持有该进程的 registry writer。CLI 只经现有私有 socket 调用，不直接打开 registry。cmd 只做配置解析、路径解析与组装；internal/cli 保持标准库及 protocol 依赖。

首次配置启动仅在目录确实不存在时创建空 registry；不会自动接受源文件。用户可先 validate，再 reload，或直接 reload 内部严格校验。reload 首次自动生成 request ID、读取当前 CAS，并在发出前打印二者；显式重试要求原 ID/revision。同 ID 内容冲突交原 registry 判断，不自动换 ID 越过 unknown。

成功接受不可变 snapshot；无效候选不覆盖 accepted。重启读取 last accepted，源文件后来损坏或编辑不替换它；既有 authority 损坏或丢失不重建为空。旧 snapshot 保留，reconcile 原 receipt 不依赖当前源文件。P2b 的原子 current+receipt、CAS、公私 identity 与 unknown 语义保持不变。

控制入口复用原 repositories socket、长路径 locator 与同一 listener/mux，不增加 daemon、provider、outbox 或独立配置账本。单用户 owner 及主机可信，只保留正常 private 文件权限与真实 crash/retry 边界，不增加同 UID、可信磁盘或恶意管理员防御。

## Category 与版本化库存事务

Owner `categories.put` 经现有鉴权/app 去重事务，创建 expected revision=0，之后按当前 revision CAS 追加不可变修订及事件。Owner GET 经既有只读鉴权与读事务；CLI 首次自动读取 CAS，重试保留原请求身份。分类退役可以保留来源历史，不解锁执行。

当前认证 runner 经现有 RPC 的 inventory register/status 操作，复用 host、incarnation、checkRuntime 和 app/request_records。登记在同一事务校验 CAS、严格安全投影/hash、分类引用、本机单调 revision，写不可变库存版本、事件与回执。原成功请求返回原 receipt；同 ID 异内容冲突。同 local revision 异 snapshot/content 拒绝；更高 local revision 即使公共 hash 相同也保留新的私有 snapshot 引用。

status 允许当前进程按同 host 原 request ID 读取前代回执及 payload hash；不登记新的业务请求，不刷新 heartbeat/presence。登记 accepted、runner 在线、当前可用和 slot 占用是不同事实；服务端状态为 registered_staged，投影 availability=false。没有当前登记或仅有旧版本时必须如实返回，不伪造能力。

候选追加迁移 0055；正式接入先核对分支实际编号，若冲突仅调整未提交候选的新编号。已提交迁移不修改。历史数据不自动创建 Category、Agent 或运行验证结果。

## Hello 协商、发送与重启恢复

配置启用才在已有 capabilities 字符串列表广告 `runner-agent-inventory-v1`，真实 hello 返回条件 ack 后才发送新操作。空 registry 不登记；启动已有 accepted snapshot 时，在首次 hello 后登记。成功 reload/reconcile accepted 仅发送变更通知，现有 reconciler 读取真实 accepted 再 hello/登记；通知不是第二份配置事实。手动 register 用于显式继续登记，例如补齐缺少的 Category 后重试。

发送前将完整 register Envelope 写入现有 journal11，保留原 ID、CAS、runtime 与公共 payload。单次 HTTP 不确定则留原 journal，复用现有 reconciler 继续；不为元数据另启 daemon。登记失败不替换本机 accepted，也不改 legacy task/run 合同。

同 incarnation status 的 not_recorded 仍可能有原请求尚在事务前，必须沿原 envelope 继续，不能改 ID/CAS。已记录成功先核对原请求 hash 及 receipt 的 host/revision/snapshot/public hash，再清 pending。已记录 failed 是确定结果，结束本次调用；以后 reload、手动 register 或既有 reconciliation 才建立新尝试。授权拒绝本身不能清 unknown：前代可能此前已提交后又被退休。

重启由原 journal 分配更高 process generation，以不同 instance 的真实成功 hello 确立当前进程。Server 在 writer 事务内退休旧 instance，登记也在 writer 事务中校验 runtime。此后当前进程先读前代原 ID/hash：原 accepted 沿原 receipt 结清；只有前代明确 not_recorded 且 generation 严格更高、instance 不同，才可明确结束旧尝试并新建当前 incarnation 的请求。不能改写旧 Envelope.Runtime，不能把同 incarnation 重复 hello 回执当新围栏。

本机 accepted、server registered_staged 与 runtime available 分别呈现。Server 仍是旧 snapshot 时，本机登记状态应表达 local_accepted 并保留旧 receipt；unknown 显示原 pending ID，不将断线解释为成功或失败。

## 协议与生成评估

本批不递增 runner wire 协议：使用已允许扩展的 capability 字符串，新增操作只在 mutual ack 后发送；HelloResult 新可选字段只向广告该能力的当前协议客户端返回，无广告者完全省略该键。没有收紧旧有效请求、增加旧路径必填条件或改变旧 worker/claim 缺省。旧 server 接受未知 capability 字符串但不给 ack，新 runner 因而不发送 inventory；新 server 对旧 runner 保持原响应。保留协议 N/N−1 的现有支持，不创造额外适配代次。

该结论由旧 server 实际 probe、新 server 对旧协议严格解码测试，以及新 runner configured/legacy × ack/no-ack 发包测试验证。若正式组合发现需要收紧旧有效请求，必须重新按仓库规则评估 breaking 并完整升级/退役，不能以本计划替代。

P2b journal11 已精确保留 ExpectedControlRevision，历史 NULL 不回填；本批复用而不再加 journal 格式。旧二进制与已升级 journal 的兼容/回退受既有发布准入约束。本批没有发布产物或兼容 manifest。

Category 与 payload 纳入真实 protocol.APITypes 导出根，经既有 schemagen 和 Web gen 命令生成 Schema/TypeScript；生成文件不手改。公共 DTO 移动同时修正 registry 测试的引用，不建立别名或复制第二 DTO。仅生成类型胶水，不涉及可见 UI 行为。

## 验证与正式接入

先写本计划，再在已固定的正式分支接入已审实际组合，更新 D123/总台账的语义与真实状态。三片候选独立预审后，正式组合另行完成了实际差异的人工及规定 CLI review，没有以候选审查替代。

定向证据覆盖：

- 本机首次 empty、validate 无提交、reload/原 ID/CAS、无效候选 last-good、源文件损坏后重启、同进程及重启 unknown、旧 snapshot；现有短/长 home socket 及 disabled 缺省。
- Category owner 鉴权/CAS/历史重放、库存原子回滚、安全字段/hosted 拒绝、同 local ID 跨 host、同公共 hash 私有新版本、分类历史和离线不伪在线、实际 schema 导出与旧存储升级。
- 真实 owner CLI→API→runner hello/RPC→server 登记/状态；accepted reload、缺分类补齐后的手动 register；同 incarnation unknown 保持 ID/hash/CAS。
- 已提交丢回复与未到 server 两种 restart：新进程只读原 accepted receipt，或真实新 hello 围栏后的明确 not_recorded 接续；授权拒绝不抹 unknown。
- 新旧 server/runner capability 四组合、旧 worker 关键回归、原 freshHello stale/retired 分支、CLI 依赖方向、生成物一致性。

候选已有小范围 plain/race/vet；正式组合需固定完整 patch/artifact，完成独立人工及规定 CLI review，再执行一次完整 make check。CI、合并、部署和 live 验收各自记录，不能由本地候选测试代替。全部测试使用临时文件、SQLite、socket 和 httptest，不访问真实凭据或账本。

本批后仍阻塞新定义的实际执行：角色 instructions 受控注入与 Tend 合同/独立验收隔离；原 runtime/artifact 可验证保留和恢复；run 固定 snapshot/model/effort；同 host 全局 workspace/backend 协调及实现/正式验收能力；hosted manager 禁执行；P3 选择/调度。登记完成不解除这些门槛。

## 正式组合证据

- 固定 base 如上，工作树起始 clean；在单份计划落盘后纳入已审组合。39 个实现/生成文件与候选最终字节一致，无 P1b3/b4 或其他后续功能。迁移编号确认 54→55，没有修改历史迁移。
- 正式组合定向 race 通过：公共 DTO/真实 APITypes roots、旧 54 代库升级、库存历史回执与当前进程状态、hello 旧 0/旧 1/新 1 兼容、旧 worker 类型边界、认证 RPC、本机配置及原 socket、Category CLI、真实配置→登记与两种重启对账。agentconfig/agentregistry 在该定向命令中仅编译，未重复此前完整小包矩阵；app 13.290 秒，e2e 9.859 秒。
- 实际运行 schemagen 和 Web gen，Schema/TypeScript 与已审候选字节一致。genapi 测试、Web TypeScript 检查和生成文件 Prettier 检查通过；修改相关 Go 包的 vet 通过。
- 正式组合人工及规定 GPT-6.1-Sol xhigh CLI review 通过，无 findings；CLI 真实退出码为 0，检查前后 43 个文件与冻结补丁保持一致。
- 唯一正式完整 make check 于 UTC 2026-10-28 20:43:14 至 21:19:24 通过，退出码 0；app race 2124.273 秒，Web 45 个文件/281 项测试通过。日志 SHA-256 为 `feb19548786dd002bcfe3dd777156f59146fc6fc6a972e103163d7b608bbaa65`。检查完成后的变动仅为四份设计/计划的事实状态记录，39 个实现/生成文件未变。
- PR/CI、合并、部署及 live 验收尚未完成。上述本地检查不替代发布验收。
