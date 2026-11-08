# 按 Category / Agent 管理扩展

Refs: D198。[实施计划](plans/managed-extensions.md)与 [本机环境](codex-environments.md)分别说明发布合同和本机状态。当前未配置 `managed_extensions` 的 runtime 保持旧行为；程序升级不会自动迁移环境或选择资源。

## 编辑和发布

所有管理写入使用 owner 身份、request ID 与控制 revision；资源安装、移除、组合和指令编辑只改变草稿。`tend extensions plan` 查看完整草稿、当前发布版、候选摘要和受影响 Agent，`publish` 才切换目标。`resolve`、`plan` 和 `--dry-run` 不更改状态。

```sh
tend extensions inventory --kind skill --limit 100
tend extensions skill add --file skill-import.json --revision 0
tend extensions plugin add --file plugin-import.json --revision 1
tend extensions mcp add --file mcp-resource.json --revision 2
tend extensions category show implementer
tend extensions category set implementer --file category.json --agents-md category-instructions.md --revision 3
tend extensions agent set worker --environment execution-a --file agent.json --agents-md agent-instructions.md --revision 4
tend extensions resolve --category implementer --environment execution-a --agent worker
tend extensions plan
tend extensions publish --revision 5
tend extensions releases --limit 100
tend extensions status --environment execution-a
tend extensions doctor --environment execution-a
```

示例 revision 仅用于说明顺序，实际使用查询返回的控制 revision。绑定 JSON 的 `revision` 则是该 category/Agent 自己的版本，两者必须分别保留。set 接收完整绑定，不是字段补丁。

本机完整包导入文件：

```json
{
  "id": "repo-helper",
  "version": "1",
  "path": "/path/to/complete-skill-package",
  "source": {"type": "local"}
}
```

Git 来源使用 `source.type="git"`、`url`、显式 `ref` 和可选 `subdir`；保存实际 commit。npm 使用 `type="npm"`、`package`、明确 `version` 和可选 `subdir`；校验完整性，不执行安装脚本。Git/npm 输入不带本机 `path`。更新使用 `skill update`/`plugin update` 与新的不可变资源版本；同 kind/ID/version 的内容不能被替换。remove 只移出草稿，不能删除仍被组合启用的版本，不操作账号级插件安装记录。

绑定示例：

```json
{
  "revision": 0,
  "choices": [
    {"kind": "skill", "id": "repo-helper", "version": "1"},
    {"kind": "plugin", "id": "sample", "enabled": false}
  ],
  "agents_md": "",
  "bundled_skills": true
}
```

执行只选一个 category，再按完整 `execution_environment_id + local_id` 叠加 Agent 选择。同 ID 的 Agent 项替换 category 项；未声明表示继承，`enabled=false` 明确禁用。AGENTS 按 category 在前、Agent 在后连接。插件自带技能保留包归属，不重复作为独立技能导入；托管名称/MCP 冲突会阻止发布。项目扩展和管理员要求继续按原生规则生效，角色限制优先。

standalone MCP 资源示例：

```json
{
  "kind": "mcp",
  "id": "docs",
  "version": "1",
  "source": {},
  "mcp": {
    "transport": "http",
    "url": "https://example.test/mcp",
    "bearer_token_env_var": "DOCS_TOKEN",
    "enabled_tools": ["search"]
  }
}
```

stdio 使用 PATH 中的 `command`、`args` 和环境变量名称 `env_vars`，在执行主机运行。HTTP 可使用 `env_http_headers` 的 header→环境变量名称映射；不得提交秘密值。发布验证不要求 server 拥有 runner 的凭据；执行端分别报告依赖、环境变量、原生连接/认证及工具目录状态。工具目录存在不等于任意调用已获权限。协调模式不会通过托管 MCP 重新打开被禁止的能力。

## 请求恢复与回退

安装包含独立内容暂存请求和草稿编辑请求，CLI 将两个 ID 输出到 stderr。网络结果未知时先对账原 ID，不能换新 ID 重发未知写入。`extensions reconcile REQUEST_ID` 返回原请求状态；完整重试保留原 request ID、控制 revision 和输入文件。publish 重试必须保留原候选 payload，而不是重新读取当前草稿；可用 CLI 通用 request replay 发送保存的原 envelope。

```sh
tend extensions reconcile REQUEST_ID
tend extensions rollback RELEASE_ID --revision CURRENT_CONTROL_REVISION
```

回退是一次新的发布控制操作，引用已有不可变内容，不改写历史发布记录或已有执行 pin。发布失败保持当前发布版；原请求的已知失败不通过同 ID 自动重做验证。

## 分发、采用与本机配置

server 与 runner 的本机配置可显式增加：

```json
{
  "managed_extensions": {
    "directory": "/path/to/extension-cache",
    "runtimes": {
      "native-codex": {
        "home": "/path/to/existing-fixed-state-home",
        "auth_source": "/path/to/shared-openai-auth.json",
        "base_config": "/path/to/independent-base.toml"
      }
    }
  }
}
```

该段位于现有对应本机运行配置中，不替换完整配置。server 的主 Agent 使用同样的消费端设置。已有 `codex_environments` 映射会被用于保留原本机环境身份；不得为了扩展更新另选二进制、模型、用途或 slots。

runner 仅向已配置 server 鉴权下载并校验内容。状态区分已发布、已下载、组合就绪和实际采用；旧 runner 未协商能力时显示未迁移。worker 新 run 固定发布/category/Agent/revision/摘要，续跑和对账保持原 pin。长期会话只在原空闲、工具/回执和维护门禁允许后重建原生进程，恢复同一 thread；unknown 不触发输入重放。多 category 的常驻/咨询会话必须在现有会话设置中选择，唯一类别可自动选择。

本机启动不使用外层 bwrap。稳定的执行私有配置 home 让不同执行的 AGENTS、配置和可写插件 cache 不互相覆盖，同一执行重启前持有执行锁；持久会话/SQLite/OpenAI auth/日志与用量采集保留固定状态根。MCP OAuth 在该执行的配置 home 原生保存，重启复用，不自动导入个人凭据。个人技能排除清单由启动触发，六小时内共享缓存，非实时目录隔离。历史路径和引用不自动清理。

## 三类现有环境的迁移预览

| 环境 | 内容与组合预览 | 状态路径与回退 |
| --- | --- | --- |
| server 主 Agent | 导入确认的 70 个 skills、6 个完整 plugins；standalone MCP 空；现有 AGENTS 一次导入；其 Codex categories 使用相同基线，Agent 自身为空 | 保留原固定状态 home、auth、线程和角色限制；保持旧配置与快照用于恢复 |
| 已启用旧投影的 runner | 同一 server 发布版，改为按任务 category＋Agent 消费，不再持续投影个人来源 | 保留原环境目录/ID与 run pin；旧 run 继续固定原快照 |
| 尚未启用旧投影的 runner | 下载 server 内容后验证组合；准备失败只阻止对应 Codex 执行器接新任务 | 不复用个人缓存作有效内容；未就绪时保留原运行与配置 |

导入保留已确认的 `lynx-cat → lynxcat` 调整，不重新分组、不导入个人 MCP。实际启用前，在机器私有预览中逐项核对完整配置 diff、slots、Agent 类别/用途/模型和 HOME/固定状态路径；走现有维护门禁。确认所有目标采用后再停止旧 watcher。回退使用发布回退及保留的本机配置恢复，不清除 home、auth、会话、缓存或原生历史；已固定新组合的执行仍需其对应程序和内容完成恢复/对账，不能直接降级抹掉引用。
