# P8a：离线账本档案与新空库工具

状态：ignored 实施候选，Refs D126。未连接真实账本、配置、runner 或服务，未部署；P8b 在线 prepare/status 和生命周期排空未交付，不能将本片当可切换声明。信任单一 owner 与本机材料，不建设真实性证明链、恶意同 UID 防篡改、第二请求账本或通用恢复框架。

## 实际入口与数据合同

在 tend-server 正常 run/config/secret/store 初始化前分派 archive 子命令。Capture/verify/list/show/original/resources/resource/export-seed/seed 可独立运行，不启动网络、App 循环或模型。CLI 将机器参数留在本地；无需 UI 实现。

internal/archive 只用标准库与既有 SQLite driver，不 import store/app/runner/journal/blobstore 初始化。Capture 要求明确 writers-stopped 前置条件；P8a 不证明进程已停。保留原 DB/WAL/SHM 和原件，从独立 working copy 用 driver backup 形成无 WAL 依赖的自包含 snapshot；源字节不变。只读 reader 用正确 file: URI 与 mode=ro/immutable，仅对已封存自包含 snapshot；不创建文件、不迁移、不改 WAL。

Manifest 初始 format1：源 deployment/schema/迁移、采集时点、每个 payload 的相对路径/大小/SHA256、原件 ID 映射和资源覆盖。Hash 用于完整性，不宣称来源鉴证。目录/文件正常边界检查，原件仅按 kind+ID 读取，不暴露任意路径。Manifest 最后发布；失败保留原材料与未完成目标，不覆盖旧档案。

初始静态 adapter 明确支持 server schema53–57，后续 P4–P6 按正式版本增补，历史迁移不改写。完整 snapshot 保留所有历史 JSON/表，不自动迁成新 Issue；reader 仅提供固定对象查询，不接受任意 SQL。Unsupported 保留原证据并报错，不自动迁移。

附件、交付报告文件、旧内联图片/文件按原 ID/bytes 导出并校验。Native、baseline/report 本机全文、runner journal 通过显式资源清单采集；已用真实 journal 10–12 fixture 验证从副本规范化，不调用 journal.Open。保存 host/run/thread/parent lineage、范围、collected/partial/not_collected/missing 及原因；未提供 native/journal 的主机、run 和 hosted thread 有明确缺口，不调用 agent 补历史。账本已裁剪的日志不冒称完整原生输入输出。

## 显式静态 seed

Export-seed 需要明确选择仓库、项目、模板、类别、主会话 model/effort 与主机描述。无自动全量恢复。旧普通 main settings 不是 named Agent 定义源，不恢复 server accepted inventory 为可运行能力；正式接入 P4 时按其显式 named 优先规则对齐，不提前声称支持 schema58。静态 DTO 不含历史业务、request/outbox、lease/workspace、来源消息、原 server/runner 凭据或 journal。主机 token/node/runtime/last_seen 不从档案恢复；新节点绑定需显式给出。未设置过普通 model/effort 时继续无覆盖值；已设置的 catalog 字符串原样保存，不能以 worker ExecutionProfile 代替 hosted catalog 校验，也不宣称当前可用。

独立 archiveseed 接点只对排他新建目标使用正常 store/app 校验/迁移，不复用正常 server run。时钟、ID、随机凭据来源注入。生成新 deployment ID/server signing secret/runner token；只写新私有目录与静态配置，保持离线未运行，能力须重新 hello/登记。旧部署 header、旧 owner token 与 runner 身份在实际 API fixture 拒绝；新业务表为空，不恢复任何旧工作。

## 验证与未交付边界

真实 SQLite fixture：最后 WAL 提交、自包含快照、源全文件字节不变、只读/缺失路径/unsupported、每版本53–57、原件独立读回、资源缺口与 manifest 错误、精确静态选择、新空库和旧身份拒绝。CLI 在 normal run 前分派，用临时 fixture 验证无需服务配置/secret即可读档案。只跑必要小包/CLI定向 race/vet，不跑完整仓库。

P8b 复用既有 local control socket、host maintenance/fresh Ready/pending/blockers 与 hosted 准入 hook；补 prepare/status、body 前拒新 owner 写、原已接纳 HTTP/upload 排空、Shutdown 错误观察、禁自动重启和实际 stop-writer runbook。本片不伪装 upgrade、安装 release 或自动 resume。Unknown/活动原生操作未核对不能真实切换；首次新外部执行后不能直接回旧库，先停写/对账并保留新数据。

## 最小操作路径（离线候选，非生产切换指令）

正常服务不必启动。停止 writer 的外部证据由最终 P8b/runbook 提供；单独传入 writers-stopped 不是排空证明。下列参数均由 owner 本机给出，不自动发现 home、认证文件或远端资源。

