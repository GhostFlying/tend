# Runner 领取 Issue manager（D128）

状态：实现及本地验证完成；待 PR CI、发布及受控切换，未部署。

## 合同

主 Agent 发布指定 category 的真实 manager task，匹配 runner 领取时在同一事务冻结 agent、配置和会话归属并更新 Issue 负责人。发布前不预选主机。默认 category 为 `issue-manager`；category 不授予正式角色权限。

只迁移 Issue manager，main/consultation 保持 host 托管。支持 Codex/Traex app-server，不支持 Orca TUI manager。保留账本、聊天、附件、来源、原请求及历史原生执行引用，不并入空账本切换。

manager task 持续负责该 Issue。每轮有独立 action/run/lease，结束且回执齐全后释放全局执行槽，无输入时 waiting；后续消息/结果沿原持久批次唤醒同一责任。首次领取后保持会话归属，失联不自动重选。交接先围栏并核清旧执行；正常 worker 不停止。Issue 收尾或交接后结束 manager task，不另做独立验收；派发的执行 task 仍独立验收。管理任务不计交付进度，不阻塞自身 Issue 收尾，不唤醒自己。

## 实施批次

1. P1 领域/合同：execution 与 issue_manager task kind、manager run/action、waiting；版本化 issues.request_manager；原子领取及负责人更新、排队原因、持续责任状态和状态机测试。
2. P2 协议/库存：runner 协议 2/1，删除协议 0 通信适配；格式 2 本机 app-server 定义及 issue_manager purpose，保留格式 1 快照/hash；无工作区执行 pin；追加 server/journal 迁移，更新兼容登记、生成类型与发布合同。
3. P3 执行闭环：共用 runner claim/slots/heartbeat/lease/journal/maintenance/fencing，跳过代码工作区；私有原生会话，动态工具、持久事件游标、停止/compact/限流恢复、输入补充与文件下载。runner 不 import server 包。
4. P4 消费者/迁移：main 工具、owner API/CLI、既有 UI 展示 category/具体负责人/运行与等待原因；版本化、可恢复的逐 Issue 迁移及交接；移除迁移后的 host manager 启动路径，main/consultation 保持原行为。
5. P5 验证/发布：定向测试、make check、规定独立 review、CI 和交叉编译；隔离 CLI 验证；合并、程序发布、配置应用、逐 Issue 迁移及真实验收分别留证。

P5 的本地完整 race 复验可用 `APP_TEST_SHARDS` 并行执行 app 包：先通过 `go test -race -list` 列出全部根测试/示例/fuzz 种子目标，再用标准 `go test -race` 做不重不漏的分片，保留标准测试工作目录及缓存语义；其余包和格式、vet、staticcheck、Web 检查不变。默认 `make check` 仍保留单进程测试入口。分片划分及完整性有独立检查，不能用分片遗漏失败场景。

## 执行与正确性

### 发布与领取

`issues.request_manager` 仅接受主 Agent 或 owner 的正式身份，冻结 Issue、category、发布时 assignment epoch、授权来源及责任要求；不冻结实现工作区、产物合同或验收配置。来源内容及关联版本在合同中留存，撤回或移出该 Issue 会围栏后续业务。创建 Issue 时携带同一 manager 请求，复用相同用例及事务。

排队期间保留实际负责人。每个 Issue 最多一个当前待启动责任，同类别重复发布返回原任务；替换或改类别要求 revision/epoch 校验和显式交接。首次领取在同一事务完成 runner 资格、维护状态、全局容量检查，创建 run/lease/公开配置 pin，并写入具体负责人、递增 epoch 和 assignment 事件。只有提交成功的身份进入领取结果；同 request ID 返回原结果。后续轮次使用首次冻结的 Agent、runner 和 native thread；无候选时保留排队，不转 host。

