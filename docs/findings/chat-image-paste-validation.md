# 聊天图片粘贴验证

> 下述为原实现候选的历史验证记录，不代表当前整合 head 已通过或已部署。当前候选的检查、独立审查与交付状态记录在对应 PR。


日期：2026-11-02。对应计划 `docs/plans/chat-image-paste.md`，设计 D86、D87、D142。本记录描述本地候选实现，不代表生产发布或独立 formal-review 结论。

## 实现边界

首页 Messaging 与主/Issue 共用 Composer 使用同一个同步剪贴板提取器。可用 file items 为主视图，无可用项时回退 files，不把两个视图相加，不按名称跨事件去重。无法读取的项提示重新复制或手动添加。原件不转码，不引入应用层大小或数量限制。

有附件时拦截原生 paste，将纯文本按 textarea 当前选区插入并恢复光标；无可用附件时保留原生文本粘贴。HTML 不渲染、不抓取远程图片。未加载、无权限、disabled/saving 时不写附件。首页复用现有 LocalAttachment 预览及 object URL 清理。仅用户显式发送后沿原 messaging.send / conversation.submit_message 和上传队列发送，不新增业务 API，CLI 继续使用既有原件上传与消息发送能力。

## 桌面实际交互

环境：Linux X11/Xvfb，Chromium **140.0.7339.186**，Node **24.19.0**，pnpm **10.34.7**，Go **1.27.1**。浏览器由 Playwright 以有窗口模式启动。以 xclip 向 X 系统 clipboard 提供 PNG，浏览器真实 Control+V 触发受信 paste；未使用合成事件替代此步骤。此为可复现的虚拟桌面系统剪贴板环境，未测物理桌面截图工具、Safari、Firefox。

隔离 tend-sandbox 提供真实 API、鉴权、上传、持久队列、账本与内容存储。Agent 回复由进程内脚本产生，没有模型或真实用户收件人。三个入口逐一验证：

- 首页 `/`：待发缩略图、移除、重复粘贴、显式 messaging.send、服务端消息附件引用及原件下载。
- 主聊天 `/diagnostics/main`：同上，经 conversation.submit_message。
- Issue 详情：同上，核对 payload 与读取记录均为对应 Issue conversation_id。

每处粘贴时均断言没有上传或业务发送；重复粘贴增加两个附件，移除一个后仅上传一个。原生中文文本粘贴替换选区并保留前后草稿。下载 PNG 与源 clipboard PNG 的 SHA-256 均为 `23cd769873a497c1398eab62e62d7e64f12039fa5d68bb21696ef0f723c3d198`，大小 5353 bytes。发送回执为 accepted，随后实际读取已保存消息和正确附件引用，不把 accepted 当模型处理完成。

交付附件含各入口的 preview、composer、removed、sent 截图及 `desktop-results.json` 的请求、回执、file_id 和原件 hash。

## 定向回归及模拟边界

剪贴板单测覆盖 items/files 双视图、null/异常及 fallback、部分不可读项反馈、多文件、同名异内容和多次粘贴、PNG/JPEG/GIF/WebP、其他文件、空名、超过旧 inline 预算、混合文本选区、纯图光标、disabled/saving、提取及草稿失败。

真实浏览器另用**合成 DataTransfer/ClipboardEvent** 验证三个编辑器的混合文字选区及光标、双视图只添加一次；合成 isComposing Enter 验证中文输入确认不发送。此项不是实际系统中文输入法验证。手动文件选择保持可用，图片移除和发送后断言 blob 预览 URL 已 revoke。

PNG/JPEG/WebP 与两帧 GIF 以合成 file event 加入首页附件，随后调用真实上传、发送、读取及下载；四种原件逐字节 hash 一致，GIF 未截为首帧。没有宣称四种格式均经系统 clipboard 实测，真实系统 clipboard 部分为 PNG。

