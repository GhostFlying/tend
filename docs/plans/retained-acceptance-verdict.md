# 原验收 verdict 的严格附着恢复

Refs: D66、D159。唯一 writer 负责 Orca worker、相关回归与本计划。

问题：原验收执行通过 bridge 提交 verdict 后，worker 重建没有恢复已确认的 verdict；严格附着禁止发送提醒，因此原 completed/settled root 无法结算。

1. 先用真实 bridge 提交及 worker 重建回归证实原候选失败。
2. 复用 `harness.Verdicts` 的原子持久机制，在原 Run 的私有状态下保存，并以原 Run/worktree/native launch marker/已持久 turn 身份隔离；严格附着仍核对保存的 terminal、pane、session、runtime、launch 和 root turn，不绑定本次附着授权或递增 worker attempt。
3. 保存失败不确认 verdict；缺失、错误绑定、破损记录保持待对账，不隐式发新 prompt、不启动原生执行；保留原严格 send guard 与非必需验收行为。
4. 验证原已确认 verdict 经重建完成，及缺失、错误绑定、损坏和写失败的拒绝路径；运行相关目标 race 检查。由独立交付流程执行完整检查和复核。
