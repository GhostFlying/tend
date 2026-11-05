# 显式问答续接入口的兼容修正

## 已确认缺口

D174 初版把旧 `decisions.respond` / `resolve_from_message` 未携带策略的合法输入，
从 answered 与真实 stopped 齐备后创建 successor 改成持久等待。旧 handoff 合同明确承诺
前一种行为，旧请求并非未授权或无效；N/N−1 测试通过与新增 JSON 字段可选都不能证明
默认语义兼容。初版设计结论不成立，不能以文档声明免除兼容要求。

此处首先是 owner operation 的语义变化，单独提升 runner wire 版本不能解决旧 owner
客户端的行为变化。本修正采用新增显式 typed operation / opt-in，保留全部旧有效输入、
原请求回执和旧 handoff/yield 默认，不改变协议代际，也不添加旧 wire 通信适配器。

## 实施范围

1. 新增 owner `decisions.respond_continuation` 及托管对应入口，必须显式提供完整策略。
   新 CLI 子命令、协调工具及能力目录使用该入口；原无新字段的命令、字节与效果不变。
   既有 Attention 答复可通过显式非空 typed 字段 opt-in，省略时保留旧语义。
2. 旧 AnswerDecision 不生成默认 wait policy。无 policy row 的旧 handoff 仍沿原
   answered/stopped/current 合同续接。只有新的显式 wait 才持久等待；dependency 与
   continue_now 保持已实现的准确事实、停止证明与双 CAS 恢复合同。
3. 查询对无 policy 的历史合同明确显示 legacy，而非伪造一个旧 wait 决定。显式的新
   resume 可以记录自身权限与请求，不回填原 answer policy/request；原 answer replay
   与历史 successor 不变。
4. 更新 D174 设计、帮助及主 Agent 指引，区分旧自动续接入口与新条件续接入口。
   不从自由文本推断条件，不以修改旧输入默认值强制用户迁移，不扩大到 runner/任务恢复。

## 验证

- 恢复旧 N/N−1 正例：未带策略的原答复在 answer-first / exit-first 两序均按旧合同续接。
- 新 typed operation 缺策略必须拒绝；显式 wait 的相同矩阵保存等待且不创建 successor。
- 保留 typed now / dependency、真实完成、重启、当前合同、双 CAS、原请求/unknown 回执
  与 CLI/API/tool 完整输入的已有验证，增加旧/新入口对照。
- 初版源码与错误结论/原 RED 历史保留在原 Git 对象和机器私有收据；不改为正确历史。
- 冻结新 head 后重新定向验证和 CI；最终完整检查、独立 review 仍覆盖最终候选。

Refs: D174。

## 独立评审追加修正

本轮完整评审发现三处必须修复的边界，原冻结版本和失败证据保留：

1. 旧 `decisions_resolve` 使用原 map 载荷，始终包含 `option` / `text` 空串键。
   不能改成 `omitempty` struct，否则旧合法 callback 的同 request/native identity
   跨版本回放会变为 conflict_payload。新增 typed 入口继续使用自己的完整载荷。
2. 两条 Attention named tool 描述明确省略策略保持 legacy 自动续接；需要等待必须
   显式 wait，不能承诺省略时只存答案。
3. dependency 是真实 Task 等待边，必须并入既有 canonical Issue/Task 和 legacy stage
   的同事务等待图。回答入账后、调度前校验候选边；后续 Issue 依赖/阶段/primary
   修改复用同一收集器，不能只在新 answer 入口单独判环。原 task_dependencies 历史
   事实也参与，当前不新增其写入口。无 canonical Issue 时仍校验 Task 间等待。
   已结束合同或已实际领取 successor 不再形成旧等待；当前待停、等待依赖、已排队未
   领取的 continuation 都保留等待边。依赖仅 exact spec 已完成时不再等待，不把
   stale/unknown 当已完成。复用现有纯图环检查，不新增全局刷新、轮询或 workflow。

验证先建立真实 RED：旧完整载荷成功 receipt 后以同 callback 回放，覆盖 option-only /
text-only 和显式/native request identity；两个 stopped handoff 的 A→B/B→A；现有 Task
前提与 continuation 混合环；后续 canonical/legacy plan 加边关闭环。GREEN 同时验证合法
无环链、原回答/策略/版本/失败回执原子性与原有兼容矩阵。只执行工具包全量和相关图/问答
边界，不重复完整检查。最终实质 delta 交独立 reviewer 评估复审范围，不沿用旧结论。
