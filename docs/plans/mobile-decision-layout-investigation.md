# 移动端决定卡片布局调查与修复计划

## 结论

本轮只调查、复现和制定计划，没有修改产品组件或样式。

1. 原图中两个长选项的正文都换成两行，但按钮边框仍保持单行固定高度；第二行越过各自下边框，压缩了选项间距并侵入后续内容的视觉空间。部署 commit 对应源码和真实组件 fixture 都确认根因是 `.opt` 的固定高度与允许换行的内容不相容，不是单独的 `line-height`、safe area 或截图缩放问题。
2. 英文标识和长不可断词是另一条风险：当前按钮没有最大宽度和断词约束，fixture 中它们把 390 px 页面撑到 845 px，而不是留在卡片内。
3. “跳到最新”浮层本来就应覆盖 transcript 的局部内容；仅看到重叠不能直接判为缺陷。但它不应离开 transcript 去遮发布横幅。fixture 确认了与原图形态一致的状态链：用户先离开最新位置，随后决定卡片出现，底部 dock 加上安全区使 transcript 收缩到 0 px；绝对定位浮层仍按 `bottom: 8px` 向上溢出，完整盖进发布横幅。此时点击浮层也不会消失，因为滚动处理在 `clientHeight === 0` 时提前返回。
4. 320 px 和 150% 字号还暴露了同一布局预算问题：决定卡片和 composer 会被固定底部导航覆盖或推出视口。修复不能只给发布横幅加 `z-index`，也不能只隐藏浮层；应先让决定选项内容驱动尺寸，再约束 dock 的可用高度并给跳转控件稳定的布局空间。

## 本轮实施计划

本轮在上述已验收调查基础上实施，不改变 D71、D89 的决定提交协议。开始修改前已确认工作树只有本报告和调查 fixture 两份前置文件，SHA-256 分别为 `07229882105d79d8e7f3da8a1e2b9f75ed8249cf70a30e0b21db47c5e028aa19`、`169bb718424d11d2256014abfed1683ba06c7844cd4b49a3ff7a5fe51446e78e`；分支随后无 stash、无切换地 rebase 到当时最新 `origin/main`，以保留并行加入的后台 Agent 会话能力。

1. **M1，决定正文自适应**：给真实决定选项单独增加布局 class，把固定高度改为内容驱动的 `min-height`，补齐纵向 padding、`min-width: 0`、`max-width: 100%` 和长词断行；移动端主要选项最小触摸高度为 44 CSS px。提交按钮和 `HandbackCard` 继续复用短操作按钮样式，避免长正文规则扩散。
2. **M2，有界底部区域**：让聊天 seat 在列布局的剩余高度内可收缩，只让决定和待发送队列所在内层纵向滚动且各子项不因 flex 收缩丢失控件；失败通知和 composer 留在裁剪边界外，保留通知操作、textarea、提交控件及向上展开弹层的键盘和点击可达性。极低移动视口限制 composer textarea 自增长，仅在决定或队列确有内容时让 Transcript 临时释放空间并为决定区保留至少一个主要触摸目标的滚动预算；空 dock 和常规视口仍让 Transcript 使用剩余空间，固定 TabBar 的预算由现有 shell safe-area 规则承担。
3. **M3，跳转状态恢复**：显式点击浮层或 header 入口时立即收敛浮层状态并滚到末尾；用 `ResizeObserver` 在决定、发布横幅、字号和视口改变后重新计算 near-bottom。零高度或暂时不可见时不让浮层越出 Transcript，恢复尺寸后按用户此前是否跟随最新决定滚动位置；用户主动展开既有工具输出时保留被点击控件的阅读位置，不把内容自身增长误判为新消息并强制跳到底部。
4. **M4，真实应用回归**：把只读调查脚本收敛成隔离 owner API 的可写回归，覆盖 320/390/430/desktop、100/150/200% 字号、长中文/中英/不可断词、安全区、390×568 及键盘收缩后的 390×400；断言正文 padding、44 px 触摸目标、无横向溢出、决定/队列/composer 控件滚动与键盘可达、composer 设置与上下文弹层不被裁剪、跳转不遮挡且两入口可恢复。fixture 只接受预期的 `decisions.respond`，验证延迟成功、失败、防重复提交和 payload；失败后仅在用户未转移焦点时恢复重试按钮，同时检查 `HandbackCard`、effect 标签及短按钮。
5. **交付门槛**：运行 Web 类型/格式/单测/构建、专项 Chromium 回归、可用时的 WebKit 回归、仓库 `make check` 和独立 `codex review`；保存关键几何与截图到任务 scratch。提交仅包含两份明确承接文件和必要实现/回归/设计记录，推送当前分支并创建 PR，不合并、不部署。