上传失败仅在浏览器路由注入一次 HTTP 422 错误，验证可见失败提示、“恢复为新消息”保留文字及原 Blob 缩略图。撤销注入后显式重发，实际服务端消息及原件 hash 核对成功。失败注入不是服务端真实配额故障。

## 移动/PWA 范围

按 bytedcli Dora 工作流查询明确 Android emulator 的 fresh idle+online 设备，创建本次独立 lease，get 的 session_id 与本地记录匹配，并在 occupied list 核对。每次 ADB 操作前重新 get 核验；finally 释放且 occupied list 复查已消失。没有接管其他会话租约。

第一轮设备为 aries_12 模拟器，Android 12、API 31、ARM64，未装 Chrome，仅有 WebView 91.0.4472.114，无法打开测试页。释放首轮租约后，重新查询并为另一台空闲在线模拟器创建新 lease；未复用旧 lease。

第二轮从 Google 官方 Chromium Android ARM64 snapshots 获取 revision 1732661 的 ChromePublic.apk，验证归档 CRC 及 ARM64 ABI 后安装。实际浏览器 **159.0.8139.0**，设备回读 Android **12/API 31**、ABI **arm64-v8a,armeabi-v7a,armeabi**。ADB reverse 虽成功但设备得到空响应；改用仅连接隔离沙盒的私网 TCP/TLS 代理。HTTP 不具备应用既有 crypto.randomUUID 所需安全上下文，因此使用临时 HTTPS 测试证书，并通过 CDP 对本次测试浏览器忽略证书错误；代理转发真实 HTTP 字节，没有 mock 业务或上传响应。

来源应用为该设备上的 Chromium：打开测试 PNG，以原生长按图片菜单“复制图片”，随后 Android KEYCODE_PASTE 在聚焦输入框触发实际系统 paste。事件记录 isTrusted=true，包含 image/png File。首页、主聊天、Issue 均验证预览、移除、再次粘贴、显式发送、正确会话接收及下载原件与实际 clipboard File hash 一致；粘贴本身不发送。截图与 android-results.json 附件记录每个入口。

**移动验证限制**：浏览器自动化点击遇到已有布局遮挡（timeline 截获 pointer），所以本次通过程序聚焦输入框、DOM 激活移除/发送按钮，真实系统 clipboard 读取及 paste 未合成。此证据证明该模拟器浏览器上的图片粘贴集成与原件链路，不证明触屏点击/布局可用，不替代此前移动/PWA 其他 Issue 的修复。未验证 Android 安装 PWA：测试证书非受信任发布证书，原生独立窗口安装/启动未覆盖。iOS Safari、iOS 主屏幕模式、物理真机、系统中文输入法及其他来源应用未测。浏览器未暴露 File 的来源仍需手动添加附件。

## 复现

1. 安装 lockfile 依赖并构建 Web，启动本次独立 tend-sandbox、空数据目录与未占用回环端口。数据目录需足够短，保证 Unix control socket 路径不超过平台限制。不要使用生产实例、他人数据或未验证的目录。
2. 启动 Xvfb 并设置 DISPLAY；确保 xclip 与 Playwright Chromium 可用。
3. 在仓库根运行：

   ```sh
   TEND_PASTE_ORIGIN="$SANDBOX_ORIGIN" TEND_PASTE_OUTPUT="$EVIDENCE_DIR" \
     node web/scripts/chat-image-paste.ts
   ```

脚本退出时关闭浏览器和自己的 clipboard 进程；sandbox 与 Xvfb 由启动者关闭。输出证据仅写指定目录。脚本包含明确标注的合成回归及一次 HTTP 错误注入，其他发送与原件核对均为隔离服务真实链路。

已执行 `APP_TEST_SHARDS=4 make check` 并完整通过，包含所有 Go race 包、app 的 830 个根测试分片、Web 67 个文件/409 项测试以及类型、lint、格式、样式和构建检查；最终日志随实现报告交付。独立执行的桌面真实剪贴板脚本亦通过。正式验收由 Tend 独立 formal-review 根据代码及证据判断。
