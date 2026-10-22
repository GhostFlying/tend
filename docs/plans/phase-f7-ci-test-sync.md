# F7 CI：续接回执丢失测试同步

CI 的 `TestFault_ContinuationReplyLost` 超时栈显示第三次启动已经重放旧 send，但 worker 的新 continuation 可能在测试调用 fake finish 后才执行，把 fake 重置为 working。修复只调整测试，不改生产 worker。

先对原测试重复运行 race 检查；随后以第三次启动前的新 continuation 发送次数为基线，等待实际新增的非 retry continuation 已送达再 finish，并给整项测试设置有界 context，避免故障等待到包级十分钟超时。重复运行 race 测试及 `make check`，独立提交。
