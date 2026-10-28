# P2a：本地 Agent 定义与公共投影基础

状态：纯配置基础已实现，定向 race/vet 和本地完整 make check 已通过，正式分片独立人工及规定 CLI review 均通过，无 findings；PR CI、合并未完成；尚无生产加载、登记或执行入口，P2 后续分片未实施，本批不部署。Refs: D123；依赖 P1 身份合同，但本包不导入 P1 实现。固定 base 为 `9b7b85530b65dc2516425c8e7a131d762ead08a1`。

## 范围与依赖

新增 `internal/agentconfig`，仅依赖标准库，交付严格 JSON 解码、typed 校验、稳定身份、不可变配置对象、公共投影及确定性 hash。本批没有既有生产包导入它，不改 protocol、schema、app、runner、harness、factory、journal、CLI 或 UI，不增加 provider、别名或第三方依赖。它不读取文件，不访问网络、时钟、随机数或进程。

本地配置不属于 runner wire payload，不放入 protocol；CLI 继续只依赖标准库与 protocol。未来 loader 在 runner 本机配置层使用纯包，cmd 只解析配置与组装。此处不实现加载、HuJSON 标准化、密钥查找、登记、reload、磁盘 snapshot、server category、调度或 run profile 转换，既有 worker 默认行为不变。

## 身份、定义和支持边界

Agent 稳定身份为 `execution_environment_id` 与 `local_id` 两个独立分量；切换 RuntimeBinding 引用不改变身份，不拼接成可能碰撞的字符串。RuntimeBinding 和 Agent 各自保留正 revision，每个 Agent 支持多个 categories。Categories 和声明 purposes 只表达分类与用途，不授予 task/run、owner 或正式验收权限。

`Decode` 精确匹配字段，拒绝未知字段、重复字段、错误大小写、错误类型、显式 null 结构/数组/元素、非法 UTF-8、尾随 JSON 和整数溢出。`Validate` 同样检查 typed 调用者，拒绝重复身份/分类、缺失 binding、不支持 kind/CLI/purpose、缺 model、非法 effort 以及 Orca effort-only。错误仅含稳定 code 与 schema path，不回显私有值或原生解码错误。

定义中的 model/effort 是请求，语法遵循当前 execution profile 合同，不证明模型 catalog、CLI 安装、认证或参数实际生效。Runtime kind 仅声明 app-server worker、Orca codex/traex 和 hosted manager；不以声明代替运行验证。非空 skills、自定义 MCP 配置明确拒绝；命名角色指令尚未支持，`instructions`、`role_instructions` 等字段由严格 decoder 拒绝。

所有公共对象均为 `registration=staged`、`available=false`。已启用的 hosted binding 返回 `isolation_not_verified`，其他已启用 binding 返回 `runtime_not_verified`；禁用定义或 binding 分别返回明确原因。配置 enabled 不构成实际可用、业务授权或 worker slot 占用。

## 公共投影和初始格式

`Definitions` 持有输入副本，`Public` 每次返回深拷贝。公共投影只包含白名单 metadata；typed `SourceKind` 可公开，私有 SourceReference.Reference、ExecutableRef、凭据、原配置、原指令及私有原文摘要不投影。来源类型由调用者提供的 typed provenance 校验后取得，不允许 JSON 配置自报公共 `source_kind`。

本地 Config 和 PublicInventory 的首次正式 `format_version` 均为 `1`。公共 canonical 编码固定 struct 字段顺序、集合排序、空数组 `[]`，使用 Go JSON 编码且无尾换行，不宣称 RFC 8785。公共 digest 为 `sha256:` 加小写十六进制 SHA-256；输入为 UTF-8 `tend-agent-public-v1`、一个 LF 字节和紧接其后的 canonical JSON 字节。

同 SourceKind 仅修改私有来源引用或 ExecutableRef 不改变公共字节/hash；SourceKind、请求 model/effort 等公开内容变化应改变 hash。公共 hash 不是完整本机 snapshot 等价性、私有安装一致性或恢复证明。本地格式、公共投影格式与 runner 通信协议分别定义；本批不改通信、不实现历史草稿兼容 adapter。

## 文件与实施顺序

先写本计划，再实现以下四个文件：

| 文件 | 职责 |
| --- | --- |
| `internal/agentconfig/config.go` | typed 定义、稳定身份、校验、安全错误和输入复制 |
| `internal/agentconfig/decode.go` | 精确字段与结构的严格 JSON 解码 |
| `internal/agentconfig/public.go` | 深拷贝白名单投影、不可用原因、canonical 编码/hash |
| `internal/agentconfig/config_test.go` | 结构、身份、边界、隐私和确定性行为测试 |

同步更新 D123 与总计划 P2 的本片真实进度，不宣称整体完成。定向 race/vet 和完整 `make check` 后固定 patch 交独立 reviewer；PR、CI、合并、部署及 live 状态分别登记。没有生产调用点，不调用真实模型或业务数据。

## 验收与后续启用门槛

测试覆盖合法与非法 JSON、typed bypass、安全错误、稳定复合身份与换 binding 不换身份、多 category、unsupported options、公共 SourceKind 变化改 hash、同 kind 私有引用变化不改 hash、输入集合顺序不改公共字节、返回切片隔离、空库存 canonical 编码及全部 staged/unavailable。

后续 P2 必须补齐受控角色指令注入，并保留 Tend harness 执行合同和正式验收隔离，不能把 model/effort 基础当作完整 Agent 配置交付。生产加载、登记或选择启用前，还必须完成：

- 本机完整不可变 snapshot，原 runtime、adapter 和 artifact 可验证的恢复 pin；缺失明确受阻，不静默换 Agent。
- 同 host 全局 workspace/backend 资源协调；首次选择同时满足实现和同 workspace host 正式验收能力。
- inventory 双向能力协商；若改变旧有效请求或默认语义则完整执行协议升级和 N/N−1 退役合同。
- hosted main/issue/consultation 的禁用原生执行隔离实证验证，验证前不标可用；纯管理会话不占 worker slot。

这些门槛不在本片实现，也不因纯配置测试通过而解除。
