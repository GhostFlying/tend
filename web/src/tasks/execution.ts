import type { ExecutionCounts, TaskSummary } from "../api/generated/api.ts";
import type { Tone } from "../shell/StatusDot.tsx";

export function executionLook(execution: NonNullable<TaskSummary["execution"]>): {
  label: string;
  tone: Tone;
} {
  switch (execution.state) {
    case "queued":
      return { label: execution.role === "accept" ? "排队中 · 待验收" : "排队中", tone: "warn" };
    case "starting":
      return { label: execution.role === "accept" ? "正在启动验收" : "正在启动", tone: "run" };
    case "running":
      return { label: "执行中", tone: "run" };
    case "accepting":
      return { label: "验收中", tone: "run" };
    case "waiting_owner":
      return { label: "等待用户", tone: "warn" };
    case "paused":
      return { label: "已暂停", tone: "idle" };
    case "unknown":
      return { label: "执行待核对", tone: "warn" };
    case "completed":
      return { label: "已完成", tone: "ok" };
    case "cancelled":
      return { label: "已取消", tone: "idle" };
    default:
      return { label: "尚未执行", tone: "idle" };
  }
}
const countLabels = [
  ["running", "执行中"],
  ["accepting", "验收中"],
  ["queued", "排队中"],
  ["waiting_owner", "等待用户"],
  ["paused", "已暂停"],
  ["unknown", "执行待核对"],
  ["idle", "尚未执行"],
  ["completed", "已完成"],
  ["cancelled", "已取消"],
] as const;

/** Counts are server facts, never inferred from active plan stages or task lifecycle. */
export function executionCounts(counts: ExecutionCounts): { state: string; text: string }[] {
  return countLabels.flatMap(([state, label]) =>
    counts[state] > 0 ? [{ state, text: `${counts[state]} ${label}` }] : [],
  );
}

/** Translate the server's chosen constraint; do not repeat scheduler predicates here. */
export function queueReasonText(task: Pick<TaskSummary, "execution" | "block_reason">): string {
  const reason = task.execution?.queue_reason;
  if (!reason) return "";
  switch (reason.code) {
    case "ready":
      return "等待执行主机领取";
    case "workspace_busy":
      return "等待工作区释放";
    case "dependency":
      return "等待前序任务";
    case "host_capacity":
      return "等待主机空闲槽位";
    case "host_unavailable":
      return "等待执行主机恢复可用";
    case "host_requirements":
      return "等待满足要求的执行主机";
    case "task_blocked":
      return (
        {
          waiting_decision: "等待用户决定",
          waiting_approval: "等待批准",
          acceptance_env_unavailable: "等待验收环境恢复",
          budget_exhausted: "等待调整实现次数",
          workspace_unavailable: "等待工作区恢复",
          waiting_resource: "等待资源",
          none: "等待任务条件满足",
        }[task.block_reason] ?? reason.message
      );
    default:
      return reason.message;
  }
}
