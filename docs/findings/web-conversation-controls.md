# Web 主会话设置与流式正文验收

日期：2026-10-22。依据 D73、D74 与 `docs/plans/web-conversation-controls.md`。

## 实现与范围

Web 输入区可选择托管主会话的模型及 thinking effort。目录来自真实 harness，显示当前 thread 的有效默认配置；设置按 revision 校验并持久化，跨客户端共享，下一次开始处理的轮次取值。运行中的轮次固定启动配置，worker 配置不受影响。无可调 effort 的模型禁用该选择器；模型声明的 `max` effort 是能力值，不代表本轮排除的 Max mode。

主会话接收真实 agentMessage delta，约每 100ms 保存累计正文；完整 item 和轮次边界立即保存。同一 item 保持 message_id、seq、created_at，正文与状态递增 assistant_revision。SSE 与会话读取均返回已提交的累计正文。停止、失败、进程恢复保留正文；已结束却缺少完整 item 的轮次标记 unknown。最终正文允许比草稿短，迟到旧片段不覆盖新正文或终态。

## 自动化与真实模型

- harness 测试覆盖分页模型目录、目录 ID 与实际模型名/provider、thread 生效默认值、turn override、Unicode 片段与早于 turn/start 回执的事件顺序。
- 设置测试覆盖 revision 并发冲突、旧版本不写事件、回执丢失后的原请求读取、目录不可用、无效模型/effort、磁盘重启保留设置，以及运行中修改只影响下一轮。
- 流式测试覆盖完成前的持久化和事件读取、稳定消息身份、批量写入、较短和空的最终正文、旧片段防回退、停止/失败/重启保留正文，以及正文先于工具调用的顺序。
- Web 共 111 个测试，包括服务端快照与 held SSE、旧分页与实时更新、终态后迟到片段的 revision 合并；最终短正文可正常覆盖草稿。
- 生产构建配合真实 CSP 的 fixture 浏览器验证覆盖桌面与手机尺寸、模型能力显示、多端设置冲突、未知回执核对、目录恢复、流式阅读位置与停止后刷新。此项 API 为替身，不作为模型联通证据。

另使用隔离账本、生产嵌入式服务、真实 traex 及两个独立 Chromium context（桌面与手机尺寸）验证：

1. Web 从真实模型目录选择 GPT-5.6-Sol / low，两端设置同步。
2. 长回复完成前，两端页面实际显示累计正文及生成状态。
3. 生成中在另一端改选 GPT-6.1-Sol / medium；停止并刷新后，部分正文与消息身份、revision 一致。下一条短消息得到真实完整回复。
4. 独立 SQLite 读回确认第一轮仍为 GPT-5.6-Sol / low、settings revision 2；下一轮为 GPT-6.1-Sol / medium、revision 3。前两轮仅有两条 assistant 消息，状态分别 interrupted 和 completed，没有按片段重复插入消息。
5. 实际服务进程重启后，共享设置和 revision 不变，已停止回复的完整累计视图保持不变。
6. 真实长回复生成期间让手机 context 断网，桌面累计版本继续前进；联网后手机 API 与页面追上缺失正文，最后两端得到同一 completed 消息，message_id、seq、created_at 保持不变。

浏览器运行时错误为空。临时服务和浏览器均在 finally 中停止；本轮真实模型联调未使用 orca。此前阶段 F 的 orca 验收产物已保存为补丁，测试工作区及三个终端已删除并核验，见阶段 F 验收记录。

## 独立评审

使用 `codex review --base main`、`gpt-6.1-sol`、xhigh 独立评审，并通过完整 `make check`。评审复现一个 P2：模型目录省略 provider 时，thread 返回的已解析 provider 导致默认模型未映射为目录 ID，前端找不到当前模型而禁用 effort。已修复继承 provider 的匹配，明确 provider 仍分开处理；两种形式同时存在时优先明确匹配，并添加回归测试。

## 部署回退准备

在当前部署账本的一致性副本上，已验证 schema 20 升至 22，候选服务端能打开升级后的账本，再换回兼容回退服务端也能正常读取。三个阶段的 owner 登录、snapshot、Web 首页与 SQLite 完整性检查均通过。回退基于已接受的上一版服务端，仅追加迁移 21、22，不启用新控件或流式实现；不覆盖已接受新写入的账本。

## 证据边界

上述手机尺寸为 Chromium 自动化，不能替代 owner 的 iPhone 浏览器反馈。owner 已验证先前版本的浏览器打开、实时消息同步及停止后继续；新模型控件与流式正文尚无 owner 真机确认。添加为 Web App 与 Max mode 均不在本轮范围。阶段 F 其余尚待确认的设备交互沿用原验收记录，不扩大为已完成。