manager 与 execution 共用优先级和等待老化，不另建调度器、不默认预留槽。协调任务不计交付完成率、不进入自身关闭 blocker，也不向自身生成完成验收。worker 合同、实现与独立验收保持原机制。暂停责任、停止单条回复和取消 Issue 沿各自控制入口处理；普通 worker 不因 manager 交接被取消。

`cancelling` 是待收尾的取消意图，已有 manager 保留核对、取消下属工作及 `finish_cancel` 的受限职责，可逐轮释放、唤醒并继续领取；禁止新建任务、扩展目标或重新激活工作。owner 取消等待中的 Issue 会持久唤醒已领取的 manager；尚未领取的 manager 发布由 owner 收回。到 `cancelled` 事实成立才取消该 manager 任务，原生及工具回执仍须核清。

### 原生执行与回执

服务端持有账本、来源审核、上下文版本及业务工具；runner 只执行原生生命周期并报告事实。manager run 凭据由服务端解出真实 Issue/conversation/epoch，不接受客户端自报权限。源码调查也走 task；禁止原生命令、写文件及绕过账本的子 agent，保留已验证 CLI 边界及其限制，不宣称 OS 隔离。

服务端复用现有会话用例和工具审核，`BackgroundPool` 对 runner manager 仅维护持久远程代理；原生 start/resume/turn/compact/stop 由 runner 的协议及 harness 执行能力处理。runner 不 import `app`、`store`、`tools` 或 `mainthread`。execution assignment 才有 workspace；manager 使用 runner 私有会话目录，跳过仓库资格、worktree、基线、提交及产物上传。主 Agent 和独立 consultation 仍使用原 host runtime。

副作用与请求/事件/outbox 同事务。原生启动、发送、工具调用先记 journal；未知只对账，旧 epoch 只能补原回执。事件按序号幂等确认，同身份不同内容拒绝，结束不能越过未确认输出。原始附件按作用域流式校验下载，不传服务端本地路径或 owner 凭据。

动态工具仍由服务端执行，调用方保留实际 manager run。作用域从持久 run、pin、正式角色、会话和当前 epoch 推导；category 不是授权。manager 不能提交实现产物或验收结论，普通 worker 也不能因类别匹配而调用协调工具。工具及原生未知结果按原身份核清，不能因超时重发。配置按轮冻结，有限限流续接保留逻辑轮、原生轮和已完成工具结果。

创建子 Issue 的内嵌 manager 授权来源与顶层来源执行同样的准入及继承范围校验，先校验、后建立新的来源关联。后台池启动时也从持久未结束的 manager run 发现远程代理；即使最后一条 delivery 已提交、原生 close 尚未排队，重启后仍会完成清理并释放槽位。

事件上传游标与应用游标分离：上传成功不等于消息、工具和用量已入账。关闭期间继续消费最终事件，覆盖全部已应用事件的原生关闭回执与 run/lease 释放同事务提交。已关闭进程尚有业务回执待核时，将该 run 记录为失败并释放物理槽，使用原身份的只读 run 核对；不把未知业务写成成功。服务端已生成工具结果但原生进程尚未领取时，关闭证明会明确拒绝其原生投递，业务结果及原请求仍留存；不让工具回调等已关闭进程消费自身结果。runner 重启后先重放已持久化关闭回执，不再启动进程或追加会使旧结束游标失效的新事件。重复内联图片仅在传输的 native raw 副本中替换为 URL SHA-256 引用，避免同一原件反复塞入 RPC；文本、item 身份、原始文件和 runner native 历史不改写。

关闭已排队时，重启的服务端代理可读取同 run 已确认的 model catalog 和同 native thread 启动元数据，先重新接上事件消费；不把恢复启动 RPC 排在 close 之后形成循环等待。这里只复用持久化的成功回执，不合成原生执行或权限观测。确认关闭时，未领取的尾部 RPC 明确拒绝，已领取但无结果的 RPC 保留 unknown。runner 在无法确认停止或提交停止回执失败时唤醒既有对账器。

