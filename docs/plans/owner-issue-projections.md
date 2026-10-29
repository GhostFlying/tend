# owner Issue 消息读取补充（D121）

当前专用page/list/detail/associations投影保留旧默认接口到可见UI切换；实际queueView还使用queue、transcript和单block读取。本片为这三个现有owner端点增加projection=issues的typed wrappers，复用共享正文和已有canonical关联映射，保留原分类分页、Active、精确控制/content revision、附件和作者时间。原LedgerEvent、runner/history不变，不在浏览器把legacy DTO冒充Issue。只增薄reader适配，不修改可见组件或新增缓存/轮询。先实现与真实HTTP/reader回归，再独立review；最终UI同批收口活动旧默认入口。

实际 HTTP 定向 race 已通过：已上传原件的同一 owner 消息经 queue→Active→实际 streaming/completed reply→transcript/block/after/before游标，旧默认与新投影的正文/附件/作者时间、来源版本、控制版本与分页完全保留。实际补充撤回沿原消息ID/控制版本。初始 fixture 曾把队列里的合法 owner 分类维护通知漏算，已改为按真正 SubmitMessage 回执定位待撤回补充，未改生产语义。reader scope/query/abort、typecheck/lint 与正常 schema/typegen 已通过；本片独立审查与最终组合检查待执行。

## 原消息按身份读取

Attention 的 source_message_id 可以指向尚未成为 transcript trigger 的真实原消息，不是 manual_message_sources 的ID。本片增加 owner GET messages/{id}，直接复用既有单行消息视图和 canonical 投影，返回 IssueConversationItem；不建立新source实体、不修改原作者，也不经 agent 资料准入过滤 owner 自己的原文。附件仍用既有下载授权与不可变原件。先正常API/生成验证再独立复核；最晚行动时间与提醒另片完整实现。

原消息按身份读取已通过实际 HTTP race：queued 原消息尚无 block 时可读 owner 原文/附件；正常关联 CAS 后同ID显示新分类、原正文不变；真实 streaming/completed assistant 回复按原作者返回，未知ID为404且需 owner。正常 schema/typegen 及 targeted vet 完成；此读取片独立review尚待，不冒称 Attention 时间/提醒已实现。
