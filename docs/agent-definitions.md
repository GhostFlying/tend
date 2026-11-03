# 编写 Agent 定义

本指南说明当前 Agent 的命名约定（D123）。Agent 在 runner 本机定义，Category 是服务端的跨 runner 工作分类。配置中的 CLI、model、effort 和 runtime 单独表达执行方式。

## 名字由谁决定

用户或编写配置的 Agent 在创建定义时选择名字，并把结果明确写入 `local_id` 和 `display_name`。用户指定名字时采用用户的选择；未指定时，编写配置的 Agent 可以选一个中性、好记的短名，并检查本机是否重名。用户无需逐个手动命名。

当前不需要随机词库或新的生成服务。现有入口接收完整配置，不会自动建议或补齐名字。若以后增加创建界面或 CLI，名字建议应在创建阶段完成，允许修改，再写入完整定义。

## 字段约定

| 字段 | 含义 | 示例 |
| --- | --- | --- |
| `local_id` | 具体 Agent 的稳定 slug | `cedar` |
| `display_name` | 面向人的名字，可以使用中文 | `Cedar` 或 `青杉` |
| `categories` | 可承担的跨 runner 工作分类 | `["implementation"]` |
| `runtime_id` | 执行方式的本机绑定引用 | `orca-codex` |
| `model`、`effort` | 这版定义请求的模型与推理强度 | `gpt-6.1-sol`、`high` |

`local_id` 已承担 slug 的作用，不另加 `slug` 或 alias 字段。名字建议使用植物等中性词，不绑定岗位、模型、CLI 或成本档位；这些属性变化时仍可保留名字。Issue owner 是 Agent 在具体 Issue 上承担的责任，不产生另一套名字或身份。

新定义的 slug 推荐使用小写 ASCII 英文字母和数字，以单个连字符分隔词段，例如 `cedar`、`silver-birch`；开头和结尾使用字母或数字。这里是编写约定，现有校验接受的大小写、下划线、点等 ID 继续有效。

slug 在同一执行环境内唯一，换 runtime 也不能复用为另一个 Agent。重名时由定义者选择其他短名或可读限定词，再提交完整配置；加载器不自动添加后缀。不同执行环境可以使用相同 slug，完整身份始终是 `execution_environment_id` 与 `local_id` 两个分量。显示名只用于展示，不作为选择或鉴权的身份键。

## 配置示例

下面是一个完整的本地定义文件。runtime 的 `executable_ref` 是实际启动命令，需与本机安装配置一起核对；示例不证明该程序、模型或 Category 已安装、登记或可用。

```json
{
  "format_version": 1,
  "runtimes": [
    {
      "runtime_id": "orca-codex",
      "revision": 1,
      "kind": "orca-worker",
      "cli": "codex",
      "executable_ref": "orca",
      "declared_purposes": ["worker_run"],
      "enabled": true
    }
  ],
  "agents": [
    {
      "local_id": "cedar",
      "revision": 1,
      "display_name": "Cedar",
      "runtime_id": "orca-codex",
      "categories": ["implementation"],
      "purposes": ["worker_run"],
      "model": "gpt-6.1-sol",
      "effort": "high",
      "enabled": true
    }
  ]
}
```

这是命名示例，不创建新的全局角色。其他 Agent 可以选用 `birch`、`iris`、`maple` 等名字，其 category 和执行配置分别定义，不建立固定的名字到职责映射。

## 更新与历史

服务端具名定义的 format 2 `hosted-manager` runtime 可显式填写 `cli: "codex"` 或 `cli: "traex"`，表示所配置启动程序的实际 CLI。旧配置仍可省略；目录只展示已知的 app-server 适配器，不从 runtime ID、路径或程序文件名猜测 CLI。更正声明时递增配置及 runtime 版本，原责任继续冻结的旧快照。声明不代表运行就绪，观测仍需独立上报。

