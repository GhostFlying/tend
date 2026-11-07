# Capability 阶段预算修订与最终候选验证

唯一写入者：本 Task 的指定 implementer；原指定 reviewer 独立验收。
本计划先于 spec3 补丁应用。Refs: D183、D155、D180。

## 来源与本轮真实差异

工作树干净，实际接续 HEAD 为
`3a48732fbfdafcd89ca7c963dabdb3a1bca07476`，包含
`2aa0678b14b652e3aff52b0d7e0f4a7f915b5ba2` 的退役声明兼容修复。
只读远端 main 和任务 base 仍为
`bd51f1288aaf1652fe25db138a7701666211d1dc`；历史来源仍为
PR244@`2ae5b26ca715c27365cb4a351d6c098ea6432eb7`。
原[逐项整合矩阵、追加迁移和兼容方案](capability-current-main.md)及
[退役声明兼容修复](capability-retired-declarations.md)继续适用。

| 范围 | 当前候选已具备 | 本轮动作 |
| --- | --- | --- |
| 定义/修订、Agent 显式声明、双 role required、冻结匹配 | 声明式闭环、历史 revision fence 和退役声明兼容 | 保留源码，在最终候选完整检查中验证 |
| 查询/API/CLI/调度 | 共享匹配解释、分页、等待通知及命令覆盖 | 保留，不重复实现 |
| 迁移与旧数据 | server 0098、journal 0019；原 server 1–97 / journal 1–18 未改 | 复核追加及原字节，无新迁移 |
| 维护恢复装配测试 | 真实空库准备与生产 reopen 共用 25 秒；已有诊断及 20ms 注入 | 应用已冻结精确补丁，分开准备与恢复期限 |
| 原检查、审查与历史未知 | 原失败、停止证明和 changes_required 已留存 | 保留，新的通过不改写旧结果 |

## 编码前方案

1. 再下载并核验原六输入；复核原 643 条对话、七版合同及其他完整读取视图的无损重建证据。
   本轮是续办完整性复核，不冒称重新完成全部历史的语义阅读。完整读取三份新增原件；
   外部 receipt/race 只作交接证据，不当作本候选通过。
2. 核对精确补丁 SHA256 为
   `4be428f8b07347ff3f115ce6016854878e8530b622566b596a34987e53dc50dd`，
   先执行 `git apply --check`，然后正常应用；保留全部原诊断标记和 20ms 测试注入。
3. fresh 迁移、全部种子、真实 Close、进程物理 hash 和准备步骤仍在独立 25 秒期限内。
   `setupCtx.Err()` 确认成功后取消准备 context，再创建独立 25 秒场景 context，覆盖
   production `Run` 重开、readiness、递增健康采样、独立 ACK/DB 链接、unknown/pin/准入断言。
   HTTP 仍为 2 秒；两父 helper 均以 55 秒限制子进程，包含 5 秒退出余量。
4. 明确原单一 25 秒总窗口增加为两个至多 25 秒窗口；本修订取代旧诊断计划的不得移动
   25 秒起点限制。Sandbox 十分钟、fake429 九十秒、resolver 五秒保持；生产逻辑、默认值、
   协议、请求去重/CAS 和持久化迁移不变。
5. 对比精确补丁、原语义断言、追加迁移及受约束测试源码，使用现有用户 Git 身份提交。
   对最终提交执行一次有根据的完整 `make check`，保留自动 CPU 预算和原始逐例日志。
   不重复隔离 profile，不建全机队列，不切换 runner 维护；实际新失败交回原责任链。
6. 交付完整 head/base/tree、commit、报告及不可变证据，供原 reviewer 按 spec3 独立复验。
   本实现者不作 formal-review 结论，不覆盖原 changes_required。

## 证据与授权边界

当前范围文件及 spec3 优先于历史部署状态和旧诊断约束。image_gen 仅标签；不增加
inventory/TTL、原生资格、真实生图或未授权 UI/sandbox 内容。无声明 Agent、无 required Task、
旧 snapshot、历史请求、unknown、原 pin、维护隔离和停止证明继续由原兼容路径保留。

自行读取限定范围的 `read_observation` 并保存 observed_at、seq 和 source；分开报告账本、
进程事实、实际源码操作和未验证项。部署仍为旧 base 时明确新功能没有线上验证；该工具不证明
浏览器或产品验收。fake429 不证明 provider 限流。无 merge/deploy、重启生产或联系第三方授权；
后续交付仍需原 Issue 精确版本化批准及单独 delivery_authorization Task。
