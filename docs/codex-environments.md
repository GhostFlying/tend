# Codex 本机环境

`show`/`inventory` 的执行采用记录使用 `--after`、`--limit` 分页，默认且最多 100 条；`adoptions_more` 为真时沿用 `adoptions_next` 读取下一页。控制写入返回更新后的配置摘要及所选快照，完整历史通过 show/inventory 读取。

`tend codex-env` 管理本机能力环境，不访问业务账本，也不安装、卸载账号级云端插件。严格隔离仅支持 Linux + bubblewrap。没有 runtime 映射的服务不受影响。

## 创建与检查

环境描述采用 HuJSON。下面的路径是示意值，使用前替换为当前机器上的绝对路径；`user_home` 必须与保持不变的执行 HOME 指向同一目录。运行 home 必须独立，不能复用主 Codex home 或环境管理目录。

```json
{
  "format_version": 1,
  "id": "worker",
  "home": "/path/to/independent-codex-home",
  "user_home": "/path/to/user-home",
  "source_home": "/path/to/main-codex-home",
  "base_config": "/path/to/independent-config.toml",
  "enabled": true,
  "skills": {"mode": "clean"},
  "plugins": {"mode": "clean"},
  "agents": {"mode": "independent"},
  "bundled_skills": true,
  "auto_sync": true,
  "interval_seconds": 60
}
```

`base_config` 可省略；它是独立维护的 Codex TOML，不是主 home 的完整配置副本。运行 home、SQLite 和日志位置由环境管理控制。插件安装/激活和 marketplace 由清单生成；独立 base config 中按原始或本地插件 ID 配置的 `mcp_servers` 策略会保留。其他 standalone MCP/hooks 必须在独立配置中明确设置，投影不会自动复制。项目配置和管理员约束仍按 Codex 原生规则生效，协调角色原有权限限制优先。

```sh
tend codex-env plan --file environment.json
tend codex-env create --file environment.json --revision 0
tend codex-env list
tend codex-env show worker
tend codex-env inventory worker
tend codex-env doctor worker
```

默认管理目录在 Tend 配置目录的 `codex-env` 子目录。所有命令支持 `--root` 指定其他绝对管理目录、`--json` 输出。所有控制写入打印/返回原 request ID；重试使用相同 ID、相同完整输入与原 revision。使用 `--dry-run` 只打印请求。`plan` 在临时目录生成差异，不初始化运行 home 或管理状态。

创建会执行真实 Codex 的无模型目录探针，校验技能解析、插件安装和启用状态以及 hook 加载。它不证明所有 connector 已授权或工具均可调用。后续运行仍需本机原生 Codex 可执行文件，环境不能替代或重选 runtime 的 ExecutableRef。

## clean、投影与独立安装

两个扩展类别可分别选模式。投影只允许清单中的项：

```json
{
  "skills": {
    "mode": "projection",
    "allow": [{"id": "repo-helper"}]
  },
  "plugins": {
    "mode": "projection",
    "allow": [{"id": "sample@personal"}]
  }
}
```

skills 默认从用户 `.agents/skills` 按目录 ID 查找，可以用 `source_skills` 修改来源，或逐项提供绝对 `path`。plugin 默认从 source home 的已安装 cache 选择原生规则下的活动版本，也可以逐项指定完整包 `path`。plugin ID 使用 `name@marketplace`，包内 manifest 的 name 必须匹配。状态保留原始 ID、生成的本机 ID、内容摘要和版本。

更新环境时提供完整描述及 `show` 返回的 revision：

```sh
tend codex-env update --file environment.json --revision 1
tend codex-env skill add worker --item local-helper --source /path/to/skill --revision 2
tend codex-env plugin add worker --item sample@local --source /path/to/plugin --revision 3
tend codex-env skill update worker --item local-helper --source /path/to/new-skill --revision 4
tend codex-env plugin remove worker --item sample@local --revision 5
```

独立安装先导入目标自有副本，原来源之后改变不会自动更新目标。需要更新时明确调用 update。投影项不能用独立安装命令修改；先修改环境描述，把同一项从 `allow` 移到 `installed`，并提供独立来源。完整更新里的 `installed` 可直接保留 `show` 返回的自有路径。

投影将符号链接解析为普通副本，包含脚本、资源、MCP 和 hooks，不向来源回写。主 home 新增未选项不会被导入。云端插件的账号安装记录、连接权限不是本地副本；移除本地项不会调用远端卸载。账号后台同步仍可能在进程私有 cache 下载未启用包，不能将加载隔离称为账号隔离。