```sh
tend-server archive capture --database "$STOPPED_LEDGER" --blob-root "$ORIGINAL_FILES" --output "$NEW_ARCHIVE" --writers-stopped --resources "$RESOURCE_SELECTION"
tend-server archive verify --archive "$NEW_ARCHIVE"
tend-server archive list --archive "$NEW_ARCHIVE" --kind message --limit 100
tend-server archive show --archive "$NEW_ARCHIVE" --kind message --id "$HISTORICAL_ID"
tend-server archive original --archive "$NEW_ARCHIVE" --kind attachment --id "$FILE_ID" > "$DOWNLOAD"
tend-server archive resources --archive "$NEW_ARCHIVE"
tend-server archive resource --archive "$NEW_ARCHIVE" --id "$RESOURCE_ID" --index 0 > "$RESOURCE_DOWNLOAD"
tend-server archive export-seed --archive "$NEW_ARCHIVE" --selection "$STATIC_SELECTION" > "$EXPORTED_STATIC_SEED"
tend-server archive seed --archive "$NEW_ARCHIVE" --selection "$STATIC_SELECTION" --bindings "$FRESH_HOST_BINDINGS" --target "$EXCLUSIVELY_NEW_DIRECTORY"
```

Selection 的空列表代表不导入；host/project 引用未选 repo/template 时明确失败，不自动扩选。Resources 列表每项包括 id、kind、scope、status 与显式 path 或缺失 reason，可带 host_id/run_id/thread_id/parent_thread_id；journal_schema 由实际采集读取，不由清单声称。Reader 只接受固定历史 kind/opaque ID，resource 只接受 manifest ID+文件序号；内容仅按字节输出，不执行附件。

Seed 成功只输出 seeded_offline 回执，凭据保留在新目录的私有 bootstrap-secrets 文件；bootstrap-config 是静态配置片段，尚需本机 owner/network/runtime 明确组装，不是可直接激活的旧配置拷贝。失败留下未完成目标供诊断，不能将其当成功新库或覆盖重试；保留它并选新目标。Archive 缺失/损坏/unsupported 时返回失败，原 DB/WAL/SHM 和未完成 capture 证据不删除；修正实际缺口后选新的 archive 输出路径重做。

本片未修改在线 API/schema/protocol，也不提供恢复归档为可写或重新启动旧工作的入口。包级只读接口供后续 owner API/UI 复用；实际 HTTP archive 路由、在线维护/排空、P4 schema58 组合 fixture 和部署演练均仍待后续片完成。

## 候选独立预审修正

r1 预审两项实际缺陷均采纳。Git remote 继续沿既有精确地址语义保留：官方 scp-style、SSH URL 用户名及本机路径均不是普通 URL 凭据；HTTP(S) 任何 userinfo 都要求重新提供无凭据 remote，其他显式 URL 拒绝嵌入密码；SSH/scp 用户名继续允许，不归一化 remote 或改变 RepositoryRemoteKey。新库目标隔离先统一 ArchiveRoot/target 的绝对路径基准，再沿用原目录关系检查，防止正常相对路径参数将新库写入档案。两项都有实际 r1 red 与 r2 定向回归；相对路径同时经过 Initialize 与实际 main CLI，验证拒绝后档案目录、字节及 Verify 不变。其余合同、schema 支持范围和 P8b 未交付边界不变。

后续窄复核补采纳 HTTP(S) username-only userinfo 的实际旧 token 复制缺口：该语法可能承载 access token，不能因为没有 Password 字段就导入 export/bootstrap。实际 ExportSeed 的 HTTP/HTTPS token 负例先 red 后 green，保留 SSH 用户名正例；只按 transport+userinfo 做最小判断，不猜 token 格式或扫描任意文本。r1/r2 原件与既有证据均保留，未扩大 schema/生命周期范围。

## 与当前 hosted 业务树组合

本片将已审离线归档实现、owner 只读归档 API 和真实历史 fixture 组合到 P4 业务树。共享 server 入口保留 named hosted 配置、登记和执行适配；`archive` 子命令仍在正常配置、凭据和业务服务初始化前分流。归档 API 的静态 reader 装配不改变当前业务的生命周期或唤醒。Schema/TypeScript 从同时包含 hosted 与 archive roots 的协议定义重新生成。

当前 reader 仅接受 server schema 53–57、runner journal 10–12；P4 当前 schema 58 不在支持范围。历史 fixture 逐个执行真实 1–57 迁移，当前服务及新 seed 目标仍使用最新 schema。组合不包含在线 freeze/drain/cutover、P5 业务、UI、真实归档采集或部署。候选定向检查与正式完整检查、独立 review、CI/合并和真实验收分别记录，不以隔离候选测试替代正式验收。