## 调查身份与边界

### 原始截图

- 文件：`IMG_1358.png`
- run 下载工具校验：1,497,873 bytes
- 独立重算 SHA-256：`28e20951b1441791998a37b0f5dafdae1b3c145325c9982a59fc22ae77ad8618`
- PNG：1206 × 2622，16-bit RGB，非隔行
- 原图保持原字节不变；fixture 截图是派生证据。

原图只能证明截图当时的可见结果。它像 iPhone 截图，但不能据此确定机型、CSS viewport、DPR、Safari 版本、显示模式或软键盘状态。

### 部署与源码

开始调查时的只读观察：`observed_at=2026-10-27T01:58:37+08:00`、`seq=5120`、source 为 server-authorized worker observation。账本事实是本任务处于 running；实际进程事实是 server 和 runner 都报告 commit `170c639ef0412cc96ffc2c362ba8a7d6159ecb07`，server `proof_status=confirmed`、`modified=false`。观察不提供浏览器渲染或 iOS 交互证据。

工作树 HEAD 就是上述 commit。对九个相关产品源码文件逐一比较了工作树 blob 与该 commit blob，哈希完全一致；因此下列根因定位针对实际部署 commit 的源码，而不是其他分支或较新的 `origin/main`。本轮新增的只有本调查计划和 fixture。

### 本地 fixture

`web/scripts/decision-layout-investigation.ts` 构建并提供真实 Web 应用，使用真实 `Chat`、`UpgradeBanner`、`Transcript`、`DecisionDock`、`DecisionCard`、`Composer` 和 `TabBar`。它只提供隔离的本地 owner API，拒绝所有产品写入，不连接部署服务。

fixture 使用 Chromium 140.0.7339.186 和临时 Noto CJK 字体；设备像素比为 1。安全区案例通过覆盖相同布局目标模拟 top 47 px、bottom 21 px 的边界，不冒充 Safari 的 `env()` 实现。原图决定文案只用于排版复现，没有凭据或生产 API 数据。

## 原图可见事实与影响

### 决定选项

- 两个选择都是多行正文；各自的圆角边框只包住约一行高度，第二行越过下边框。
- 第一项越界文字占用了本应属于两个按钮的间距；第二项越界文字靠近“补充说明或其他回答”。即使点击区域仍可能是按钮盒，视觉上无法判断第二行是否属于可点击范围。
- 风险包括：选项边界不清、误触、焦点环只包住部分正文、disabled/loading 状态看似只覆盖第一行，以及辅助技术与视觉点击区域不一致。

### 跳到最新

- 原图中“跳到最新”位于发布卡片的下半部，遮住了发布卡片内部空间。
- 浮层本身是“用户离开最新位置”的合理提示；它覆盖 transcript 内容是预期设计。发布卡片是 transcript 的前一个普通流兄弟，因此覆盖发布卡片不是预期的目标区域。
- 原图本身不能证明用户当时是否点击过、软键盘是否打开、safe area 值或浮层是否可恢复；这些仍需实际 iOS/Safari 验证。

## 源码证据

### 选项按钮

- `web/src/decisions/DecisionCard.tsx:96-109`：每个 option 直接作为按钮正文，没有单行限制；`effect` 标签也可能追加在同一按钮内。
- `web/src/decisions/DecisionCard.module.css:62-74`：`.opts` 可换行且间距为 8 px；`.opt` 强制 `height: 32px`，只有水平 padding，没有多行所需的纵向 padding、最大宽度或断词策略。
- `web/src/decisions/DecisionCard.module.css:96-108`：移动端只把固定高度改成 `36px`，仍不是内容驱动高度。
- `web/src/decisions/DecisionCard.tsx:112-138`：`.opt` 还被“发送给 worker”提交按钮复用；`HandbackCard` 也复用 `.opt`。修改共享选择器前必须验证这些短操作按钮。

### 浮层与高度预算

