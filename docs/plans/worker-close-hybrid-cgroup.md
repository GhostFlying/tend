# 兼容实际 systemd cgroup v1 执行边界

本计划补充 D203 的本机 worker-close 对账，不改变业务协议或执行权限。

1. prepare 优先识别已有 unified v2 unit 边界；混合布局中统一层只给根路径时，
   按实际 name=systemd 的 v1 层级与 mountinfo 绑定原 unit，保存层级种类、真实
   filesystem mount root、目录 device/inode、原 Run/thread/owned hash 和 runner 代次。
   沿用固定 request 与不可变 intent，不从服务名猜内核路径。
2. v2 保持原递归 populated=0 或 kernel_removed 判定。v1 不以 tasks/procs 的
   可变目录快照推断递归为空；仅原执行停止后 kernel 已移除所绑定目录时生成
   kernel_removed 证明。目录仍存在、身份替换、mount/boot/原记录变化保持明确错误。
   v1 允许目录及祖先改名；原路径缺失后须在已绑定的 cgroup filesystem 内有界
   核对原 device/inode 确已不在目录树。发现原 inode、读取错误、超时或超过
   4096 个目录均保留未确认，不能把路径消失误报为内核对象删除。
3. runner 沿现有精确原件绑定消费该历史证明，不伪造原 Close，不重放模型或结果。
   本地证明可选新增字段须保留已有 v2 intent/proof 的读取；无线上协议或数据库变化。
4. 同步 CLI 帮助、运维说明与定向回归：实际混合布局、旧 v2 原件、v1 目录仍存在、
   活跃后代/目录替换/mount 变化、原 kernel 目录移除后的成功和固定请求恢复。

实现和检查覆盖最终候选，经 PR 合入后构建新发布包。已暂存但未安装的旧发布包
与收据保留；操作前重新核对实际执行环境，所有运行配置保持原字节。
