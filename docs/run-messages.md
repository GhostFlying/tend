# 向运行中的 worker 补充上下文

主代理使用 `runs_send_message` 给明确指定的原 run 追加文本，使用 `runs_read_messages` 查询结果。补充消息不改变正式 spec、输入绑定、验收条件、权限或 owner decision；需要这些变更时继续使用原有机制。等待 `ask_owner` 的问题仍用 `decisions_resolve` 回答，普通补充消息不解除该等待。

## 主代理工具

先用 `tasks_read` 读取原 task 的当前运行事实、spec、代次和控制修订。发送参数：

```json
{
  "run_id": "run_example",
  "task_id": "task_example",
  "spec_version": 1,
  "generation": 1,
  "expected_control_revision": 1,
  "text": "复现时请一并核对已有失败样例的输入格式。",
  "commit_seq": 2,
  "reason": "补充现有范围内的调查线索"
}
```

托管主代理需要当前来源消息和递增的 `commit_seq`，来源由 Tend 的工具调用上下文注入，不由正文指定。外挂 MCP 不要求 `commit_seq`，记录已认证客户端及可用的原生 session 身份，不能伪装成直接 owner 消息。正文声称自己是 owner、要求提升权限等均不影响身份和正式任务协议。

工具返回 `request_id`、请求状态和 `outcome`，后者包含 `message_id`、投递状态、task/run/action/spec/generation/control_revision、序号与认证来源。必须保留请求身份：请求结果丢失时用 `requests_read` 查询，安全重试使用相同 request_id，不能换新 ID 模拟重试。发送只接受 managed 控制下 running 或 waiting 的原 run；目标不一致、旧 spec/代次或旧控制修订会拒绝。

接收能力冻结在原 run 分配时；后续 host 心跳宣告新能力不会给旧会话添加工具，旧 run 返回 `unsupported/run_not_negotiated`。有尚未确定消息的开放 run 拒绝由缺少 `run-messages-v1` 的 runner 接管，防止升级或回退丢失投递责任。

查询参数为 `{"run_id":"run_example","after":0,"limit":20}`。返回 `messages`、`more` 和 `next`，按 run 内单调序号分页，最多 100 条。每条消息包含冻结来源、当前结果、固定失败原因与追加历史；查询不消费消息。工具同时在托管主代理和外挂 MCP 注册，可通过工具发现读取完整契约。

文本为非空 UTF-8，最多 16 KiB **字节**；允许换行、制表符和回车，拒绝其他控制字符。文本保存在需要合法身份访问的账本中，不复制到普通诊断日志；发送凭据和 provider 错误原文不进入消息回执。不要在补充上下文里传递凭据。

## 执行器与状态

| 路径 | 原地接收方式 | accepted 的证据 | 限制 |
| --- | --- | --- | --- |
| TraeX app-server | `turn/steer`，固定原 thread 与 expectedTurnId | 返回相同 turnId 的原生接受回执 | 活动 turn 必须可 steer；结束、review、manual compact 等拒绝，不开新 turn |
| Orca TraeX / Codex TUI | 原 run 专属 MCP `receive_messages`，随后 `ack_message` | 原领取实例的显式 ack，reason 为 worker_acknowledged | 协作接收；在开始、长操作后、提交结果前检查。未检查保持排队，没有即时投递时间保证 |
| 旧 runner / 未协商的执行器 | 明确 unsupported | 无 | 不发送终端按键、不取消、重启或续开模拟 |

`accepted` 仅表达执行器接收证据，不表示模型已经阅读、同意或完成要求。实际响应体现补充信息是另一个可审查的证据，任务验收仍独立进行。

| 状态 | 可证明的事实 | 重试边界 |
| --- | --- | --- |
| queued | 已保存，尚未领取 | runner 离线时等待；恢复后仍须通过原作用域检查 |
| dispatching | 服务端已授权这一实例的一次投递；未获得接受回执 | 不重复原生调用，不向新工具调用再次返回正文 |
| accepted | 原生回执或原 worker 显式 ack 已确认接收 | 不重发；不等同消费或执行 |
| failed | 未投递或明确被拒绝；有固定原因 | 本消息结束；不自动重新创建消息 |
| unsupported | runner 或原地接收方法不支持 | 本消息结束；不自动降级为其他执行方式 |
| unknown | 曾可能投递，但结果不能确认 | 不重发，也不越过它投递后续消息；可接收真实晚到回执 |

run 内最早的 queued 消息先领取。dispatching/unknown 阻止后续投递，failed/unsupported/accepted 可进入下一条。暂停、取消、takeover、正式 spec 改变和原 run 结束使未领取消息明确失败；可能已经投递的消息保留 unknown。结果晚到时仍保存事实，不能激活旧 run 或影响后续代次。控制修订变化不会把旧消息热迁移到恢复后的执行。

