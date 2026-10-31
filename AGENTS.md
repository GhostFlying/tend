# tend 开发约定

本文件约束在本仓库中工作的人和 agent。`CLAUDE.md` 是指向本文件的软链接。

**开始任何任务前，如果 `.local/NOTES.md` 存在，必须先读它。** 其中记录了当前机器上的主机、路径、镜像获取方式等信息（不进仓库，见约束 14）。涉及构建、容器、网络或远程主机的操作时，以它为准。

设计依据见 `docs/design-v0.1.md`（基线）与 `docs/design.md`（v0.2 及以后的决策，编号 Dnn）。

## 1. 仓库结构

```
tend/
  AGENTS.md                本文件
  Makefile                 check / test / build 等统一入口
  go.mod                   单个 Go module：服务端、runner、spike 共用
  .devcontainer/           开发环境（D25）
  .local/                  机器相关的笔记与配置，已 ignore，不提交
  cmd/
    tend-server/           服务端入口：只做配置解析与组装
    tend-sandbox/          独立 Web 开发沙盒入口（D133），不进入发布包
    tend-runner/           runner 入口：只做配置解析与组装
    tend-updater/          固定更新器入口与本机控制命令（D84）
    tend/                  tend CLI 入口：只做组装
  internal/
    serverkit/             服务端共享组装与显式外部边界注入（D133）
    sandbox/               沙盒专用脚本与 demo；正式服务端不依赖
    protocol/              线上协议：请求信封、错误码、payload_hash、领域枚举；导出 JSON Schema
    domain/                纯领域逻辑：状态机、完成条件派生、动作代次规则；不做 IO
    store/                 SQLite、嵌入式迁移、仓储、事务辅助
    blobstore/             不可变原文件的独立目录与流式 IO（D87）
    app/                   用例层：请求去重 + 领域规则 + 存储 + outbox，在一个事务内完成
    api/                   owner 与 agent 的 HTTP API（bearer token，D49）
    tools/                 主 Agent 工具面（外挂经 MCP 桥，托管经 app-server 动态工具）
    views/                 账本的只读视图（主 Agent 工具与 owner API 共用）
    mainthread/            托管主会话（默认不启用，D45、D68）：前台 thread、逐条消息的 turn、上下文注入、工具回调
    runnerapi/             runner 协议的服务端
    scheduler/             排队与资源分配
    dispatch/              outbox 投递
    contextbuilder/        ContextPacket、快照、delta、水位
    harness/               harness 抽象接口
    harness/traex/         traex app-server 客户端；generated/ 存放生成的协议类型
    harness/orca/          orca CLI 客户端与 orca 里的 TUI worker（D63、D66）
    harness/bridge/        TUI worker 的 MCP 桥：runner 本机 socket、worker-mcp、agent 项目级配置（D66）
    runner/                runner 客户端：journal、租约、自我 fencing、worker 驱动
    release/               不可变发布 manifest 与产物校验，只用标准库
    updater/               单实例进程监督与受控安装，不访问业务数据库
    upgradectl/            server 本机升级入口与更新器组装，业务写入经 app
    cli/                   tend CLI：login、MCP 桥、hook 命令、setup、owner 命令（D46）
    netx/                  tsnet 封装（listener、WhoIs）；测试时替换为本地 listener
    webui/                 嵌入并提供 Web 客户端的构建产物（D70）；dist/app/ 由 make web 生成，不提交
    testkit/               fake harness、fake runner、可控时钟、ID 生成器、故障注入
    e2e/                   服务端与 runner 的端到端测试（只有测试代码，可以 import 任何包）
  web/                     Web 客户端（D70）：Vite + React + TypeScript，pnpm；构建到 internal/webui/dist/app/
    src/api/generated/     由 owner API 的 JSON Schema 生成的类型
  spikes/                  阶段性验证脚本，每个子目录有 README；正式代码不得 import
  docs/
    design-v0.1.md         基线设计，只读
    design.md              当前设计与决策表
    plans/                 实施计划（编码前先写）
    findings/              spike 与调查结论
```

### 1.1 包依赖方向