`tend agents hosted --json` 与 `tend agents show --host <host-id> --json` 返回相同安全投影中的 `runtimes[].cli`、适配器和精确版本；不会公开 executable 或私有指令。Web 目录将运行位置和运行方式分列，原绑定、版本及观测在详情中查看。

启动、reload、登记、重启恢复及模型升级都不重新生成名字，不从显示名、Category 或模型推导 `local_id`。配置仍明确要求填写 ID 和非空显示名，系统校验格式和同环境内的重复 ID。

只修改显示名时保留 `local_id`，按既有定义版本与本机显式接受、登记流程发布。显示名不参与身份键，但属于定义和公共投影；修改它会形成新的配置快照，已固定的任务和运行继续使用原身份、版本与快照。修改名称不会自动交接现有 Issue owner 或刷新它的执行 pin。

更换 `local_id` 表示另一个身份，需要显式迁移责任并保留旧引用，不能当作显示名更新原地替换历史记录。当前部署中的岗位式 ID 可以继续保留，先改善显示名；新定义遵循本指南即可。

本机操作沿现有 `tend-runner agents status|validate|reload|reconcile|register|registration --config <runner-config>` 入口。`--config` 指向 runner bootstrap 配置，定义文件由其中固定的 `agents.source_file` 引用；接受、登记与实际可用是不同的观察结果。流程及请求重试语义见 [本机配置与登记计划](plans/agent-inventory-control.md)，身份和不可变快照见 [配置基础](plans/agent-definition-config.md)及[本机 registry](plans/agent-registry.md)。


## 自适应并发分组（D153）

并发分组独立于主机 slot，按用户显式指定的共享账号/API 预算归组。
只通过 CLI 管理，Web Agents 的“并发分组”分类只读；不增加配置文件。

```sh
tend agents concurrency create shared-codex --name "Shared Codex" --min 1 --initial 4 --max 8
tend agents concurrency bind shared-codex --environment runner-a --runtime codex
tend agents concurrency list --environment runner-a --limit 50
tend agents concurrency show shared-codex
tend agents concurrency update shared-codex --max 6
tend agents concurrency history --group shared-codex --limit 50
tend agents concurrency history --issue issue-example --session native-session-example --cause congestion
```

绑定使用执行环境与 runtime 的稳定 ID，覆盖其各版本；跨主机或适配器共享账号
时，显式绑定同一组。直接 bind 到另一组会原子转移活动占用。`unbind` 解除单个
绑定；`delete` 要求组中已没有绑定。未绑定的 runtime 保持原准入行为。

组内明确的临时 429/overload 每次减一，三十秒内合并降档，最后拥塞后冷却
三十秒。最近三十分钟的有效观察中无拥塞、累计满载十五分钟、有成功 turn，
并且仍有执行只因容量不足而等待，才加一。空闲、失联和停机不累计健康证明。
修改参数保留学习值，仅按新的上下限截断；initial 不用于重置已有学习值。

输出包含当前有效上限、预留/未知占用、确认运行数、观察开始时间、满载毫秒、
成功 turn 数、等待数和绑定；不能把预留或未知执行算成满载。分组历史保留滚动
七天，拥塞记录保留确切 Issue 与 Tend/native session/turn 来源，升档保留窗口和
需求证据。无 Issue、native ID 或执行端发生时间未提供时如实显示。

读取命令支持完整 JSON；list/history 使用 --after/--limit 分页，history 支持
--since/--until（UTC 毫秒）及 --cause/--issue/--session，实际查询窗口随响应返回。
写命令支持 --dry-run 输出原信封，--request-id/--revision 重试必须保留原输入。
部分参数更新只在首次执行时读取当前值补齐；重放 update 应使用原完整 --data
或 --file，或完整的 name/min/initial/max 标志，不能重读新默认值。
也可将 dry-run 原信封交给 `tend request replay`，以原 request_id 和版本恢复。