runner journal 在调用原生 steer 或返回新工具消息前保存一次投递意图，并先保存结果再上传回执。连接中断只重试相同服务端请求。重启后：未调用的已领取消息记录 worker_ended；可能已调用但无回执记录 receiver_restarted/unknown；已保存回执按原请求身份上传。重新启动的 worker 不会收到旧正文，也不能确认旧实例的未确认消息。相同 ack 的安全重试不重复正文或业务效果。TUI 同进程内领取或 ack 超时后，下一次 receive 先用原 request_id 核对未完成领取或上传已保存回执；没有已保存回执的 offered 正文仍不重发。原生 session/terminal 被替换时，旧未确认领取记为 unknown，新实例不能替它 ack；已保存的真实 ack 仍可重传。

## 隔离复现与上线

默认单元测试不联网、不调用模型。真实复现需当前主机允许的执行器及原有配置；不读取或复制 owner 凭据，不修改生产 server、runner、配置或既有 worker。`TMPDIR` 指向任务 scratch，保留独立 ledger、journal 与 evidence.json。使用：

```sh
TEND_RUN_MESSAGES_REAL=1 make it PKGS=./internal/e2e
```

仅运行本验证可用 `go test -race -tags integration ./internal/e2e -run '^TestIntegration_RunMessages$' -v -count=1 -timeout=15m`，同时设置 `TEND_RUN_MESSAGES_REAL=1`。默认模型为 `TEND_TRAEX_MODEL`（GPT-5.6-Sol）；`TEND_TRAEX_BIN` 可指定现有允许的 app-server 入口，`TEND_ORCA_BIN` 可指定允许的 Orca 入口。Orca 默认同模型，`TEND_ORCA_MODEL` 可选择模型；显式空值沿用既有主机 launcher 配置。

验证为两条路径各创建独立任务，发送时原 agent 正在执行受文件 gate 控制的命令。随机 codeword 只通过主代理补充消息发送，随后原 worker 的响应必须体现收到它；核对只有一个 implementation run、一次 worker Start、原 session/terminal 或 thread/turn 身份连续。接受回执、后续响应与独立结果验收分别记录。验证不会向当前任务或生产 worker 注入测试消息。

本地实现不代表生产上线。上线需一起发布支持 `run-messages-v1` 的 server 和 runner，并按主代理工具契约变更的既有交接规则刷新工具。server schema 到 50，runner journal 到 10；通信协议仍为 1。回退到不能读取新 schema/journal 的旧二进制需要既有发布预检，不直接替换运行中的程序。TUI 需执行本轮新增 brief 和原 run 工具桥；没有接收工具的旧活跃会话不能被当成已支持。

## 最终阶段与历史回执

专用消息 journal 与 worker 完成记录分别保存。worker_done/submitting/releasing 先验证原 worker 已停止，再在最终报告前恢复回执；停止与 fencing 不被回执网络重试阻塞。runner 的独立恢复循环也读取这些阶段与 done 的已保存回执，不依赖 hello 再分配，不启动 worker、不调用执行器、不重发补充正文。收到的 accepted 回执在上传响应丢失、最终结果已提交或 runner 重启后仍以原 request_id 和原 payload 重试。完成前无法上传时保留最终请求与回执；done 记录的历史欠账也持续重试并计入维护 pending operations。

原生 invoked 意图没有回执时只报告 unknown/receiver_restarted，不能假称 accepted；恢复不重复调用原执行器。回执第一次保存即冻结请求身份，完成恢复与上传重试并发时仍返回同一个原信封。

`POST /runner/v1/rpc` 的 `runs.report_message` 可在结束后使用原 run token 报告真实迟到事实。它要求当前合法 host/node、原 lease 派生 token、正确 run/role，以及已领取消息完全匹配的 message_id/receiver/thread_id/turn_id。回执通过 app 的去重和单事务审计更新投递历史；它不修改 task/run/spec/decision，不续租、不恢复控制或领取。普通消息 GET、文件/输入 GET 和其他 RPC 仍拒绝 released token。被撤销的 host 或错误节点不能使用历史回执路径。

## 隔离验证入口

`TestIntegration_RunMessages`（`integration` 标签）创建独立账本、journal、workspace 与原生执行器，发送时用命令进程身份验证 agent 正在执行，核对同一 run/session 的后续响应及消息回执，并清理它创建的 provider 资源。设置 `TEND_RUN_MESSAGES_REAL=1` 后运行该测试；通过 `TMPDIR` 将证据与临时文件放在任务自身 scratch 中。当前 Orca 的 TraeX TUI 不接受 launch-time `--model`，可显式设置 `TEND_ORCA_MODEL=""` 省略该参数，使用其已有配置；app-server 仍使用 `TEND_TRAEX_MODEL`。

启动测试前，必须在测试子进程环境中移除全部 `key.upper().startswith("ORCA_")` 的项；不修改外层 Orca 的环境，不修改模型 home 或鉴权配置。保留的 `evidence.json` 包含发送时命令身份、原 thread/turn 或 terminal/session、消息来源与历史、原 worker 的补充响应和启动次数。仅操作本次隔离测试创建的资源，不能向已有 worker 注入测试信息。
