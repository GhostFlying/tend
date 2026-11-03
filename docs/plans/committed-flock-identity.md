# 已提交维护的精确 flock 身份

## 问题与边界

Btrfs 子卷文件的 `stat.st_dev` 可以不同于内核 flock 记录中的 superblock device。
仅用前者查找 `/proc/locks` 会错误拒绝真实持锁的 updater。本修复只改变新维护计划的
锁观测，保留原状态、进程、unit、配置及前滚授权要求，不修改原恢复窗口或业务 run。

## 实施

1. 在指定 updater 的 fd 表中查找 `fstat` 完整 device/inode 匹配的文件描述符。
2. 同一 fd 的 `fdinfo` 必须包含该 PID 的完整文件独占 flock，并通过 mount ID 核对设备。
   普通文件要求设备相同；Btrfs 允许其 mount superblock device 与子卷 stat device 不同。
3. 观测前后重新核验 fd 文件身份和 updater PID/start/hash，缺失或不匹配一律拒绝。
4. 覆盖实际 Btrfs 两种设备号的形状、错误 PID/inode/device/mount 与未持锁负例，
   再用真实本机持锁进程验证。独立 review、完整检查和 CI 通过后交付。

Refs: D155。
