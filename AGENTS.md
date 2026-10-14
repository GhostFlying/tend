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
    tend-runner/           runner 入口：只做配置解析与组装
  internal/
    protocol/              线上协议：请求信封、错误码、payload_hash、领域枚举；导出 JSON Schema
    domain/                纯领域逻辑：状态机、完成条件派生、动作代次规则；不做 IO
    store/                 SQLite、嵌入式迁移、仓储、事务辅助
    app/                   用例层：请求去重 + 领域规则 + 存储 + outbox，在一个事务内完成
    api/                   浏览器 HTTP API 与 SSE
    tools/                 主 Agent 工具面（app-server 动态工具）
    runnerapi/             runner 协议的服务端
    scheduler/             排队与资源分配
    dispatch/              outbox 投递
    contextbuilder/        ContextPacket、快照、delta、水位
    harness/               harness 抽象接口
    harness/traex/         traex app-server 客户端；generated/ 存放生成的协议类型
    runner/                runner 客户端：journal、租约、自我 fencing、worker 驱动
    netx/                  tsnet 封装（listener、WhoIs）；测试时替换为本地 listener
    testkit/               fake harness、fake runner、可控时钟、ID 生成器、故障注入
  web/                     Web 客户端（Vite + React + TS），阶段 C 起
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
cmd → api / tools / runnerapi / scheduler / dispatch / contextbuilder
    → app → domain → protocol
          → store  → protocol
runner → harness, protocol, netx（runner 不 import 服务端的任何包）
harness/traex → harness, protocol
```

- `domain` 不依赖 `store`、网络、时钟或随机数：所有输入通过参数传入，输出是决定（新状态、事件、待创建动作），方便穷举测试。
- 服务端的 handler（`api`、`tools`、`runnerapi`）不直接写数据库，所有有副作用的操作都走 `app`。
- `internal/` 之外不放 Go 业务代码。

## 2. 工程约束（不变量）

以下约束对应设计中的正确性要求，评审时逐条检查。

1. **请求身份**：有副作用的入口都必须带 request_id，并经过 `app` 的去重。同一请求重试时沿用原 ID，新的业务动作使用新 ID（D11）。
2. **单事务**：状态变化、领域事件、outbox 消息和请求记录在同一个事务里提交。外部副作用只能由 dispatcher 在事务提交后发出。
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

## 3. 代码风格

- Go：遵循 [Google Go Style Guide](https://google.github.io/styleguide/go/)，使用 gofmt / goimports；`go vet` 和 `staticcheck` 必须无告警。
- TypeScript（web）：strict 模式，使用 eslint 和 prettier；API 类型由 `internal/protocol` 导出的 JSON Schema 生成，不手写。
- 代码注释和标识符使用英文；设计文档使用中文。

## 4. 测试

- `make check` = 格式检查 + vet + staticcheck + `go test -race ./...`，提交前必须通过；CI 在每个 PR 上执行同样的检查，外加 Windows 和 macOS 的交叉编译。
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
- 历史保持纯线性：GitHub 仓库只允许 rebase & merge，禁止 merge commit 和 squash；`main` 禁止直接 push 和 force push。PR 合并前先 rebase 到最新的 `main`。
- 远程：GitHub，公开仓库。
- git 身份使用仓库或全局已有配置，不添加 agent、bot 或工具作为作者或 co-author。
- 不提交生成的二进制、数据库文件、`.env`、`.local/`，也不提交 `spikes/` 产生的输出。
