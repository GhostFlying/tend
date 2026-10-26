# Runner 配置的受控维护（D106）

状态：C1–C3 与 C6 核验接口已实现，独立 review 通过；等待完整检查及 C4–C6 发布验证。

目标：worker 可准备配置候选；外部协调器接手应用，避免 worker 重启承载自己的 runner。首期仅允许 delivery_observer 配置差异，固定 runner 校验模式拒绝身份、存储、transport 和其他配置变化。配置只进入目标主机私有 updater 存储，不进入 release bundle、任务附件、日志或服务端。

1. C1：复用发布批次，新增显式单主机配置计划，绑定原/新配置 SHA-256、所需 provider。配置批次必须保留当前 server 和所有 runner 二进制，跳过 server 维护；只冻结目标主机 claim，等待 run、未知操作及 journal 操作排空。新能力显式协商，旧 server 的普通发布保持原行为。
2. C2：updater 固定配置路径，提供幂等 stage-config。核对原文件和候选身份、私有备份、manifest 与批次；只有排空授权后才停止旧进程并原子替换配置。同 hash 二进制不能跳过配置应用。
3. C3：候选以维护状态启动。runner 报告实际启动时读取配置的哈希；server 核对两次健康心跳、哈希及要求的 provider；updater 提交持久证明后恢复领取。失败沿既有 admission fence 回滚原配置；未知启动/停止继续对账，不猜测重复执行。
4. C4：覆盖重复请求、配置冲突、崩溃窗口、过期授权、配置损坏、缺能力、旧端兼容和回滚，运行 make check 与独立 review，PR 合入。
5. C5：先以普通发布部署支持能力的 server/runner 与固定 updater；当前 provider 任务在维护期间暂停并收敛 run，保留工作区和候选。随后同二进制配置批次只应用目标主机。
6. C6：read_observation 在已有 deployment 授权下增加本 run 主机的心跳、实例、模式、控制修订和能力，不泄漏其他主机配置。成功读回 online/normal/provider/进程及配置身份后，恢复同一任务作独立验证；未成功不得将任务标记完成。维护失败恢复可证明的原配置，保留错误和未知事实。

协议：不改变协议 1 的既有有效请求与默认语义。配置请求仅发送给声明 runner-config-maintenance-v1 的 runner；新增 runtime 字段仅在该 server 显式下发配置目标期间发送，普通升级及与旧 server 通信不发送新字段。配置批次不与二进制更新混用，首次能力部署使用原发布路径。
