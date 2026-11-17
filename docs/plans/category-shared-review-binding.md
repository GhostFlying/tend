# Category 审阅任务的真实执行身份绑定

## 目标与故障边界

D252 的普通 Agent 工具只接受 category/capability，由调度器选择执行者。共享 PR 审阅的 report 分支却先要求 spec.agent_id，已产生真实独立 Run、甚至已提交不可变报告的 category Task 仍无法 start。等待完成不能解除该门槛；更新运行中 spec 又会替换执行协议。

目标是沿原 Issue、PR、Task、spec、generation 和真实 Run 登记独立报告，不修改 Task 合同、不重新执行审阅、不接受调用者自报身份或 verdict。报告提交、其他检查及原件读取在登记故障期间仍可继续；未选中真实执行者时保留未登记状态，待实际执行 pin 可读后使用同一正式入口。

## 最小实现

1. report 审阅优先使用完成 Task 的当前 artifact.RunID；尚在执行的 category Task 使用同 Task/spec/generation 的实际 implement Run，读取不可变 execution pin 并冻结 reviewer/profile 和执行 generation。运行中报告允许该 generation 内正式等待交接产生的新 Run，报告形成后才冻结精确产出 Run。没有实际选择时明确未就绪。
2. 保持历史显式命名的预绑定路径与 acceptance 导入路径；真实 pin 存在时验证命名约束。保留 report、原件、授权、独立性、执行 profile、精确 head/base/requirements digest、当前 spec/generation 约束。
3. 使用已有正式 CLI/tool 输入，不新增调用者可写身份字段、状态机、迁移或通信协议。同步操作帮助以说明 category Task 的真实执行绑定时机。
4. 回归从正式 category Task 创建、真实领取/启动、提交报告到 start/record；覆盖运行中绑定与已完成报告恢复，验证绑定不改变 Task/spec/generation，覆盖真实外部等待、退出和同 generation 续行报告，并覆盖无实际 Run、身份不独立及旧协议来源不可借用。fixture 仅预置仓库、目录和外部 provider 观察；任务、执行与报告状态由正常用例驱动。

## 验证与交付

先运行故障回归并证明原版失败，再实现最小修复及相关共享审阅回归。按实际 diff 运行 make check，独立 codex review 使用仓库指定模型和推理强度，最终候选与基线所需 CI 通过后受保护 rebase 合入。外部维护 PR 不标受管来源标签。

生产恢复由正式完整 format 2 发布完成，升级前保留原批次/请求与未知事实，协调真实排空，不强停活动 Run。安装成功后由原 owner 对原审阅 Task 的原报告正式 start/record，读回有效结果才证明业务恢复；测试、合入和部署本身不能代替该证明。后续业务修复仍通过其原责任和完整发布收口。