- `web/src/shell/Chat.tsx:49-59`：DOM 顺序为发布横幅、Transcript、底部 `.seat`；决定 dock 和 composer 都在 `.seat` 内。
- `web/src/shell/Chat.module.css:30-43`：`.seat { flex: none; }`，长决定卡片不会收缩或获得自己的滚动预算，而是挤压前面的 Transcript。
- `web/src/conversation/Transcript.module.css:1-13`：`.wrap { flex: 1; min-height: 0; position: relative; }`，允许收缩到 0 px。
- `web/src/conversation/Transcript.module.css:66-83`：`.jump` 是相对 `.wrap` 的绝对定位控件，`bottom: 8px; height: 30px; z-index: 5`，没有确保父容器至少 38 px 高，也没有溢出边界处理。
- `web/src/conversation/Transcript.tsx:98-105`：scroller 高度为 0 时 `onScroll` 直接返回，不能更新 `atBottom`。
- `web/src/conversation/Transcript.tsx:146-163`：显式跳转只调用 `scrollTo` 和更新 refs，没有同步修正 `atBottom`；0 高 scroller 上不会产生可用于恢复状态的有效滚动。
- `web/src/conversation/Transcript.tsx:165-221`：只要 `!atBottom` 就继续渲染浮层，未检查可用高度。
- `web/src/shell/Shell.module.css:65-71` 与 `web/src/shell/TabBar.module.css:5-16`：移动端为固定底部导航预留 safe-area 高度，但 `.seat` 自身没有在这份可用高度内建立滚动边界。
- `web/src/upgrades/Upgrade.module.css:1-18`：发布横幅是普通流、`flex: none` 的按钮，不是导致浮层定位的控制层。给它增大 padding 或 z-index 只能遮掩症状。

## DOM/CSS 尺寸复现

详细原始数据见 `geometry.json`。以下均为真实组件的 CSS 像素。

| 场景                                                          | 关键结果                                                                                                                            |
| ------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- |
| 402 × 874、原图中文选项                                       | `.opt` 边框高 36；两行文字范围高 41，文字下沿比边框低 7 px；按钮盒间距 8 px，但第一项文字到第二项边框只剩 1 px。                    |
| 402 × 874、top 47 / bottom 21 safe area，先滚离最新再打开决定 | 发布横幅 y=107..181；Transcript y=189..189，高 0；浮层 y=151..181，完整落在发布横幅的垂直范围。点击后仍显示。                       |
| 390 × 844、两个长英文不可断词                                 | 按钮本身维持单行，但文档宽度为 845，超过 390 px 视口 455 px。                                                                       |
| 390 × 844、150% 字号                                          | 选项文字三行、范围高 73，按钮仍高 36；卡片下沿 787，composer y=860..912，已在视口外；底部导航 y=788..844。                          |
| 320 × 720                                                     | 第二项三行，文字下沿比按钮低 29 px；卡片下沿 733，已进入导航 y=664..720；composer y=806..858，完全在视口外。                        |
| 430 × 932                                                     | 两个中文选项仍可出现两行溢出；Transcript 高 65 时浮层保持在 Transcript 内，不与发布横幅相交，说明“所有重叠都必然错误”的判断不成立。 |

字体会改变精确换行点，但不会改变固定高度与多行内容不相容这一结论。没有 CJK 字体时同一 fixture 的换行数量较少；交付截图和最终 `geometry.json` 使用 Noto CJK。

## 可复现根因与未确认项

### 已确认

1. `.opt` 固定高度造成中文多行正文越界。
2. 缺少 `max-width/min-width/overflow-wrap` 约束造成长不可断词扩展页面宽度。
3. `.seat` 不收缩、`.wrap` 可到 0 高、`.jump` 绝对定位的组合可让浮层跑出 Transcript。
4. 0 高 scroller 使浮层点击无法通过现有 scroll handler 清除。
5. 较窄视口或字体放大时，决定 dock 和 composer 可以进入固定 TabBar 覆盖区域。

### 尚未确认

- 原图生产浏览器是否经历了与 fixture 完全相同的状态顺序；本轮没有生产 browser 会话。
- iOS Safari/PWA 对 `100dvh`、`env(safe-area-inset-*)`、动态工具栏、软键盘和字体放大的实际组合。
- 原图对应的具体 CSS viewport、DPR、系统字体和辅助字体设置。
- VoiceOver、真实触摸命中和软键盘打开后的焦点滚动。

