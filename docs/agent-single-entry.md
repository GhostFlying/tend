# Agent 单入口与角色迁移

format 3 在现有配置结构中保留 `runtimes` 和 `agents`。每个 Agent 的 `runtime_id` 唯一引用已有 env 内的入口，runtime 必须声明 `cli`（codex 或 traex）；模型和推理强度在 Agent 定义上。一个 Agent 的 `categories` 可以是多个委托角色。相同 runtime/model/effort 可以由不同 Agent 共用，不要求执行四元组全局唯一。

迁移前读取已登记 inventory 及本机已接受配置，以 primary 入口为依据，保留 identity、name、local_id、model、effort 和 provider。为改变的 Agent/runtime 分别增加 revision，配置也增加接受修订，再去除 owner 第二入口。托管入口名不代表实际 CLI；必须依据真实配置与可执行文件证据填写。runtime、env、driver 和 transport 不得混称。

建议将 implementation 的 provider/economy 变体合并到 implementation，formal-review 的 provider 变体合并到 formal-review；investigation/planning 保留。先发布活动角色的新描述修订，再发布 Agent 新定义，最后 retire 不再用于新发现的旧分类。旧分类、定义修订、Task/spec/pin/事件仍可读。不要全表替换历史字符串，也不要给所有 Agent 虚加所有角色。

本机 named CLI 路径保持完整：

```sh
tend agents local status --socket "$TEND_RUNNER_SOCKET" --json
tend agents local validate --socket "$TEND_RUNNER_SOCKET" --json
tend agents local reload --socket "$TEND_RUNNER_SOCKET" --request-id "$REQUEST_ID" --revision "$LOCAL_REVISION" --json
tend agents local reconcile --socket "$TEND_RUNNER_SOCKET" --request-id "$REQUEST_ID" --json
tend agents local register --socket "$TEND_RUNNER_SOCKET" --json
tend agents local registration --socket "$TEND_RUNNER_SOCKET" --json
tend agents show --host "$ENV_ID" --json
tend agents hosted --json
tend agents issues --environment "$ENV_ID" --agent "$LOCAL_ID" --scope all --limit 50 --json
```

validate 不代表接受或可用；reload 不代表服务器已登记。unknown 必须用原 request_id/revision/payload 对账。register 复用内部登记 journal，不能手造新请求替换未知结果。历史读取可用 `agents issues` 的 snapshot/agent-revision/scope/state/search/after/limit 筛选。并发仍用 `agents concurrency` 的 list/show/history/create/update/bind/unbind/delete 命名命令；不因为角色合并解除旧 active/unknown 的预算占用。

具体 Agent 可通过 `issue create` 的 `owner` 或 `issue request-owner` 指定，category 可省略，也可仅作工作性质元数据；仅提供 category 时用于发现。完整 JSON、CAS 和恢复参数仍用现有 named 命令：

```sh
tend issue request-owner "$ISSUE_ID" --revision "$ISSUE_REVISION" --epoch "$EPOCH" \
  --data '{"agent_id":"existing-env/ordinary","reason":"Take responsibility with the fixed primary entry"}' --dry-run
```

`issue assign` 和 `finish-assignment` 保留显式责任交接、旧执行停止/未知对账与 epoch fence。分类不赋权；实际入口不支持协调时保持可见阻塞。format 3 Orca 会报告 `coordination_entry_not_supported`，当前版本没有其 owner 协调 E2E，禁止切到 app-server 冒充完成。发布这类迁移前需保留这项未完成责任。

任务所需 review 由创建者策略单独决定，不能从 investigation/implementation 等角色推导。`issue create --data` 可携 `independent_acceptance:{required:false,reason:...}`；未来策略修改使用 `issue set-acceptance-policy`。Task 创建时冻结策略，真实 Assignment/启动提示读取该快照。未带策略的 legacy assignment 继续严格要求独立验收。代码 PR review、完整检查、CI 继续按仓库约定。

内部最小交接使用 `issue share-artifact`，输入含 `internal_handoff:true`、原 `artifact_id`、`ref_sha256`、目标 epoch 和理由，不带 source_message_id。只有后代到祖先的精确 artifact/files 被授予；文件下载验证原尺寸/SHA，不得到源 Issue/兄弟/源 Task 控制权。撤销用 `issue revoke-artifact-share` 的 `internal_handoff:true`、share_id、expected_share_revision、目标 revision/epoch；`issue replay` 或 `request replay` 保留完整原 Envelope。跨树共享继续需要原 owner 授权。

main 无 Issue 地创建人工 action/decision 时，`attention create` 同样必须记录 `human_requirement` 与具体 `human_evidence`，不能把内部材料权限问题直接建成人工待办。

main 通过 `attention escalate` 进入人类待办时，完整 payload 必须包括 `human_requirement`（fact/decision/action）与 `text` 中的具体缺失人类输入，以及原责任 revision/epoch/reason。纯技术授权、内部材料交接、可自行修复的平台问题留在 Agent/main；禁止用已读、隐藏、归档或伪造完成代替解决。

本说明不授权合入、部署、线上定义修改、timer 或原运行重启；这些动作各自需要真实交付合同与独立验证。
