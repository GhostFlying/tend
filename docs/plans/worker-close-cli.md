# 本机执行边界关闭对账 CLI

本计划补充 D203 与 owned-close-reconciliation 的 C2，不改变业务协议或执行授权。

1. `worker-close prepare` 在旧 runner 精确存活时读取原 owned 记录，绑定原 Run、
   thread、记录 SHA、runner PID/start/boot 与其真实 cgroup v2 路径及目录身份。
   操作员明确认定该 unit 是原执行边界；命令只保存带固定 request_id 的不可变 intent。
2. 操作员经既有受控维护停止旧 unit；`confirm` 在新 runner 启动前重新核对原记录、
   原进程集合和已绑定 cgroup。两次有界观察须证明递归 populated=0，或原目录确被
   内核移除；目录身份替换、仍有进程、身份冲突和读取错误不产生证明。
3. 确认结果使用独立 `external_execution_boundary` 历史证明，按原 owned SHA 定点
   保存，不写 CloseObserved。runner 只校验精确绑定后沿既有结果回收接点消费。
4. 固定 request 的 show/相同输入重读支持中断恢复；不换 ID、不覆盖既有事实。
   CLI 同步登记能力、完整参数和 JSON 输出、帮助与必要 Linux/跨平台编译回归。

命令不停止进程、不发业务消息、不写业务数据库，不重放模型或结果。机器路径与
实际运维记录仅保存在忽略目录。
