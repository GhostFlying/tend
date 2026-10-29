# Issue 浏览器 CI 迁移（r8）

Refs: D121、D123。前件r7包含最终固定owner schema/TS；r7唯一typecheck blocker为旧fixture类型，必须与本片组合后提交。r7冻结不动，本片先计划后编码。没有真实服务/账本/模型/部署操作。

## 原CI目标与实际迁移

保留ci.yml实际八命令，不以额外harness替代，不删失败脚本/断言凑绿。恢复被退役时误删的work-items-layout/delivery-refresh/followups三个命令并迁移为真正canonical Issue场景；其它mobile/decision/repos/agents/usage脚本更新真实当前入口/API/owner投影。旧fixture用真实IssuePage/IssueView，不手写旧DTO或兼容alias；Choice/Decision原问题/版本/答案/unknown请求效果与raw ledger身份保留。

逐项保留：手机入口/viewport/滚动/焦点/composer；资源/repo登记、项目过滤不筛掉全局host；Issue树/责任与task执行区分；交付unknown原请求与新观察；跟进创建/编辑/触发/历史/恢复；会话关联版本、迟到流、消息与附件身份；usage/context/model菜单与rollout。已退stage/旧route字面断言明确对应递归Issue/真实历史Choice，不恢复旧业务面。

## 独立产品修复

IssueBoard的原项目筛选遗漏是实际功能回归。恢复既有黑白header筛选/清除/项目选项，使用真实controller project参数；翻页/重读保filter，主机资源仍全局。保已确认层级布局，无新导航/缓存。手机资源访问迁移现有Issue→任务执行记录→资源与记录路径，不创建另一drawer。

按有限能力矩阵收口既有能力：项目筛选查询跨父层级的真正匹配Issue，显示实际parent_issue_id；已结束选择传根、子项与分页。当前与所选revision的完整持久定义、后续事项、收尾/留存依据与默认工作区源task只读取该snapshot，折叠展示而不拼当前事实。列表保手动刷新、失败保留与离线可见说明。

跟进复用原表单/时区解析及样式，完整at/every/condition、expiry/max/固定目标、编辑CAS与说明、原来源/连续静默历史；deadline-owned规则只读，不提供通用控制。新增规则和规则编辑沿原DurableOp，unknown恢复原请求。真实followup.changed经既有LiveData失效分支刷新打开的canonical reader，不加timer/cache。

交付以原refreshBaseline/refreshReconciled判断何时可显式再次观察。只给既有DurableOp请求表增加只读prefix枚举，保每个原Envelope/回执核实；completed事实不冒充旧unknown请求成功，也不删除旧请求。新观察按真实baseline选局部scope，无新表/ledger/自动重试。

原手机待处理tab关闭覆盖层时保留选中view，恢复原Sidebar设置可达性；任务详情手机管理按钮恢复原36px最小触达。均复用原组件/导航，没有新增布局方向。

原DecisionDock几何/滚动测试明确作为Inbox503时保留的真实fallback，只允许该准确路由的预期503错误，其他console仍严格。正常Inbox展示原问题/版本以及TaskDetails原DecisionCard的decisions.respond路径独立覆盖，不使用200null伪造旧模式。旧讨论模板由已确认的负责人会话/真实task→Issue读取替代；原未发送正文/附件、Choice原回答业务与任务控件继续覆盖。

## 验证与交付

实际执行仓库八个pnpm命令，逐一留exit/日志/截图，失败保留并定位后再修改。最终全Web类型、相关测试、lint、格式、构建；不运行Go/fullcheck。项目过滤真实request与已批黑白风格截图view_image核对。原source/附件/archivereader与独立验收不变。冻结UI修复与脚本/生成依赖分开，供独立review与root最终组合。
