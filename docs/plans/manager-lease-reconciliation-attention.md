# manager 失租后的技术对账责任

Refs: D150、D169

## 问题与边界

manager（包括 receipt-only 对账运行）在已有 lease 到期后变为 unknown，
目前只保存事实并通知只读视图，没有建立交给上级的可处理责任。原 manager
自身被 unknown 安全门挡住时，pending 输入不能代替上级已经接收技术问题。

本变更只补持久责任和通知，不判定原业务失败，不重启、不重新派发、不续旧
lease、不改变原 pin、native RPC、pending 输入、owner pause 或合法条件等待。
不增加 API、CLI、迁移、协议或定时器；使用既有 Attention 与逐级上报能力。

## 实施

1. 在 ExpireLeases 已确认 manager 活动 lease 到期、实际写入 unknown 的同一
   事务内，按原 run/lease 的稳定身份创建至多一个技术 action Attention。
   源事实绑定原 run、lease、task；系统是发现者，不冒充 manager 业务调用。
2. 问题来源为该 manager 的 Issue，接收者为实际父 Issue 负责人；根 Issue
   到 main。沿 D150 的既有责任记录与 background source 通知，不交回失租的
   manager。责任已经交接则先交当前 owner；main 不能解决时可沿原 Attention
   升 owner。本触发不回填历史已 unknown/lapsed 的运行，历史责任另按原身份
   做技术补齐，不宣称部署即恢复。
3. 重复 expiry、App 重建不重复建问题或唤醒。只有真实失租触发，单纯 pause、
   waiting 与没有过期 lease 的 manager 不产生故障。
4. 使用可控时钟覆盖 ordinary manager 与 receipt-only reader、自身 unknown
   保留、原 native pending/pin/输入不变、去重和重启、父/main/owner可达及
   pause/正常等待不误报。先保存 RED，再做 GREEN 目标测试。

## 验证与交付

只运行本地有限目标测试，GOMAXPROCS=2、Go package parallel=1，独立临时
cache；共享 module cache 仅只读复用。不执行 full、CI、模型或生产写入。
目标验证通过后向整合者提供 exact commit、差异、原失败及验证范围；正式
合入前的完整检查、独立 review、CI 与受控部署仍由整合流程完成。
