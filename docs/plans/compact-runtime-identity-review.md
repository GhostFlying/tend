# 原生 compact 恢复的 runtime 与 thread 身份

依据 D150 与未知原生事实不重发约束。07185d38 的独立审查指出：未知 compact 的恢复分支直接读取当前定义，可能把仍可从旧冻结执行恢复的原 runtime 改为新配置，切到另一个 native binding；该分支不能以新配置猜原执行身份。

## P1：在原生发送边界冻结私有身份

新增一个只追加迁移，为 compact 的原 operation 保存私有 runtime snapshot 与原 thread ID。实际 `starting` 与其并发许可、身份 pin 在同一事务提交，然后才发送原生 compact。显式记录 legacy 的空 selection，避免其后配置具名 runtime 被误认为原执行。私有 pin 不进入 owner API、使用量快照或普通协调执行表；不改变现有迁移或 runner wire。

## P2：只按原 native 身份恢复

优先使用该 compact 的私有 pin 并核对原 thread。升级前没有 pin 的旧事实，可从同一 conversation、同一原 native thread 已持久的 hosted execution 恢复；若该 thread 对应多个不同 native binding，或已有具名配置但缺可信历史，不以当前定义猜测，保留 unknown。没有具名配置历史的 legacy 事实仍以原 legacy binding 读取。原 compact outbox request 与较新输入 hold 继续独立，既有主会话回执恢复保持优先。

即使原 thread 明确无法恢复，存在未知 compact 时也禁止 fallback 创建替代 thread；保留原 operation 及待对账事实。恢复只消费原完成事件，不重发 compact、不自动结清较新输入的 Attention。

## 验证与交付

先提交计划，再用已有具名配置/repair 夹具复现 v1 原 compact、v2 当前定义场景；覆盖实际 starting 的新 pin、升级前原 thread 历史恢复、显式 legacy、缺证与歧义不猜、原 thread 拒绝恢复不创建替代。先有限 RED，再最小实现和 race GREEN；保持测试原 deadline。当前 obsolete full 在得到新 TEST_SHARDS1 的 mainthread 阶段证据后按剩余成本取消封存，不再等待无效 App/Other 完整收口。

实际产品变更结束后，以新精确 head 继续同一 PR 的规定独立审查、CI 与一次必要正常完整检查；先前失败、缓存命中、取消、单用例诊断及新验证环境分别保留。此计划不新增用户能力、CLI、API 或业务 replay 入口。
