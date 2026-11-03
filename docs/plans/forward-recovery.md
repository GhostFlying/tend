# 固定 updater 前进恢复实施计划

依据：已独立验收的 recovery-design 第 3 节；设计验收不是实现验收。实现以任务冻结基线为起点，不读取其他实现工作区。

## 安全边界

只实现当前 systemd 用户服务 adapter。生产安装、信号、配置、HTTP 与凭据访问均不在本次执行范围。测试使用私有临时目录和隔离服务；未知 manager、身份、锁、授权、资源交接或持久化结果必须拒绝继续。原 operation、terminal、失败历史、旧 proof 完整保留；新 binding 的成功不能解释为原 binding 成功。

## 实施顺序

1. R1：追加 format 2 / generation、recovery 快照与幂等 CAS，受控 adopt；恢复优先于普通 Step，禁止旧 checker/monitor 争写。先落停止/启动意图，不确定结果仅对账。
2. R2：实现独立 installer 的 systemd-user plan/apply/status 第一跳；独立锁、固定 unit 与完整策略摘要、原 updater/child 身份、同 state/lock inode、原子安装 journal。停止 updater 保 child；不依赖旧 updater RPC，不回装不兼容旧 binary。
3. R3：新授权与精确前进状态机；保留原 grant，禁止 rollback，单次 stop/start。新 server 启动 admission fence、app 恢复登记、真实 readiness、单调两样本；atomic Current/proof/recovery receipt。
4. R4：app 单事务幂等关联原升级、事件/请求/outbox；receipt 先持久再释放 admission fence，最后解除 updater 原未完成占位，旧历史可独立查询。
5. R5：CLI 实际 plan/apply/status、帮助和 D142 目录；记录实际安装步骤、guard、失败边界及生产启用前置。追加设计决策；协议不兼容变更按仓库规则处理。
6. R6：定向回归覆盖旧 terminal/checking、真实旧 health 条件、安装保活与单锁、CAS/重放、停止启动不确定、真实 readiness、持久化及 DB 失败。取得重型窗口的真实排期回执后运行 make check；未完整 exit 0 不 commit。随后按规定独立 Codex review、处理意见、创建真实 PR、核对最终 head/base/CI；正式验收由独立责任完成。

## 验证与交付

输入逐份通过下载工具校验大小与 SHA-256，保留原字节于任务 scratch。轻量定向检查与完整检查分开记录。重型窗口未释放前只编码与定向检查，不启动第二 full。交付报告、可重用 bundle、commit/PR 身份、检查证据和未满足条件；生产始终暂停。

## 首轮独立工程 review 后的修正

- R1：兼容性以不可逆运行 candidate 的已建立 protocol/storage 为准，不能用旧 Current；安装也使用同一核验路径。
- R2：启动 fence 同时约束以 system principal 调用的本机 owner upgrade/archive/host maintenance，保留恢复登记、关联和观测。
- R3：installer 中断后提供独立稳定 request/CAS 的显式续接授权；相同原请求/续接请求仅对账，fresh manager/child/state/lock guards 后才可继续固定 updater，不重复 server start。

每项补有意义回归，修正后重跑隔离 systemd（含启动前中断）并重新独立 review；不占用第二完整窗口。
