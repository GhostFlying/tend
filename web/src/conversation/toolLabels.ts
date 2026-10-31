import type { IssueConversationItem } from "../api/generated/api.ts";

export const toolLabels: Record<string, string> = {
  actions_list: "查看执行安排",
  agents_runner_read: "查看执行 Agent 登记",
  attentions_confirm: "确认待处理事项",
  attentions_respond: "回答待处理事项",
  attentions_update: "更新待处理事项",
  attentions_withdraw: "撤回待处理事项",
  background_submit: "提交背景来源",
  decisions_list: "列出决策",
  tasks_update_intent: "更新任务意向",
  issues_cancel: "取消 Issue",
  issues_context_fact: "记录 Issue 事实",
  issues_dependencies: "更新 Issue 依赖",
  issues_finish_cancel: "结束 Issue 取消",
  issues_followups_pause: "暂停持续跟进",
  issues_followups_resume: "恢复持续跟进",
  issues_pause: "暂停 Issue",
  issues_resume: "恢复 Issue",
  issues_refresh_delivery: "核对 Issue 交付",
  issues_review_completions: "核对 Issue 完成条件",
  issues_review_sources: "核对 Issue 来源",
  issues_set_status: "更新 Issue 状态",
  issues_update_task: "更新关联任务",
  issues_list: "列出 Issue",
  issues_read: "查看 Issue",
  issues_create: "创建 Issue",
  issues_assign: "交接 Issue",
  issues_progress: "更新 Issue 进展",
  issues_update_progress: "更新 Issue 进展",
  issues_close: "结束 Issue",
  issues_reopen: "重开 Issue",
  issues_create_task: "创建任务",
  issues_context_read: "读取 Issue 背景",
  issues_context_review: "核对 Issue 背景",
  issues_turn_read: "读取 Issue 本轮消息",
  issues_turn_commit: "确认 Issue 本轮核对",
  issues_finish_assignment: "结束 Issue 责任",
  issues_plan_put: "更新 Issue 计划",
  issues_deliveries_read: "查看 Issue 交付",
  issues_followups_create: "创建持续跟进",
  issues_followups_list: "查看持续跟进",
  issues_followups_history: "查看跟进历史",
  issues_followups_trigger: "触发持续跟进",
  issues_followups_update: "更新持续跟进",
  issues_followups_cancel: "取消持续跟进",
  issues_deadline_put: "设置 Issue 截止时间",
  issues_deadline_cancel: "取消 Issue 截止时间",
  issues_request_manager: "安排负责人 Agent",
  issues_manager_migration: "迁移负责人 Agent",
  issues_update: "更新 Issue",
  tasks_create: "创建任务",
  tasks_read: "查看任务",
  tasks_list: "列出任务",
  tasks_define: "更新任务要求",
  tasks_rate: "设置任务优先级",
  tasks_set_control: "调整任务控制",
  tasks_cancel: "取消任务",
  tasks_start: "启动任务",
  tasks_update: "更新任务",
  attentions_create: "创建待处理事项",
  attentions_read: "查看待处理事项",
  attentions_list: "列出待处理事项",
  attentions_answer: "回答待处理事项",
  decisions_open: "请求你的决策",
  decisions_resolve: "记录决策回答",
  decisions_answer: "回答决策",
  decisions_read: "查看决策",
  decisions_explain: "解释决策",
  decisions_respond: "回答决策",
  conversation_read: "读取对话",
  conversation_reply: "回复对话",
  conversation_associate: "关联 Issue",
  conversation_associate_issues: "关联 Issue",
  conversation_turn_read: "读取本轮消息",
  conversation_turn_commit: "确认本轮核对",
  conversation_turn_adopt: "接续本轮核对",
  conversation_turn_wait: "等待本轮确认",
  conversation_submit_message: "发送对话消息",
  conversation_compact: "整理对话上下文",
  conversation_handoff: "交接主 Agent",
  conversation_interrupt_reply: "停止回复",
  conversation_consult: "发起咨询",
  conversation_set_settings: "更新对话设置",
  conversation_set_handoff_settings: "更新交接设置",
  background_sources: "读取背景来源",
  background_item_read: "读取背景条目",
  background_evidence_read: "读取背景证据",
  background_checkin: "提交后台核对",
  runs_send_message: "补充执行消息",
  runs_read_messages: "读取执行消息",
  runs_read: "查看执行",
  repos_list: "查看仓库",
  files_read: "读取文件",
  download_file: "下载文件",
  attachments_read: "读取附件",
  artifacts_read: "查看产物",
  requests_read: "核实请求结果",
  actions_create: "安排执行",
  tend_context: "读取当前上下文",
  agent_categories_list: "查看 Agent 类别",
  agents_hosted_list: "查看负责人 Agent 目录",
  agent_conversations_list: "查看后台对话",
  agent_rollout_read: "读取 Agent 执行记录",
};

export function toolLabel(name: string): string {
  return toolLabels[name.replaceAll(".", "_")] ?? name.replaceAll("_", " ");
}

/** Only adjacent completed calls belong together; messages and live calls are boundaries. */
export function groupToolItems(items: readonly IssueConversationItem[]): IssueConversationItem[][] {
  const groups: IssueConversationItem[][] = [];
  for (const item of items) {
    const previous = groups.at(-1);
    const completed =
      !["owner", "system", "external"].includes(item.author) &&
      item.tool?.status === "completed" &&
      !item.choices?.length;
    const last = previous?.at(-1);
    if (
      completed &&
      last?.author === item.author &&
      last.tool?.status === "completed" &&
      last.tool.turn_id === item.tool?.turn_id &&
      last.reply_to === item.reply_to &&
      !last.choices?.length
    )
      previous!.push(item);
    else groups.push([item]);
  }
  return groups;
}
