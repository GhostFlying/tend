import type {
  ExecutionCounts,
  IssueBoardColumn,
  IssueSummary,
  IssueWorkStatus,
} from "../api/generated/api.ts";

export const issueWorkStatuses: readonly { id: IssueWorkStatus; label: string }[] = [
  { id: "backlog", label: "待安排" },
  { id: "todo", label: "待推进" },
  { id: "in_progress", label: "进行中" },
  { id: "in_review", label: "待检查" },
  { id: "done", label: "已完成" },
  { id: "cancelled", label: "已取消" },
];

export const boardColumns: readonly { id: IssueBoardColumn; label: string }[] = [
  { id: "backlog", label: "Backlog" },
  { id: "wip", label: "WIP" },
  { id: "waiting", label: "Waiting" },
  { id: "done", label: "Done" },
  { id: "cancelled", label: "Cancelled" },
];

export const workStatus = (
  item: Pick<IssueSummary, "board" | "state" | "work_status" | "effective_work_status">,
): IssueWorkStatus =>
  item.effective_work_status ??
  item.board?.status ??
  (item.state === "closed"
    ? "done"
    : item.state === "cancelled"
      ? "cancelled"
      : item.work_status || "todo");

export const workStatusLabel = (status: IssueWorkStatus) =>
  issueWorkStatuses.find((column) => column.id === status)?.label ?? status;

export function boardDropStatus(
  item: IssueSummary,
  column: IssueBoardColumn,
): IssueWorkStatus | undefined {
  if (item.board?.column === column) return undefined;
  switch (column) {
    case "waiting":
      return workStatus(item) === "backlog" ? "todo" : undefined;
    case "wip":
      return undefined;
    default:
      return column;
  }
}

export const waitingExternalLabel = "等待外部条件";
const pausedExecutionLabel = "执行已暂停";

export function executionLabel(counts?: ExecutionCounts): string {
  if (!counts) return "暂无执行";
  if (counts.unknown) return "执行待核对";
  if (counts.waiting_owner) return "等待你处理";
  if (counts.waiting_supervisor) return "等待负责人";
  if (counts.running) return `${counts.running} 项运行中`;
  if (counts.accepting) return "独立验收中";
  if (counts.recovering || counts.starting) return "执行准备中";
  if (counts.queued) return "任务排队中";
  if (counts.waiting_external) return waitingExternalLabel;
  if (counts.paused) return pausedExecutionLabel;
  if (counts.idle) return "任务待安排";
  return "暂无执行";
}

// Current responsibility stays open after its historical executions finish.
export function responsibilityLabel(item: IssueSummary): string {
  const board = item.board;
  const counts = board?.execution;
  if (item.state === "closed" || item.state === "cancelled") {
    const labels = [item.state === "closed" ? "Issue 已完成" : "Issue 已取消"];
    const stopping =
      (counts?.starting ?? 0) +
      (counts?.running ?? 0) +
      (counts?.accepting ?? 0) +
      (counts?.recovering ?? 0);
    if (stopping) labels.push(`${stopping} 项执行收尾中`);
    if (counts?.unknown) labels.push("执行结果待核对");
    return labels.join(" · ");
  }
  if (item.state === "cancelling") return "取消收尾中";
  if (item.assignment_state !== "active") return "Agent 交接中";
  const summarize = (label: string, includeRunning = false) => {
    const labels = [label];
    if (includeRunning && counts?.running) labels.push(`${counts.running} 项运行中`);
    if (counts?.waiting_external && label !== waitingExternalLabel)
      labels.push(waitingExternalLabel);
    if (counts?.paused && label !== pausedExecutionLabel) labels.push(pausedExecutionLabel);
    return labels.join(" · ");
  };
  if (board?.column === "waiting") {
    if (board.wait_hint === "ci") return "等 CI";
    if (board.wait_hint === "decision") return "等你决定";
  }
  if (counts?.unknown) return summarize("执行结果待核对", true);
  if (board?.owner_attention_count || counts?.waiting_owner) return summarize("等待你处理", true);
  if (board?.attention_count || counts?.waiting_supervisor)
    return summarize("等待 Agent 处理", true);
  const execution = executionLabel(counts);
  if (execution !== "暂无执行") return summarize(execution);
  if (item.state === "paused") return "Issue 已暂停";
  if (board?.children?.running) return "子 Issue 正在推进";
  return "";
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