依赖只能自上而下，禁止循环：

```
cmd/tend-server、cmd/tend-sandbox → serverkit → mainthread → app, tools, contextbuilder, harness
serverkit → api / tools / runnerapi / dispatch / contextbuilder / messaging / messaging/telegram
    → app → scheduler → domain → protocol
          → domain
          → store → protocol
runner → harness, protocol, netx, release, updater（updater 仅用于本机状态与回执校验；runner 不 import 服务端的任何包）
upgradectl → app, protocol, release, updater
updater → protocol, release（不 import app/store/runner）
app, store → release（只使用不可变 manifest 数据类型）
app, store → blobstore（仅标准库；原文件不可变暂存与读取）
cmd/tend → cli → protocol（cli 只用标准库，不 import 服务端的任何包）
sandbox → serverkit, runner, testkit（仅沙盒入口使用）
messaging, messaging/telegram → app, protocol
harness/traex, harness/orca, harness/bridge → harness, protocol
serverkit → webui（webui 只用标准库，不 import 其他包）
```

- `domain` 不依赖 `store`、网络、时钟或随机数：所有输入通过参数传入，输出是决定（新状态、事件、待创建动作），方便穷举测试。
- `scheduler` 和 `domain` 一样是纯函数：领取 run 时，`app` 在同一个事务里收集事实并调用它。
- 服务端的 handler（`api`、`tools`、`runnerapi`）不直接写数据库，所有有副作用的操作都走 `app`。读取可以经 `views` 直接查询 `store`，视图类型定义在 `protocol`。
- `internal/` 之外不放 Go 业务代码。

## 2. 工程约束（不变量）

以下约束对应设计中的正确性要求，评审时逐条检查。

1. **请求身份**：有副作用的入口都必须带 request_id，并经过 `app` 的去重。同一请求重试时沿用原 ID，新的业务动作使用新 ID（D11）。
2. **单事务**：状态变化、领域事件、outbox 消息和请求记录在同一个事务里提交。外部副作用只能由 dispatcher 在事务提交后发出。D87 的不可变文件内容可在事务前暂存；暂存不创建有效业务引用，元数据、引用和请求身份仍在同一事务提交。
3. **版本校验**：控制类写入必须校验 `expected_control_revision`。旧版本的结果只作为事实保存，不能触发后续动作。
4. **不伪造确定性**：结果无法确认时记为 `unknown` 并进入对账，禁止因为没收到回执就重发。
5. **可注入的时钟与 ID**：业务代码不直接调用 `time.Now()`、`rand` 或 `uuid.New()`，通过接口注入，测试使用 `testkit` 里的实现。
6. **不用全局可变状态表达身份**：不用包级变量或环境变量表示“当前任务”“当前请求”（v0.1 §7.5）。
7. **context 与 goroutine**：所有阻塞调用都接受 `context.Context`；启动 goroutine 的一方负责它的退出，测试里检查泄漏。
8. **错误**：用 `%w` 包装错误；跨协议边界时映射为 `protocol` 中的错误码，不把内部错误文本直接返回给调用方。
9. **日志**：使用 `log/slog` 结构化日志，带上 trace_id 和 request_id。禁止记录 auth key、token、私钥和完整的用户凭据。
10. **迁移只追加**：已提交的迁移文件不再修改，修正一律通过新增迁移完成。
11. **生成代码**：放在 `generated/` 目录，附带生成命令和源版本，禁止手改。
12. **依赖**：新增第三方 module 时，在 commit body 里说明理由；能用标准库解决的不引入依赖。
13. **密钥**：任何密钥都不进仓库。开发用的 key 放在 `~/.config/tend/`，通过 env file 注入。
14. **公开仓库，不暴露本地信息**：代码、文档、注释和 commit message 中不出现本地绝对路径、用户名、主机名或 ssh 别名、IP、tailnet 名称、账号标识和内部系统地址。机器相关的信息放在 `.local/`（已 ignore）；需要提到时用通用描述或环境变量。
15. **先写计划**：非平凡的功能在编码前先在 `docs/plans/` 写计划；设计变化在同一个 commit 里更新 `docs/design.md`，并引用决策编号。
16. **协议 breaking change 与退役**：server / runner 通信发生不兼容变更时必须递增协议版本，不能只改程序版本、任务 spec 或存储 schema。每代支持 N 和 N−1；提升到 N+1 的同一变更必须实现 N+1、保留 N 的适配与兼容测试、删除 N−1 的通信适配，并增加拒绝 N−1 的测试。删除/重命名字段、改变类型、增加必填条件、收紧旧有效请求校验或改变默认语义都必须评估为 breaking change。
17. **兼容代码有期限**：旧协议适配集中放置，登记 ID、适配协议、退役版本及关联代码/测试，代码用 `tend:compat` 标记引用登记。`make check` 必须拒绝过期适配器或登记不一致；开发者在协议升级 PR 中删除代码并更新设计及发布元信息，不通过删除标记或延后期限逃避退役。通信适配退役不删除已提交迁移、历史任务或产物；旧持久化格式需先迁移或保留专用历史读取。发布前必须确认主机及未结束 run/待对账操作不再依赖已退役协议。