## 推荐修复顺序

### M1：先修决定按钮的内容尺寸

修改 `DecisionCard.module.css`，必要时在 `DecisionCard.tsx` 给选择项增加专用 class，避免把内容排版规则无意扩散到所有操作按钮。

- 把固定 `height` 改成 `min-height`，高度由正文决定；为多行正文增加明确的 block padding 和 line-height。
- 在选项按钮上设置 `min-width: 0; max-width: 100%; white-space: normal; overflow-wrap: anywhere; text-align: center`。
- 移动端触摸目标建议至少 44 CSS px；短选项仍保持当前 pill 外观，长选项自然增高。
- 保留 `.opts` 的明确间距，并用文字范围而不仅是按钮盒验证间距；不能用 ellipsis、line-clamp、固定两行或 `overflow: hidden` 裁掉决定差异。
- 单独核对 `.reply .opt`、`HandbackCard .opt` 和带 `.eff` 的 tend 选项。如果共享 `.opt` 改为自适应高度，短提交按钮的视觉高度和对齐也必须保持稳定。

### M2：给 dock、composer 和 TabBar 明确分配高度

修改 `Chat.module.css` 和 `DecisionDock.module.css`，必要时小幅调整 `Chat.tsx` 的容器结构。

- `.seat` 应参与剩余高度约束，不能让长卡片把整列内容推出视口。
- 保持 composer 和其发送/附件控件可到达；让决定 dock 在剩余空间内滚动，或让 seat 成为有界滚动区。优先避免整个页面与 transcript 的双重无界滚动。
- 滚动容器需要 `min-height: 0`、明确的 overflow 和 `overscroll-behavior`；焦点进入屏外 option、textarea 或 submit 时必须自动滚到可见处，焦点环不能被裁切。
- 预留 top/bottom safe area、固定 TabBar 和 composer 的真实尺寸；不要用只适配某台 iPhone 的总像素常量。

### M3：让跳到最新控件有稳定边界和可恢复状态

修改 `Transcript.tsx` 与 `Transcript.module.css`。

- 仅在控件真正位于 Transcript 可用矩形内时采用当前 absolute overlay；为它预留至少 `height + bottom gap` 的空间，空间应来自 M2 的有界 dock，而不是继续把页面向下撑出视口。
- 如果可用高度不足，使用正常流中的专用跳转槽位，或明确回退到 header 中现有的“跳到最新”按钮；不要把浮层藏在发布横幅后面，也不要用更高 z-index 争抢遮挡关系。
- 在 ResizeObserver/布局变化后重新计算 near-bottom，覆盖决定打开/关闭、发布横幅出现/消失、旋转、字号变化和键盘 resize。
- 显式点击跳转时同步收敛可见状态，并在滚动完成后以 scroller 的真实几何复核；0 高时不能等待永远不会发生的 scroll event。
- 保留 header 按钮现有 `aria-label="跳到最新"`，为浮层也提供稳定 accessible name；两处都要能用键盘触发并有可见焦点。

### M4：补局部自动化，再做实机验证

把调查 fixture 收敛为通用、非生产数据的 Playwright 回归，推荐扩展 `web/scripts/mobile-layout.ts` 或增加同类脚本并进入 `test:layout`。先完成 M1 的纯尺寸断言，再完成 M2/M3 的状态顺序断言，避免一次改动难以归因。

实现完成后再用 iOS Safari 或 PWA 做一次真实验证；软键盘、动态工具栏和 VoiceOver 不能由本地 Chromium 结论替代。

## 回归矩阵

### 尺寸和正文

- 视口：320 × 720、390 × 844、430 × 932；桌面至少 1440 × 900，确认共享样式无回归。
- 文案：短单行、两行中文、三行中文、中英混排、带 `executor_unsupported` 的标识、超长不可断词、带 `.eff` 标签的 tend 选项。
- 字体：100%、150%、200%；若平台支持动态字体，再测系统大字体。
- 每个 option 的文字 Range 矩形必须完整包含在按钮 padding box 内；相邻 option 的文字矩形不相交，预期 gap 不被文字侵占。
- `documentElement.scrollWidth <= innerWidth`，卡片、问题、option、textarea 均无横向裁切。

### 状态与交互

