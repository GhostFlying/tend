# 最终界面实际调用合同修正

Refs: D121、D122、D125。正式最终 CLI review 识别四项可达问题，本片保留已批准布局并修复实际合同：归档材料保存后读取原消息真实 content_revision，与 message_id 一起冻结创建请求；owner 创建时直接选择已登记、同环境且具有 Issue manager 目的的具名 Agent，复用原 resolveHostedAssignee、单事务创建、manager conversation 和 source wake，不新增客户端双阶段状态、权限体系或请求机制；通知使用持久 kind=information；尚无 run 的排队任务在首 run 到达时派生默认选择，显式选中的历史 run 不随新执行变化。

owner 身份、request_id 去重、来源作者/撤回/内容版本、原 manager scope/epoch 与未知结果对账不改。以真实 owner caller 和原 API envelope 核对创建、精确重放和原 manager wake；浏览器 fixture 按真实创建校验，补齐归档、通知和排队→首 run 交互回归。旧固定完整检查、四 finding 与后续窄复审分开保留；本片完整检查、CI 与部署结果另记。
