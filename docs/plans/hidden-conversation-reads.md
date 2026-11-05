# 隐藏会话视图停止专用读取

## 问题与范围

直接打开 Issue 看板仍挂载隐藏的主会话。CSS hidden 不会停止 PendingQueue、Settings、ContextUsage 的 effect；因此会读三个队列分类和 transcript、模型设置，并每五秒轮询 usage/compactions。这是可独立复现的无用读取，不是看板接口等待时间的已证根因。

## 实现

1. 复用 Chat 已有 OverlayVisibility，把队列视图 start/stop、设置读取 start/stop、usage/compactions 的读取与轮询绑定到会话实际可见性。隐藏时中止这些在途视图请求，返回时重新读取。
2. 组件、草稿与缓存状态保留；不卸载整个 AgentWorkspace，不停止非主会话 outbox 恢复，不改变未知操作原身份核对、全局 SSE、通知摘要、snapshot 或收件箱 badge。
3. Settings 的 pending 操作核对与视图读取分开，保持原后台核对。PendingQueue 的 draft 与已持久撤回核对不受视图门影响。
4. 不调整默认六列、不修改服务端投影、超时、业务请求或调度；LiveData 的全局原始 conversation 读取另有职责，当前不拆分。

## 验证

使用真实 Web 客户端与 loopback HTTP 替身，先在原产品源得到失败，再使用相同测试验证：直接 /issues 的隐藏主会话无队列、设置、usage/compactions 请求；打开主会话后启动读取；离开后停止轮询并中止在途读取；返回后缓存和草稿保留且读取恢复。同时确认全局摘要与通知读取继续、已持久 pending 操作仍沿原请求身份核对。测试不调用生产 API 或模型。运行必要现有队列/设置单测、类型与 lint 检查，构建可运行 Web 候选。