- idle、键盘 focus、鼠标/触摸 hover 或 active、disabled、提交中、成功 pending、失败后恢复。
- 本地延迟响应时，所有选项保持 disabled、防止双发；“发送给 worker”显示提交状态，textarea 内容和焦点行为不丢失。
- 成功后 options 被 pending 文案替换；刷新后状态与服务端 fixture 一致。测试只检查本地请求次数和 payload，不访问生产。
- Tab 顺序覆盖详情、所有选项、展开调查依据、textarea、提交按钮、composer 与底部导航；焦点环不被 overflow 容器裁掉。

### 浮层与垂直布局

- 初始在最新位置：不显示 transcript 浮层。
- 离开最新位置：浮层显示，矩形必须被 Transcript 或专用跳转槽完整包含，并且不与发布横幅、决定卡片、composer、TabBar 相交。
- 点击浮层和 header 箭头：都滚到最新并清除浮层；重复点击幂等。
- 状态顺序：先滚离最新，再依次打开/关闭决定、出现/消失发布横幅、改变文案高度、缩放字体、旋转视口。
- 安全区：模拟 top 0/47、bottom 0/21；同时覆盖有发布横幅、无发布横幅。
- 低高度与键盘边界：390 × 568、本地 resize 模拟 visual viewport 变小；决定卡片、回复 textarea、提交按钮和 composer 必须仍可滚动到达，不能被 TabBar 永久覆盖。

## 独立验收条件

1. 从实现 commit 重新构建真实应用，以隔离 fixture 运行；不能只测静态 HTML 仿品。
2. 在 320/390/430 px 和 150% 字号下，所有选择正文完整可读；边框包住文字与 padding，选项之间无文字或边框重叠，页面无横向溢出。
3. 不存在 ellipsis、line-clamp、固定行数或隐藏 overflow 对决定正文的裁切。
4. 长决定出现后，card、textarea、submit、composer 都能通过滚动和键盘到达；与 TabBar/safe area 不发生不可恢复遮挡。
5. 对“先滚离最新、再打开决定”的顺序，跳转控件不盖发布横幅；点击浮层和 header 按钮都能恢复到最新且浮层消失。
6. disabled/loading/pending/failure 状态仍保持原有业务请求语义，fixture 只发生预期的一次提交；焦点和 accessible name 保持可用。
7. 桌面短按钮、HandbackCard、tend effect 标签和 worker 提交按钮无共享样式回归。
8. 单独记录 iOS Safari/PWA、软键盘、动态工具栏、系统字体放大与 VoiceOver 结果；未执行前不得写成已验证。

## 交付文件

- `mobile-decision-layout-investigation.md`：本报告与修复计划。
- `decision-layout-investigation.ts`：真实组件、只读本地 API fixture 和几何断言。
- `geometry.json`：六组场景的原始 DOM/CSS 尺寸。
- `original-before-jump.png`：402 × 874 原文案与按钮溢出。
- `safe-area-jump.png`：状态变更加安全区后，浮层覆盖发布横幅。
- `safe-area-after-click.png`：0 高 transcript 下点击后浮层仍未消失；其字节与上一张相同。
- `stress-before-jump.png`：长英文不可断词导致横向溢出。
- `font-150-before-jump.png`、`narrow-before-jump.png`、`wide-before-jump.png`：字号、320 px 和 430 px 边界。

## M5：桌面附件与多行草稿的决定区预算

独立评审复现：桌面窗口中，16 个附件和 30 行草稿可将决定区压缩到零高度，选项及 worker 回答控件不可达。将决定区的非零最小预算及 seat 滚动兜底扩展到桌面，同时保留空决定区不占高度的行为；transcript 保持在 seat 滚动范围之外。设置与用量弹层通过 portal 定位到触发控件，限制在视口内并可滚动，不受 seat 裁剪。

先在隔离真实应用 fixture 增加附件与多行草稿的组合场景，确认旧样式失败，再修复样式。覆盖桌面正常/受限高度和移动端；验证选项、回复、提交、队列、附件移除与 composer 可通过滚动到达；聚焦高 composer 后直接点击 header 跳转必须显示最新 transcript；短桌面用量弹层的标题、百分比、token 与按钮均可达。复跑已有弹层、跳转、提交状态及其他布局回归。Browser plugin not available；使用既有 Playwright 流程。
