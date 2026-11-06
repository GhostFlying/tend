# 有界维护的真实 systemd 组件验收

Refs: D155、D156、D180。

使用显式 opt-in 的 Linux user-systemd fixture 验证更新器的真实进程、单锁与外部接管边界。
测试只创建自己唯一命名的单元、临时目录和空数据库，不读取生产配置、凭据、数据库或
journal。普通 `make check` 不启用这些单元；原 fake-process 用例继续验证领域分支，
本验收补真实系统边界。

## 范围

1. updater 的控制 socket 或内部业务 mutex 不响应时，独立 installer 经持久计划与
   实际 manager/进程/锁身份完成外部接管，不依赖同一个故障 RPC。
2. 停止或升级信号只作用于已核 PID/start/launch/hash 的 updater；原 child 身份保持，
   无关 sentinel 不受影响。接管后只有一个真实 flock owner，竞争 writer 必须被拒绝。
3. successor 已实际启动而观察或回复丢失时，恢复只查原 launch 并收养唯一精确进程；
   不重复 Start，不把原 unknown 回执改写为首次成功。

server 与 runner 的通用外部接管入口各运行一个真实 mutex 挂死场景。测试专用 updater
helper 仅在显式环境变量与精确 `serve --config` 子进程参数同时满足时启用，持有真实
Supervisor mutex 并忽略 SIGTERM；正常包测试仍直接执行原测试入口。它不进入产品
二进制，不增加产品 fault flag。SIGSTOP 的实际产品 updater 场景另保原结果，不冒充
仅 mutex 持锁的证明。

每项只运行一个有限反例，不复制所有 mock 分支。server App 的原 Run/lease/request 保护、
原 unknown 保留及 runner issuer 分别由相应真实组件验收覆盖。

## 实施

- 新增独立 `internal/updater/bounded_systemd_linux_test.go`，不改已有 core 测试或业务
  实现。与 installer 实现者固定包内入口、实际 CLI 和最小故障注入点。
- 沿用 `TEND_TEST_SYSTEMD_USER=1` opt-in 与唯一 transient user-unit 约定；产物必须是
  显式绝对路径，记录实际可执行字节。先复用既有 recovery fixture helper；必要的挂死或
  丢观察故障使用测试专用子进程入口，不向产品 CLI 增加测试默认行为。
- 使用真实 `/proc` 身份、pidfd 停止结果、完整 lock device/inode 与 fdinfo/FLOCK，
  以及独立原生启动事件，而非 fake-process 计数代替系统证明。
- 原日志、stdout/stderr、终态、unit/进程/锁身份在清理前保存为结构化证据。清理只处理
  本测试记录的单元与精确进程，验证退出后再删除本测试创建的临时文件；不使用通配符。

## 验证

先完成编译及普通定向用例，再在独立可用 user manager 中串行运行冻结 test 名的一次
opt-in 验收。记录 source/tree、测试与产物 hash、命令、退出码、原逐阶段输出和真实
清理结果。源码或实际入口变化后评估受影响验证，不以 skip、API 接受或 fixture 进程
存活替代验收通过。此验证不声称生产部署或历史业务 unknown 已收口。
