# P2b：本机不可变配置快照与请求 CAS 恢复

状态：正式实施中；两个候选已分别完成独立静态评审，本片定向 race/vet 与 r1 完整检查通过；r1 规定 CLI 独立 review 的一项正常配置缺陷已采纳，已最小修复并通过定向 race/vet，r2 独立复核与完整检查均通过，CI/合并未完成。无生产配置调用、发布或部署。Refs: D123；依赖 [P2a 配置基础](agent-definition-config.md)。固定 base：`fdd81c682875bfeeefceb2dfc1a9405c6acdae99`。

## 范围与边界

新增 `internal/runner/agentregistry`，只依赖标准库、已有 `internal/agentconfig` 和 protocol 的 request ID 规则。接收 opaque validated Candidate，保存完整私有配置 snapshot、公共 hash 与本机来源/安装引用。Agent 稳定身份沿用执行环境 identity 加 local agent ID；runtime binding 可变，历史 snapshot 不变。安装引用是声明，不是实际可用证明。没有 factory 加载、runner 自动上传、server 库存、CLI、调度或 UI 接点，不改变既有 worker 默认行为。

唯一 owner、主机及配置可信，不防恶意管理员或多租户。本机 runner 生命周期拥有一个 writer，不新增锁服务。默认目录 0700、文件 0600；不做全树权限/symlink 反篡改包装，不要求存储 JSON 逐字节等于重新 Marshal。

同时修复通用 runner journal 的请求恢复：现有 requests 表漏存 Envelope.ExpectedControlRevision，重启后显式 CAS 变成 nil、PayloadHash 改变。该修复是未来固定配置控制请求可靠重放的前置条件；本片不新增此类生产请求或完整 run 配置快照协议。

## 本地快照提交与恢复

Agent 身份及配置校验沿用 P2a。Snapshot 以本机 opaque ID 和完整私有内容 hash 固定；公共 hash 不涵盖私有引用，不能用于判断完整配置相同。更新不修改旧 snapshot；本片不另做全历史声明版本制度或 artifact 真实性验证。

Snapshot 先持久化，随后同一次原子文件提交更新 current、revision、snapshot 索引与成功 request receipt。原请求重放先于 CAS 及其他 pending，同 ID 异内容冲突；并发 expected revision 只允许一个胜出。写入/同步/发布失败不伪造成功；rename 或目录确认结果不明时保留旧 LastConfirmed 并对账原请求，不换 ID 逃避 unknown。

Open 读取权威 metadata 并验证 current payload；历史 snapshot 仅在 Resolve 或实际复用时读取/hash 核对，无关旧文件缺失不阻塞健康 current。当前必要 state 损坏/丢失明确报错，不默认重建空库；orphan 不晋升 accepted。重启没有原 receipt 返回 not_recorded，不伪造失败/成功或外部效果。旧 snapshot 保留，无 GC 和第二份 run 引用账本。

## Journal 11 与兼容

追加 `0011_request_control_revision.sql`，requests 增加可空 INTEGER expected_control_revision，CurrentVersion 从 10 升至 11。共用 putRequest 与 Requests 精确保留 nil、显式 0 和非零 int64；PutRequest、Finish、Replace 共用写入路径，RequestForRun 沿用读取路径。请求 ID、target、payload、run 关联和 PayloadHash 在重启后保持。

0001–0010 不改；旧行新增列为 NULL，保持原有“无前置 revision”恢复语义，不填当前版本或 0。旧格式若已丢失外部调用者的 CAS 值，本片无法恢复历史意图。现有生产 updater authorization 的独立 envelope_json 保存路径不变。

这是本机存储迁移，不改变 server/runner wire 字段、协议默认语义或版本。11 代 journal 会被旧 10 代二进制按既有检查拒绝；正式发布时 manifest 必须声明实际 runner journal 兼容范围，部署/回退需遵守恢复准入，不能声明旧二进制可以读取 11 代。本片不生成发布产物或 manifest，不解除 P9 的发布核验门槛。

## 文件和验证