runner 代理的启动历史/用量读取与事件入账同时推进，完成前不开放新业务输入。迁移后的 Issue 取消或继任任务取消，不撤回旧 host 核清原未知回执的只读入口；旧回执核清后才退出该入口。

附件与证据通过 file ID 及 Issue 范围读取，runner 对下载流校验长度、摘要和原字节。动态下载工具需要本地路径时，由 runner 生成其私有下载路径。已冻结但过期的责任只恢复原历史读取及回执，不领取新业务输入；失联不自动换负责人。交接等待旧启动、业务工具、恢复及停止回执核清后才允许继任者领取。

### 通信与持久化兼容

runner 协议升级为 2，同时保留集中登记的协议 1 普通 execution 适配，退役并实际拒绝协议 0。manager 必须协议 2、`runner-issue-manager-v1` 和相应 journal 能力。新 runner 初次使用兼容 hello 协商：旧 server 保留协议 1 普通任务，不登记或领取 manager；新 server 协商成功后才接受格式 2 manager 库存。兼容登记、JSON Schema、生成的 API 类型和 release manifest 同步更新。

本机配置格式 2 显式区分 Codex/Traex app-server 并声明 `issue_manager` purpose；格式 1 的读取和原 hash 保持不变。不可变快照及其安全库存投影复用既有接受机制，私有 instructions、凭据、安装路径保留在 runner。manager 专用只读就绪探测失败会进入既有重试，不使同定义的健康 worker purpose 失效。

server 与 journal 只追加迁移。已有任务默认为 execution，不补造历史 manager pin、不重写旧合同或配置；未来 journal 格式由旧 runner 拒绝读取。通信适配退役不删除历史任务、消息、附件或迁移。

### 保留账本切换

迁移先预检并备份数据库/内容，围栏旧 host manager，核清 native/control/recovery unknown，保留 conversation 和待消费输入，用账本及有版本交接资料建立新原生会话；领取后更新负责人。一个 Issue 只有一条可执行路径。新副作用产生后的回退也必须对账并反向交接，不回滚数据库覆盖事实。生产切换单独执行，不随 PR 自动部署。

## 验收

- 两 runner 竞争只产生一次领取/负责人更新，丢失回执按原 request 返回原 assignment。
- 缺 category/CLI/槽位或维护中明确排队，无 host fallback。
- 单槽位 manager 派发、释放、worker 实现及验收、manager 唤醒与责任收尾全闭环。
- waiting/pause/stop/cancel/close/handoff 各自正确，无自身完成循环。
- 跨 Issue、旧 epoch、worker 冒用 manager、manager 冒用验收及撤回来源均拒绝。
- 在领取/启动/工具/事件/结束/停止窗口重启两端，保留身份，不重放 unknown。
- Codex/Traex 实际 start/resume/工具限制/compact/限流/附件分别验证。
- 旧数据迁移保留原件与请求，unknown 阻止切换，恢复不重复消费。
- 协议 2/1 双向兼容、协议 0 拒绝、旧 journal 向前拒绝；普通任务及 host main/consultation 回归。

最终证明：main 只指定 category；runner 领取后实际 agent 成为负责人，能委派、等待和收尾，server 未启动该 Issue 的 agent 进程。

## Owner 切换操作

先通过既有 runner 本机配置接受命令接受格式 2 快照，再确认目录中目标 Agent 的 `issue_manager` purpose、实际可用观察、协议 2、journal 13 和 `runner-issue-manager-v1`。配置只存 runner 本机，服务端不保存私有 instructions、凭据或安装路径。类别为空闲候选而非负责人；原负责人保持到首次成功领取。

