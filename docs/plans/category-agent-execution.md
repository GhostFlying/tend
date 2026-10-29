# P3a：category 选择与实际 run 配置

状态：本片代码已正式纳入，Refs: D123。固定审查前件 `964167d50923fd5ce5053e1187c9624f5540856f` 与正式检查前件 `4776f2a3a0fcebe62fe75adaee64efa1d6ccf7a9` 的 tree 相同。正式 r2 规定 CLI review 与 r3 schema 增量复审通过，r3 完整 make check 和 Windows/macOS 交叉编译通过；CI/合并、部署和真实模型 E2E 未完成。下文保留候选和失败检查当时的历史记录；当前状态以本节及最后补记为准。hosted main/issue/consultation 不在本片启用。

## 完整调用范围

owner 在 `OwnerTaskSpec.agent_selection` 为 implementation 与 acceptance 分别指定 category，可选硬 runtime 引用；既有 `requirements.host_id` 仍是 HARD runner。真实 Issue create-task/update-task 输入经唯一映射进入领域 Spec，服务端不接收私有配置、instructions、安装路径或原始配置。

scheduler 使用当前认证 runner 的已协商执行观察、当前注册库存及 active category。当前 task/spec 首次 named 分配必须在同一 host、同一实际 workspace backend 同时存在实施和正式验收候选，但只冻结当前 run。验收真正分配时在原 host/backend 选择自己的候选。相同 run 的领取回执、重连和重启永远使用原 pin；新的 run 才能重新选择并记录。已有 workspace 上的首次资格由当前 task/spec 已有的 named run pin 证明；owner start-over 放弃 binding 后，即使 spec 不变也必须在新 host/backend 重新核对双角色资格。复用 workspace、legacy run 或旧 spec 分配均不替代该检查；依据已有 run execution 与 run 冻结 spec，不增加持久标志。能力后来消失即等待，不迁移 workspace、不用另一 runtime 顶替原 run。

每个 role requirement 可选 model/effort，沿 OwnerTaskSpec 冻结并过滤本机 definition 的实际 profile，绝不覆盖配置；省略的字段不增加筛选，尤其空 effort 不要求本机 effort 为空。不匹配明确排队。旧 AcceptanceExecution 仍按完整 profile 精确匹配，与新 acceptance requirement 矛盾的输入直接 invalid_request。

run pin 与派发在同一事务中保存，包含执行环境/local agent 身份、Agent/runtime revision、本机 snapshot、公共投影 hash、完整 model/effort、实际 worker kind 及 backend。私有配置只由 runner 从 accepted 索引解析原 snapshot。新库存/reload 只改变未来选择。

保留既有 host 全局 Slots/Busy 和跨角色 workspace 独占；unknown 仍占资源。category 是筛选元数据，不授正式角色权限。旧 `host.Worker` 继续用于无选择 legacy 路径，不能冒充全部 named adapter 能力。显式 named 的 multi-mount 检查使用实际 backend，不被无关 legacy worker kind 预检错误拒绝。

## 本机 factory 与共享 workspace

`agents.execution` 显式启用；`workspace_backend` 可选 git/orca，缺省按既有 legacy worker 配置推导。`orca_bin` 可独立配置共同 Orca workspace backend，不要求为启用 named Orca Agent 更改无关 legacy worker kind。主机只有一个 workspace 协调器，有限 layout 视图共享 Home、锁、repository binding 与回执；不创建多个独立实例争用同一 Home。

本片每主机配置一个 named backend。app-server 可使用 git 或 Orca 管理的实际目录，Orca TUI 使用 Orca backend；多 mount 只允许 git backend。provider ID/instance、路径及 backend 首次固定，恢复和清理核对原资源；缺原 backend/配置明确受阻。

新 named backend 资源的后续 spec 编辑或新 task 复用必须保留/提供双方 `agent_selection`，缺失返回可操作原因，不建设 legacy factory fallback。旧 backend 为空的资源与无选择旧任务保持原行为。拒绝新输入不修改已开始 run 的合同。

accepted snapshot 中 RuntimeBinding.ExecutableRef 是实际启动命令/路径，LookPath、原生 probe 和 Traex/Orca factory 唯一使用它。Installation.LocalReference 只作为冻结的本机来源/安装 metadata locator，不覆盖启动命令、不要求两字段相等，也不承诺已验证二进制内容。readiness 表示本机适配器可接纳该配置；它不保证模型服务、认证或配额持续可用。真实 launch 拒绝沿 run 的实际失败路径保留原因，不换默认配置。新选择被拒绝后，已有会话停止/恢复仍能解析原 factory。后台 setup 复用现有 reconciler，heartbeat 不等待原生 setup。

## 私有角色指令与正式验收

AgentDefinition 的可选 `instructions` 仅保存在私有不可变 snapshot，与完整私有 identity 关联；不进入公共投影/hash、服务端、CLI 状态或日志原文。旧缺字段配置/快照保持空值，字节和 hash 不重写。

两个正式角色均从原 snapshot 取得 model/effort/instructions。Traex 使用真实 thread start/resume 的 developer instructions 与 turn model/effort；Orca 使用已有 CLI 的 agent kind/model/effort/prompt，显式 terminal surface，不新增 provider、不修改 Orca。Tend 的正式 role/task 合同保持；验收使用自己的 run、凭据、scratch 与 native 会话，不继承实施会话、implementation-only context 或自评。

