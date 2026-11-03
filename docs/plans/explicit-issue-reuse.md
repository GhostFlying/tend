# 显式复用原 Issue 责任并接纳新来源

Refs: D172。原 HB7/EHBN 的 h-registration-recovery：不同 request_id 不能证明是不同业务责任，但标题相似或同一来源也不能证明相同。已有消息关联是分类能力，只校消息 revision/content revision；它不提供原创建回执、目标 Issue revision/assignment epoch 的完整复用合同。已有 Issue update 要求传回完整 plan，且个人主协调器不能借该入口执行 Issue 技术规划。因此补一个精确的显式 reuse 动作，不增加推断分类器或唯一键框架。

## 合同

- 独立 owner 操作 `issues.reuse` 指定真实原 Issue ID、当前 Issue CAS 与 assignment epoch、确证成功的原创建 request_id、新 owner source/content revision 及其消息关联 revision。原创建身份由服务端只读 metadata 给出，不让调用者猜。
- 验证原 Issue 的 revision1 创建 provenance 与原成功 request record 的 operation/result 确切对应。缺失、失败、未知或未对上的来源不得猜成成功；原 create 仍沿原 ID 对账，不以 reuse 创建另一个 Issue。
- owner 可直接操作，Main 只能在当前实际准入执行中转交该新 owner 来源；其它执行者不能自行合并责任。目标必须 active/paused、assignment active；closed/cancelling/cancelled 明确拒绝，不隐式 reopen/换 owner/复活 terminal Task。
- 在同一事务保留原有消息关联，将精确新 source 加到原 Issue，尊重消息 content/association CAS 与 owner_locked 修正。复用既有新来源 review obligation/fence 和 manager 通知，不假称新来源已经技术复核或任务已执行。
- 原 Issue plan/owner/epoch/tasks/完成/交付/独立验收不变；只推进控制 revision 记录本次 reuse 原因及原创建身份，返回同一个 Issue ID、来源关联 metadata 与下一步。request record、关联/事件/通知同事务，未知重试沿原完整 ID/payload。
- CLI `issue reuse-status` / `issue reuse`、typed owner API 与 Main 工具同批；旧 create 默认行为、合法相似但不同的 Issue、旧 payload hash 不变。无迁移，不扫描标题，不自动折叠历史 Issue，不加 stable key。

## 验证

两个不同 request 接纳两个新的 owner 来源仍只有同一个 Issue；每次原请求回放结果稳定。错误 origin/Issue CAS/epoch/source content/association revision、旧 turn 来源、未准入/withdrawn source、owner locked冲突、closed Issue 拒绝且无部分关联或控制写。真实 DB 证明原 plan/owner/terminal Task 不变、needs_review 与通知正确；CLI/HTTP/Main 权限与完整参数/recovery 语义回归。生产保持停机。

## 独立审查修正

首轮复现两条真实缺陷：分类/事实复查能扩充 `logical_turn_messages`，不能把此可写复查集合当本次原生输入的授权；保留旧工作项显示关联也不能要求它改成 canonical Issue。共享 Main source gate 改为核同一 logical execution 身份下真实 main_turn delivery、该 delivery 已冻结 return source 与已消费 steer，保留实际原 logical 责任的 repair/empty continuation；新分类和 adoption 的复查集合不创造 native 准入。旧已提交请求仍原结果回放，独立历史 decision 答复权限不变。复用的唯一新增 target 仍须 canonical/current；旧 active 关联仅保留原行，不为它新增控制权限或改其来源/锁。

回归将保留首轮反例：先分类历史来源后 reuse 仍拒绝；同一共享 source predicate 核 current/root repair/真实 consumed steer 的正例和 queued/display-only/adopted review 的负例；已有 legacy 与 canonical 链接在加新 target 后原样保留。

第二轮原生来源核验未再发现缺口，但真实咨询升级入口的成功 request operation 是 `conversation.promote`，虽其 revision1 和结果均为新建 canonical Issue。创建入口源码枚举只有 `issues.create` 与该 promote 新建分支；promote 关联既有 Issue 不写新的 revision1。来源验证接受这两个精确 operation，仍逐项核原 principal/domain、Succeeded、同 Issue ID 与 revision1；其它 operation、失败/未知或回执不匹配继续 unavailable，不凭消息关联猜创建成功。新增咨询真实创建→状态→复用→原请求回放及不匹配来源负例。

## 旧澄清委派兼容回归

最终CI枚举到两条原回归。测试直接插 review membership 不能替代真实 steer consumption，改用实际 Claim/ConsumeMainSteer。另一个实际 Await→owner answer→AdoptClarification→更正原失败委派是合法责任继承，不能为修安全缺口删掉，也不能让任意 adopted source 获得新授权。

native 身份 helper 本身仍拒绝 adoption-only 来源。仅已有 delegation 三类操作可以按真实 resumed_by 未完责任链，更正完全相同 source/operation/target/intent 的已确定 invalid_request/stale_revision 失败；原 source 还必须在原失败 execution 中实际 native 准入，原 request 必须 OpFailed。没有冻结原失败、不同意图、未知结果或一般 reuse/share/revoke/reconcile 都不能走此有限更正，不能新ID重发未知动作。原 source 当前仍须 owner、版本相符、未撤回，原请求成功回放不变。
