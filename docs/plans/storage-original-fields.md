# S1/S2 正文字段与读取契约

配套总计划：`storage-footprint-governance.md`，Refs: D225。
本清单区分需要保留的原文、当前查询的小投影和可退出的派生副本。

## 表示与事务

- `retained_original_archives` 只保存一次长归档身份；窗口、原生来源和 chunk 通过
  紧凑关联定位，SHA-256 在实际存储表中使用 32 字节 BLOB。旧表名的只读视图恢复
  历史公开 hash 和 archive ID 表示，不额外存储一份窗口元数据。
- 业务字段使用 `tend:retained-json:v2` 和稳定的内部整数 body ID。可以直接读取的
  原件窗口只记录窗口和必要编码；确有历史字节读取需求的组合规则保存到原件 pack
  一次。业务行不再嵌入规则，也不为普通工具展示的 JSON 外壳创建原件。
- v1 嵌入规则、尚未迁移的 inline 正文和 v2 引用同时可读。既有 JSON 字符串窗口
  可以解码为工具文本，不因转义或空白差异再次复制同一份原文。候选匹配之后仍读取
  原件并精确核验；canonical 索引查询保留显式的非空部分索引条件。
- 新文件先持久化，随后才在业务事务中提交索引与引用。历史分批迁移使用固定字段
  清单、水位、控制 revision 和逐行旧值校验；读取一致后才替换字段或删除旧共享规则。
  物理替换不推进会话内容版本，正常业务写入仍执行原 coverage 触发器。

## 字段清单

| 字段 | 写入与读取 | 迁移/退出条件 |
| --- | --- | --- |
| `manager_native_events.body_json` | 原生原文文件化；DB 只留消费和匹配需要的 thread、turn、item、kind 等小字段 | 包含短事件和旧 v1；消费时还原完整原文 |
| `agent_rollout_events.data_json` | 文件原文；既有 sequence、thread、turn、item、kind、时间保留 | 分页和 Unicode 窗口沿既有接口读取 |
| `run_logs.body_json` | 文件原文；保留实际身份、类型和状态查询投影 | Run/seq 去重、时间和当前恢复读取保持；无输入时生成的小默认事实不是原生原文副本 |
| `background_replies.tool_json` | 仅保留小路由/状态外壳，input/output 使用共享引用 | 旧大 JSON 按字段迁移；发布后的工具行复用同一引用 |
| `background_replies.body` | 大回复保存完整原文 | 发布与 `conversation_messages` 共用，不重新复制正文 |
| `conversation_tool_uses.input/output` | 大文本保存一次，按需读取完整原文 | 新写入不先截断；旧截断文本原样保留，不宣称补回未保存内容 |
| `conversation_tool_uses.native_result_output` | schema 122 起保存同一真实工具业务结果的服务阶段文本引用 | 与原生展示正文不同的实际结果不互相覆盖；大型历史值进入同一迁移清单 |
| `conversation_messages.body`（assistant） | 大业务结果使用原文引用；160 字预览为当前展示投影 | 原始来源、消息顺序和正文读取消费者还原后再解释；owner 的保留字形文字不当作内部引用 |
| `manager_native_requests.payload_json/result_json` | 仅真实 pending/started/unknown 命令冻结输入与未知结果保留 | completed RPC 由 S3 退役，不为普通返回保留缓存文件 |
| `context_packets.body` | built/unknown/failed 的冻结输入完整保留一次 | 确认 injected 后清独立输入，保留 baseline、水位与状态；后续从业务记录生成当前 context |
| `main_thread_handoffs.prompt` | 未完成交接的固定 prompt 使用同一原件设施 | committed 后清独立输入；真实目标、祖先链、来源版本和阶段事实保留 |
| `agent_turn_contexts` | 只含 request/会话定位、context revision 和 settled，小型元数据保留 | 无大输入，不额外文件化 |
| `server_delivery_queries.result_json/late_result_json` | S2/S3 协调退出周期完整查询历史 | 当前共用 PR 视图及真实业务引用证据保留；不为周期派生 JSON 创建原件 |
| `retained_body_recipes.recipe_json` | 旧历史读取兼容，不再产生新行 | 先核对实际业务根；必要原文随业务字段核验并转窗口/pack。119 无根规则暂留；122 后无业务根的退役缓存仅清旧元数据，不创建文件 |

## 验证和覆盖

`storage originals status` 按字段报告 pending，并包含旧共享规则；mutation 回执不在
writer 内扫描全部历史。不能用某六个字段完成代表其余字段也完成。
旧规则扫描按 schema 分水位，避免119阶段的暂缓项在RPC退役后被跳过；清理前再次
核对真实业务引用和精确旧行。缺失的必要原件会失败，已退出且没有业务根的缓存不
因清理而另造文件。
`storage originals verify` 同时验证文件窗口与完整共享值/规则，使用不透明分页游标。
缺失、损坏、无法重建或冲突返回真实错误，不用空 JSON 或裁剪内容冒充原文。

`RetainedArchiveRoots` 包含规则 pack、正文窗口以及 native 已接受但尚未完成 JSONL
索引的尾部。备份必须连同稳定内部 key 及这些原件恢复。此枚举是保守根集，不宣称
仅删除业务引用就已回收所有历史窗口；文件回收仍须叠加保留备份、未知动作和暂存根。

合成回归覆盖旧规则中断/重启、精确读取、Unicode 分页、摘要版本不变、规则损坏、
自包含备份，以及工具和回复发布共用原文。真实冷快照原型只操作独占副本，分别报告
DB 页/空闲页、文件长度及合计；普通复制和文件长度不用于声称 COW 独占物理收益。
