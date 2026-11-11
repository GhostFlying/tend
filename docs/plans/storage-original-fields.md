# S1/S2 正文字段与读取契约

配套总计划：`storage-footprint-governance.md`，Refs: D225。
本清单区分需要保留的原文、当前查询的小投影和可退出的派生副本。

本轮必要性抽查确认，三事件列的传输包装曾被机械地按精确字节归档，产生了不必要的
外壳及规则增量。修正时区分 provider Raw、不可解析的实际工具文本、派生 JSON 结构：
原始内容和未结命令继续保真；派生结构使用紧凑元数据及原件窗口关联，读取时组装合法
JSON，不为键序、空白或固定 null 字段创建文件。三事件历史迁移核验 JSON 语义、
稳定身份、消费水位与分页；文本及真实 Raw 文件仍按原件 hash 核验。同一事件的重报
允许外壳键序变化，拒绝真实内容冲突。先做窗口复用、显式 null/缺省、Unicode、丢 ACK、
重启及损坏的小回归，再用受保护快照的新副本验证；先前增量演练保留为已知缺陷证据。

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
| `manager_native_events.body_json` | 派生事件结构与小状态在 DB，真实 `native.Raw`/长文本共享原件窗口；显式空字段用默认位图 | 包含短事件和旧 v1；消费时组装语义 JSON，保留初次上传 hash 与消费顺序 |
| `agent_rollout_events.data_json` | 真实 provider 大原文引用既有窗口，不能匹配的独有 Raw 保存一次；小状态不文件化 | sequence、thread、turn、item、kind、时间与分页保留；JSON 键序/空白不是外部合同 |
| `run_logs.body_json` | 派生结构保留必要身份和状态，大 provider 内容及不透明文本使用窗口关联 | Run/seq 去重、时间与当前恢复保持，重报忽略外层 JSON 键序空白但拒绝真实内容冲突 |
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