先写本计划，再纳入四个 registry Go 文件与 journal 的实现、0011 迁移、恢复回归，并窄增量更新 D123/总台账。`model.go` 定义私有候选/快照及 P2a 投影；`disk.go` 负责普通文件原子提交；`registry.go` 负责去重、CAS、当前观察、对账及按需恢复。

定向运行 registry/journal 两个小包的 race 与 vet；仅临时目录/数据库。registry 覆盖格式化重启、公私 identity、旧 ref、receipt/pending、并发 CAS、代表性提交失败及 old/next 恢复、orphan 不晋升、按需历史读取和当前损坏不重建；不做故障窗口全乘积。journal 覆盖 nil、显式 0、超过浮点精确范围的 int64、PutRequest/Finish 两种写入、同请求 hash、真实 10 代库迁移及重复打开。随后冻结实际 patch，执行规定独立 review 和唯一完整 make check；未发生结果不写成完成。

生产 loader/server 库存、冻结 run 的 model/effort/runtime 配置、原 runtime/artifact 可验证恢复、角色指令真实注入、同 host workspace/backend 协调和 hosted 禁执行仍由后续 P2/P3/P4 实现。公共定义仍 staged/unverified；文件存储与 CAS 恢复不能被称为 runtime 已启用。

## 实施证据

- 固定 base 如上；r2 实施 patch SHA-256 `3e750483f5b06ad78a668c2ad92019e58c3577383273ff1aa488c301babc0fd4`，文件 manifest SHA-256 `8dfde65df366fa0be7803b678f2b7fd5f5334d8bd29cd432694e49087d6b505e`；随后仅更新验证状态。
- 已评审候选：本地存储引入 manifest SHA-256 `869724f64787f46512e6183f1e4ba8b13430440430d6b66ab793152daf20053b`；journal patch SHA-256 `ef73d0ec2c6bf22c83532059c07d9e5a3b555010481150d3c45c61a94d09b299`。候选评审不代替正式组合补丁验收。
- 正式定向 race：`go test -race ./internal/runner/agentregistry ./internal/runner/journal -count=1` 退出 0；registry 1.291 秒、journal 2.453 秒。两个包的 `go vet` 退出 0。
- 现有 runner 发布兼容回归定向 race 退出 0，1.256 秒：`TestReleaseDownloadRequiresSameOriginExactBytesAndDigest`、`TestReleaseBundleRejectsPreviousJournalOnlyBeforeArtifacts`，验证当前 journal 支持及仅支持前一代的发布被拒绝。Go 格式与 diff 空白检查通过。
- r1 正式独立 review 一项缺陷已修正，r1 完整 make check 通过；r2 独立复核和完整 make check 通过，CI/合并未完成。部署及 live 验收未执行。

### r1 review 与 r2 修正

r1 完整 make check 退出 0，UTC 19:25:05 至 19:32:18。规定 CLI 独立 review 指出：typed 空配置和 runtime-only 配置的合法 nil 顶层列表，在 detach 的 JSON roundtrip 中先写为 null、被 P2a 严格 Decode 拒绝，现有 nil→[] 归一执行得太晚。采纳该项；只把已有顶层归一移至 Marshal 前，不改变 P2a wire decoder。新增 typed 空/runtime-only 与显式 [] 等价的私有/公共 hash、持久提交及重启回归，先记录旧实现失败再运行修复后小包检查。r1 检查通过不代表已修复候选验收完成。

r2 回归在旧实现实际失败：typed 空配置在 $.runtimes、runtime-only 在 $.agents 报 invalid_shape；前移归一后 registry 全小包 race 退出 0，1.293 秒，vet 退出 0。新测试同时验证调用者 nil 列表不变、与显式 [] 的公共/私有 hash 和内容相同、同 snapshot 复用及持久提交后重启读取。P2a decoder、journal 和其他生产行为均未改。r2 独立增量复核通过，无新增 findings；原规定 CLI 已正常退出 0，其 finding 和修复记录保留。本次小修未重复运行整次 CLI。最终完整 make check 退出 0（UTC 19:34:28 至 19:35:54），全部 Go/Web 检查通过；日志 SHA-256 为 `8e4ad3415ec2d419160f3e6be764c9ed4fb73a2c2199c17035ae35a4e50c6178`。实现文件与已复核快照一致，之后仅更新进度记录。