`doctor` 的 plugin_cache 单独统计每次启动的工作缓存（包括异常退出遗留目录）中的已下载版本、属于各自快照的版本和快照外版本；缓存目录存在不代表插件已加载或 connector 已授权。项目和管理员规则仍可允许快照清单之外的项目插件，诊断不会把这些目录冒充成环境白名单项。

## 同步、采用与恢复

```sh
tend codex-env watch enable
tend codex-env watch status
tend codex-env sync worker --revision 6
tend codex-env rollback worker --generation SNAPSHOT_ID --revision 7
tend codex-env reconcile worker --request-id ORIGINAL_REQUEST_ID
tend codex-env watch disable --revision 1
```

watch 使用用户 systemd 服务；enable/disable 管理它自己的 revision，status 返回该值。`watch run` 是前台同步进程入口。只同步 enabled 且 auto_sync 未关闭的环境，默认每 60 秒检查选中内容；没有变化不发布新快照，也不调用模型。同步失败保留有效快照，状态给出原因；超时或退出不明时先 reconcile 原请求，不自动换请求重放未知操作。

用户服务保存启用时的工具 PATH，确保非交互进程也能找到本机 Codex 和 bubblewrap。`rollback` 设置 sync_held，避免下一轮自动同步立即撤销人工回退；显式 sync 或环境 update 解除该暂停。禁用 watch 保留环境和当前快照，即使 bubblewrap 已不可用也可以停止该服务。

`latest` 表示已发布版本，`adoptions` 表示执行实际固定的版本，两者可能不同；pending_generation / pending_reason 说明保留 run 引用、等待空闲、启动失败或待对账等采用限制。worker 和 runner 托管 manager 在首次 run 启动时持久化引用，同一 run 的恢复沿用该引用；后续新 run 使用最新快照。首次启动前还在 run 自己的固定状态目录保存环境目录和 ID；之后更换或移除 runtime 映射不会重定向原执行。功能启用前已有的未隔离线程继续使用其原路径。问题回答产生的新 run 若继续原线程，则从前序 run 的本机状态继承环境身份，并保存自己的引用。本机长期主会话只在 delivery、turn、维护和未确定回执均已结束，且原生 thread 确认为 idle 后重建 app-server，恢复原 thread，不生成业务输入。新进程握手成功仍保留旧引用，原生 thread 加载成功后才确认采用；加载失败且确认关闭后记录被阻止的版本并恢复旧引用，退出不明时保留 unknown。工具 schema 升级继续使用既有隔离与索引交接流程。

`AGENTS.md` 的 independent 模式读取运行 home 自己的文件；sync 模式从 `agents.source` 整文件单向同步，默认来源为 source home 的 AGENTS.md。两种模式都通过新快照采用，项目 AGENTS 保持正常规则。账号凭据不进入快照：运行 home 的 auth.json 显式链接 `auth_source`，默认指向 source home 的 auth.json；其他 connector 凭据需要独立配置。

咨询会话按自己的已安装工具 hash 恢复原线程，不进入全局 Main 的交接上下文。工具 schema 不匹配时保持更新受阻，需要完成咨询范围内的维护；不能为能力更新读取全局 Main 历史或创建替代线程。

## 接入与回退

runner 的本机配置可增加：

```json
{
  "codex_environments": {
    "directory": "/path/to/environment-store",
    "runtimes": {"native-runtime": "worker"}
  }
}
```

server 将相同结构放在 `main_agent.codex_environments`。runner 托管的运行配置在实际执行的 runner 上设置。仅映射原生 Codex app-server runtime，ExecutableRef 仍指向选定的本机 Codex；原有包装器不会被自动替换。

正式迁移前，为 server 和各 runner 分别输出环境描述、模式/白名单、模型、CLI、用途、slots 和本机绑定差异，经部署流程确认后再切换。先用临时环境验证，保留旧配置和旧 home；回退时使用原绑定或保留的快照。`delete` 只退役环境，不清除运行 home、会话、auth 或历史快照；活动/未知执行阻止退役，保留引用可继续恢复。首版不自动回收历史快照。

会话、日志和 SQLite 始终位于固定运行 home。Kaboo 只需统计该 home，不应添加每个 generation 的路径；已有运行 home 未迁移时不修改 Kaboo 配置。

仅需停止新执行时，从 show 返回的完整 profile 只将 enabled 改为 false，然后带当前 revision 更新；这项操作即使投影来源缺失也能提交，保留已有快照及恢复引用。再次启用仍需来源完整并通过校验。runner 未设置 rollout_roots 时自动加入映射环境的 sessions 目录；显式列表和空数组保持原义。