## 3. 代码风格

- Go：遵循 [Google Go Style Guide](https://google.github.io/styleguide/go/)，使用 gofmt / goimports；`go vet` 和 `staticcheck` 必须无告警。
- TypeScript（web）：strict 模式，使用 eslint 和 prettier；API 类型由 `internal/protocol` 导出的 JSON Schema 生成，不手写。
- 代码注释和标识符使用英文；设计文档使用中文。

## 4. 测试

- `make check` = 格式检查 + vet + staticcheck + `go test -race ./...`，加上 Web 的类型检查、lint、prettier 检查、vitest 和构建，提交前必须通过；CI 在每个 PR 上执行同样的检查，外加 Windows 和 macOS 的交叉编译。
- 单元测试默认不联网、不调用模型，使用 `testkit` 替身。
- 需要真实 traex 或 tailnet 的集成测试使用 build tag `integration`，通过 `make it` 显式运行；测试模型使用 `TEND_TRAEX_MODEL`（默认 GPT-5.6-Sol）。
- v0.1 §17.2 的故障场景对应的测试命名为 `TestFault_*`，便于对照检查。

## 5. 提交规范

- 格式：`<type>: <summary>`，不带 scope。例如 `feat: impl request dedup middleware`。
  - type：`feat` `fix` `refactor` `test` `docs` `build` `chore` `spike`
  - summary：英文、小写开头的祈使句，不超过 72 个字符，末尾不加句号
- body 用英文详细描述这次做了什么、为什么这样做、引入了哪些约束；涉及设计决策时引用编号（如 `Refs: D17`），涉及计划项时引用编号（如 `Plan: B3`）。
- body 不写跑了哪些测试、多少测试、测试结果；提交前通过 `make check` 是默认前提。
- 每个 commit 是一个可以独立构建、测试通过的原子变更；重构和行为变化分开提交。
- 所有改动都通过 PR 合入：从最新的 `main` 拉短期分支（`<type>/<topic>`），推送后开 PR。CI（`.github/workflows/ci.yml`，执行 `make check` 和交叉编译）通过后才能合并。
- 每个 PR 合并前都要经过独立 review：在 PR 分支上运行 `codex review --base main -c model='"gpt-6.1-sol"' -c model_reasoning_effort='"xhigh"'`。review 意见由提交者逐条判断：确实的缺陷和值得做的改进在合并前修掉；过度防御的意见（为假设性场景加防护、超出本次范围的扩展）可以不采纳。在 PR 描述里简要记录采纳和不采纳的意见及理由。修改较大时重新 review。
- 历史保持纯线性：GitHub 仓库只允许 rebase & merge，禁止 merge commit 和 squash；`main` 禁止直接 push 和 force push。PR 合并前先 rebase 到最新的 `main`。
- 远程：GitHub，公开仓库。
- git 身份使用仓库或全局已有配置，不添加 agent、bot 或工具作为作者或 co-author。
- 不提交生成的二进制、数据库文件、`.env`、`.local/`，也不提交 `spikes/` 产生的输出。