本机 owner、配置和安装可信，不增加恶意 prompt 扫描、同 UID 防篡改或每层二进制证明。未实际实现的 skills/custom MCP 仍由原严格配置校验拒绝。当前 Orca 的 trae 分支不支持所请求 model preference 时，仅该设置组合明确不可用，不能由此宣告全部 Orca runtime 不可用。

既有 `acceptance_execution` 与 named selection 同时出现时是验收完整 profile 的精确约束；不默默覆盖 definition，也不继承另一模型的 effort。

## Wire 与本机 journal

wire 保持 N=1/N−1=0。新执行由独立 capability 与条件 hello ack 协商；协商前/旧 server 不收到执行观察，旧 runner 的 reply 不增加新字段；新 cleanup 请求仅在条件 ack 后声明 backend 支持，旧请求不会收到 named backend 清理资源。显式新 task 才要求执行 pin。旧 no-selection task、claim/checkWorker、hello 默认和旧恢复不收紧。库存静态投影仍是 staged 描述，当前执行观察是独立字段，不把登记等同运行可用。

本机 journal 追加 12：assignment_json 保留完整原 pin，不增加竞争账本。旧 11 reader 会忽略未知 JSON 字段，故必须在读取前由既有 user_version gate 拒绝 12；不能让旧二进制按默认 worker 恢复 named run。旧 11 数据升级保持原 assignment 与缺省身份，不回填 pin。实际 release manifest 必须声明真实 journal 12 兼容范围，旧二进制回退须遵守该准入；本候选不产生 release manifest 或解除发布核验门槛。server migration 57 仅追加 workspace backend 与 run pin；正式应用时重核迁移编号。

## 定向验证

- 真实 owner CLI/HTTP 创建和更新选择、硬约束排队、拒绝移除新资源的选择；实际 runner 登记、启动、reload、重启和独立验收。
- app-server/app-server 与 app-server/Orca 两种组合；同 run 恢复旧 profile/instructions，后续验收 run 使用新 snapshot，独立会话/上下文。
- 实际 native Traex start/resume/turn 和 Orca CLI argv/prompt/surface 参数；不联网调用模型。
- 共同 backend、全局容量及 unknown workspace gate；共享 coordinator 的锁、bindings、provider metadata 与原目录保持。
- 原 snapshot accepted-only 解析、拒绝后原 factory 可停止/恢复、setup 不阻塞 heartbeat；原配置/原 backend 缺失不得默认替换。
- 当前生成入口 OwnerTaskSpec/schema/TS、条件 ack/旧请求字段缺省、journal 11→12 恢复与真实旧 reader 拒绝，以及旧无 instructions snapshot 的实际读取/hash 对照。

候选已运行上述业务路径及必要邻近回归的定向 race/vet/生成检查，并完成独立 xhigh 复核。正式组合仅补必要组合检查，之后统一执行规定 CLI review、完整 make check 与 PR CI；候选证据不替代这些检查或部署验收。

## 正式 review 修正候选

正式首轮 make check 因 ST1005 错误字符串格式失败，未完成；规定 CLI review 提出的四项实际 P2 均采纳。本次仅在独立候选修正：共享资源从 legacy 提升前，等待仍未结束且未选择 named 的真实 peer binding 合同；不重写旧 task/spec 或为其补 Agent。Probe 暂时失败返回错误给已有 reconciler retry timer，heartbeat 不等待 probe，不新增健康循环。Orca 原生 launch 继续包含本机私有 instructions，但生命周期上传用不含该配置的同一任务 brief，不扫描任意模型文本。Cleanup 在 SQL eligibility 前过滤未协商 named 资源，再 limit，旧客户端不预留这些资源且可继续清理 legacy。并纳入一行错误字符串小写修正。

本轮候选定向检查/独立复审结果另行记录；正式 tracked 仍保持首轮冻结，不把首轮格式失败或 CLI findings 记为全检通过。

## 第二轮完整检查的 tool schema 修正

正式 r2 的独立规定 CLI/manual review 已通过，但完整 make check 未通过：现有 tools TestSchemasMatchArgs 证实 handwritten specSchema 漏掉 domain.Spec.agent_selection；app 包另达到本机40分钟测试预算，未据此确认新业务缺陷。本次只补既有 legacy tasks_create/tasks_update_intent 的可选 schema 字段，保留 category/runtime/model/effort 的实际筛选约束、内部双角色选择与现有 domain/app 校验；不新增顶层 required、不改变无选择默认或授权/canonical责任边界。create/update 原已解码并转交 domain.Spec，本次不启用新工具、私有pin或执行入口。

该修正以原 TestSchemasMatchArgs 的 red/green 与必要工具回归核验，不新增镜像实现测试。未改 Makefile/超时预算，未运行 app 全包或完整检查；完整检查、CI/合并与部署状态仍由正式后续核验记录。

## 最终本地检查补记

P3 正式 r2 规定 CLI review 正常退出且无 findings；r3 仅补原 tools schema 的可选 agent_selection，原严格测试不变，独立窄复审通过。r3 完整 `make check TEST_TIMEOUT=60m` 于 UTC 2026-10-28 23:56:24 至 2026-10-29 00:38:14 退出 0（app 2465.547 秒、mainthread 411.145 秒、tools 71.456 秒、Web 全部通过），使用已有本机参数，未修改仓库默认超时；Windows amd64 与 macOS arm64 交叉编译通过。全检日志 SHA-256 `6aa8abb8095508e2fc33d8d52a201cdacfb7d111301c596a26f77f5c0a472613`。首轮格式失败、第二轮 schema 缺项及 40 分钟 package timeout 均保留为失败记录，不记作通过。PR CI/合并、部署和真实模型验收仍待完成。
