# Workspace 与交付物的 Web / 工具接点

细化 workspace-reuse-and-deliverables 计划。后端维护资源独占、来源绑定、交付文件和授权；本子任务只负责主 Agent 工具说明、上下文提示和 Web 呈现，不改变调度或任务创建身份。

主 Agent 依据自然语言选择 workspace.mode=new/reuse/temporary；复用先解析明确的任务引用，再读取真实资源与产物，不把 follow_up 关系误当自动继承。deliverables 可组合 commit/report/files，保留旧 delivery=commit。工具 schema 支持新字段但不改变省略字段的旧请求；报告任务可用独立普通目录，独立验收判断交付而非强求 Git 改动。

详情展示实际 workspace 模式、资源身份、主机、目录与占用、来源任务跳转；缺字段使用旧视图兼容展示。完成任务继续优先显示当前产物，报告 Markdown、证据、不确定项和文件清单与 Git 产物并列。下载只按不可变 file_id 构造同源 owner API 请求，通过现有客户端 Bearer 鉴权并处理登录恢复、错误、取消与 Blob URL 清理；不从报告读取任意下载地址，不把 token 放 URL。保留原始记录折叠，完成通知复用既有系统卡片与 TaskRef。

验证重点为报告无 Git、混合交付、历史兼容、来源跳转、当前产物身份、授权下载/失败/取消与令牌刷新，及桌面/390/320 明暗布局。Browser plugin not available，采用既有 Playwright 生产构建合成 fixture；临时资源 finally 清理，不接触真实模型/工作区。Web 检查与工具针对性测试后由主任务统一 make check / 独立 review；共享工作区不自行 commit 或 push。

主 Agent 增加 files_read 只读工具：仅允许产物已提交的不可变文件，字节 offset 与 max_bytes（默认 16 KiB，范围 4–64 KiB）窗口；按 UTF-8 码点裁齐并返回 next_offset，二进制只返回清单/标记。SQL 有界读取避免整文件装入工具内存，不改变 owner 下载权限。外接 Agent 继续保留 D48 的唯一 task alias 去重；复用时该别名不创建或重命名资源。

提供方备注的任务链接使用已有详情读取路径：初次打开 ?task=合法任务 ID 时自动打开详情，支持已完成和跨项目任务；严格使用现有任务 ID 解析，拒绝混入 URL/脚本/额外文本。刷新同一链接仍定位同任务，不增加路由框架或自动写操作。