新 Issue 可在创建合同内携带 `manager: {"category_id":"issue-manager","reason":"..."}`。既有 owner Issue 使用 `tend issue request-manager ISSUE --data '{"category_id":"issue-manager","reason":"委派持续协调责任"}'`。CLI 在首次请求读取 revision/epoch；重试必须保留首次输出的原 Envelope。`--dry-run` 可先保存完整 Envelope，后续使用 `tend issue replay --file ENVELOPE_FILE`，不能用新 request ID 重放未知结果。

既有 host manager 通过 `tend issue migrate-manager ISSUE --data '{"phase":"preflight","category_id":"issue-manager","reason":"保留账本迁移"}'` 预检；`phase:"switch"` 在服务器生成数据库与内容一致备份并保存其 receipt 后围栏旧责任。`phase:"status"` 读取任务及旧执行 blocker；未知结果只查原请求或恢复原 Envelope。旧 native/control/delivery unknown 未清时，继任者保持排队；不重发已执行输入或拷贝私有 native 历史。

首次领取前可用 `phase:"cancel"` 取消切换并把责任交回当前认证 owner。旧回执仍未核清时拒绝取消；首次领取后要求正式反向交接。所有活跃的 host manager 及其未知回执清理完毕后，owner 显式提交 `phase:"finalize"` 关闭 host manager 新启动及直接分配路径。主 Agent 和 consultation 不受该开关影响。以上命令均沿原 request/revision/epoch 鉴权，不以查询或备份作为放行证明。

## 本地验证记录

已覆盖真实 HTTP runner 的 category 首次领取、无服务端原生启动、逐轮释放和单槽位实现/独立验收/manager 收尾；并覆盖双 runner 竞争与领取请求重放、应用游标不越过未处理事件、结束回执与资源释放同事务、关闭期间最后用量入账、授权撤回及附件 Issue/角色范围、本机 journal 重开保留未知操作/线程及事件序号。浏览器在桌面与手机尺寸检查原有 Issue/交接流程及 manager 待领取展示。

完整普通 Go 回归已通过。manager 的 HTTP 执行、单槽位交付/验收/收尾及限流原会话续接通过 race 检查；结束回执重启窗口、就绪重试、超过 4 MiB 的原生图片事件及最终用量处理有定向回归。Web 类型、lint、格式、单测和构建通过，桌面/手机浏览器验证包括 manager 待领取与协调任务详情。

Codex 与 Traex 的隔离 home 原生 start/resume、动态工具读取原始附件内容、compact 完成事件、只读/never 及原生执行拒绝已分别运行，测试使用独立目录和确定性本地 provider。Codex 的类型化 429、保留工具结果、进程重开及空输入续接另有实际 CLI 回归。Traex 原生历史能保留已完成工具结果并接受恢复后的空输入续接，但当前 CLI 把持续 429 的终态报告为 `other`、HTTP status 0，未提供类型化限流及 continuation-safety 证明。因此服务端保留原会话及错误事实，不仅凭文案自动安排 Traex 限流续接；不宣称这一自动能力已可用。

协调原生执行沿用已验证的 Linux CLI 边界，并增加 runner 父进程退出后的进程终止保护。其他平台保持就绪拒绝，不把 Windows/macOS 交叉编译通过当作 manager 可运行的证明。

完整 `make check` 已通过，覆盖全部 Go race 测试及 Web 检查；较慢的 app 测试按互斥的完整测试名称分片，不跳过测试。Windows/macOS 交叉编译通过。独立 review 的有效意见已修复，包括原生关闭期间的历史事件消费、跨回合来源身份、取消前后的责任边界；最后的取消收尾复核未发现有效问题。已领取的 manager 在 cancelling 阶段保留受限收尾权限，等待中的责任被持久取消来源唤醒；未领取的任务不会因取消而取得新责任。

上述原生能力和 HTTP 替身闭环不替代真实部署及各崩溃窗口验收。当前文档不表示已合并、发布、配置已应用或生产 Issue 已迁移；生产切换和真实平台验收仍作为独立交付留证，PR CI 结果以对应提交的流水线为准。
