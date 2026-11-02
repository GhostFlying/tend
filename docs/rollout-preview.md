# 原生执行过程预览

从任务的「执行与日志 → 执行过程 → 查看完整过程」或 Issue 的执行诊断入口进入只读 monitor。横向概览、记录列表和详情共用所选 run；不会启动模型、重试执行或接管终端。Issue manager 共享会话的完整诊断仍使用 Issue 既有入口，run 预览只读取已确认的原生 turn。

## 来源与保留

默认读取 runner 的原生记录。通过「记录来源」可以独立查看服务端已经采集的日志，后者不代表完整原生历史。执行状态始终来自正式账本。central TraeX 执行按照冻结的 runtime 绑定读取 server 上原本就存在的 native 原件，不经 runner 查找同名文件，也不新增正文归档。

runner journal 的追加迁移 0015 保存 deployment/run/session/turn 和原件位置，与待上报事件及完成执行清理分离。一个 run 的多次会话来源依次保留。原生记录中的明确工具输出关联在读取时解析，关联目录与原件一起受清理保护；不把命令参数或普通文本中的路径当作读取权限。Tend 的 workspace 清理会拒绝删除已登记的原件和原生 artifact 目录。

不保证外部删除、磁盘损坏、runner 退役后的恢复，也不提供打包下载。升级前已经结束的执行仅在现存 journal 或服务端会话身份与原生记录足以确认归属时读取；没有证据时显示「来源无法确认」。manager 历史必须有对应 run 的成功原生 turn 回执。

## 配置与兼容

runner 配置可选 `rollout_roots`，每项为本机允许搜索和读取的原生 sessions 目录。省略时使用 `TRAE_HOME/sessions`（默认 `~/.trae/cli/sessions`）和 `CODEX_HOME/sessions`（默认 `~/.codex/sessions`）。自定义 provider home 时将实际 sessions 目录加入该配置；显式空数组关闭 runner 预览能力。Web 不接受任何本机路径。

server 的 central 原件目录来自 `native_workers.traex_home`，未配置时使用其本机 `TRAE_HOME` 或默认 TraeX home。只有冻结 inventory 明确为 `central-traex-worker` 的执行才能使用这一读取端。

本增量不改变 runner wire 2/1。新 runner 声明 `rollout-preview-v1`，只有 hello 回执确认后才建立诊断长轮询。旧 runner 显示不支持，旧 server 不收到新读取请求。通道独立于领取、slot、维护模式和执行租约。

## 读取与故障

打开 monitor 后约每两秒读取增量，关闭后取消。每页最多 100 条及约 1 MiB 记录正文；单条正文每次最多 64 KiB，滚动到末端自动继续读取。读取器保留分页偏移索引，后续只扫描新增完整 JSONL 行；单行解析上限 16 MiB，超出或未知格式会明确提示。未完成尾行等待下次读取。

文件替换、截短、消失和恢复、runner/server 重启可能使旧游标失效。页面保留断线前已读内容，重连后续读有效游标；游标失效则重新读取窗口并提示来源变化。原件缺失、外置输出缺失、未生成记录、来源无法确认、格式不支持、runner 不可达和超时分别显示。

owner 读取接口为 `GET /api/v1/runs/{id}/preview`。`before`/`after` 与 `generation` 分页；`record`、`generation` 及可选 `field`/`reference`/`offset` 读取单条长内容。runner 使用独立的 `GET`/`POST /runner/v1/rollout-preview` 领取短期读取请求、回传结果。请求取消及结果仅在 server 内存中存在，不进入业务请求去重、事件、正文日志或用量表。正式鉴权和当前实例校验在每次读取/回传执行。

## CLI

`tend run preview <run-id>` 输出完整状态与当前记录窗口的 JSON，使用既有 owner 登录。分页和详情使用与 Web 相同的 generation：

```sh
tend run preview <run-id> --before <before-seq> --generation <generation>
tend run preview <run-id> --after <after-seq> --generation <generation>
tend run preview <run-id> --record <seq> --generation <generation> --field input
tend run preview <run-id> --record <seq> --generation <generation> --field output --offset <next-offset>
tend run preview <run-id> --record <seq> --generation <generation> --reference <reference-id> --offset <next-offset>
```

`--field` 可选 `raw`、`text`、`input`、`output`，默认读取单条原始记录。输出保留 `state`、`origin`、`generation`、分页边界及 `content.next_offset/more/references`，不会自动拼接整份原件。若原件变化使 generation 失效，去除游标重新读取；不可达或超时可重试相同读取，不需要业务请求恢复或重放执行。
