import type { ExecutionCounts, IssueSummary, IssueWorkStatus } from "../api/generated/api.ts";

export const boardColumns: readonly { id: IssueWorkStatus; label: string }[] = [
  { id: "backlog", label: "待安排" },
  { id: "todo", label: "待推进" },
  { id: "in_progress", label: "进行中" },
  { id: "in_review", label: "待检查" },
  { id: "done", label: "已完成" },
  { id: "cancelled", label: "已取消" },
];

export const workStatus = (
  item: Pick<IssueSummary, "board" | "state" | "work_status">,
): IssueWorkStatus =>
  item.board?.status ??
  (item.state === "closed"
    ? "done"
    : item.state === "cancelled"
      ? "cancelled"
      : item.work_status || "todo");

export const workStatusLabel = (status: IssueWorkStatus) =>
  boardColumns.find((column) => column.id === status)?.label ?? status;

export function executionLabel(counts?: ExecutionCounts): string {
  if (!counts) return "暂无执行";
  if (counts.unknown) return "执行待核对";
  if (counts.waiting_owner) return "等待你处理";
  if (counts.running) return `${counts.running} 项运行中`;
  if (counts.accepting) return "独立验收中";
  if (counts.recovering || counts.starting) return "执行准备中";
  if (counts.queued) return "任务排队中";
  if (counts.paused) return "执行已暂停";
  if (counts.idle) return "任务待安排";
  if (counts.completed || counts.cancelled) return "本轮执行已结束";
  return "暂无执行";
}

export function notificationLabel(due: number, now: number): string {
  const seconds = Math.max(0, Math.ceil((due - now) / 1000));
  if (!seconds) return "等待通知负责人";
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")} 后通知负责人`;
}

export const blockerLabels: Record<string, string> = {
  child_responsibility: "子 Issue 仍在推进",
  child_source_review: "子 Issue 的补充资料待核对",
  source_review: "新增资料待负责人核对",
  task_responsibility: "关联任务尚未结束",
  task_execution: "任务执行尚未收束",
  task_workspace: "任务工作区尚未释放",
  task_decision: "任务有待决事项",
  task_message_unknown: "任务操作结果待核对",
  completion_review: "任务完成结果待负责人核对",
  attention: "待处理事项尚未解决",
  follow_on: "已有承诺或问题尚未处理",
  stage: "阶段尚未完成",
  issue_dependency: "前置 Issue 尚未完成",
  issue_cancelling: "取消流程尚未结束",
};
