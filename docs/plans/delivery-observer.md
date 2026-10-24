# 交付状态的独立观察器

Refs: W4。状态：适配器、协议、独立观察表、poll/report、owner 刷新、runner 循环和配置已实现；定向 race 与 vet 验证通过。

观察器独立于 worker harness、workspace 和执行 slot。首期仅通过 gh 的只读 GraphQL 查询及 bytedcli 的 MR get JSON 查询，返回开放、合入、关闭状态、commit、外部时间和可用的操作者身份。显式指定仓库和编号，不根据当前目录或分支猜测。GitHub 关闭人来自最后一次 ClosedEvent；Codebase 关闭人允许缺失，不用创建人或更新人替代。

实现只使用标准库及既有进程取消辅助，命令接受 context、限时、测试注入 Exec。未知状态、认证/网络/解析失败不推断关闭，不覆盖最后已知事实，不调用模型或外部写操作。错误输出不包含 CLI 的原始 stderr、凭据或整个响应。

新增可选 provider capability 后才派发观察任务；协议使用 runner.deliveries_poll/report，身份为 work_item_id、delivery_id 和目标修订。提交观察经请求去重，并防止迟到查询覆盖新目标或新观察。开放默认五分钟、暂缓一小时、终态停止周期刷新。独立观察表使用服务端递增 generation、nonce、host 和当前进程实例绑定查询；结果迟到不能覆写新查询。手动刷新校验规划修订但不改变它；失败保留最后成功事实及时间。观察读操作不进入执行 journal 的 pending 队列，不妨碍维护排空。旧 server 拒绝可选操作时终止本进程的观察循环，兼容旧 runner，不改变执行 assignment。

回归覆盖两平台开放/合入/关闭、关闭人缺失、再次关闭取最近事件、字段缺失、异常 JSON、未知状态、命令失败及 context 取消。所有单元测试使用 fake CLI，不联网。
