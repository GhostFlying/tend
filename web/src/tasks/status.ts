// How a task's state reads: a status dot and a short Chinese label. The
// order follows what the owner needs to know first: finished, then who
// controls the task, then what blocks it, then its stage.
import type {
  BlockReason,
  ControlMode,
  Rating,
  TaskLifecycle,
  TaskSummary,
} from "../api/generated/api.ts";
import type { Tone } from "../shell/StatusDot.tsx";

export interface TaskLook {
  tone: Tone;
  label: string;
}

export const lifecycleLabels: Record<TaskLifecycle, string> = {
  draft: "草稿",
  queued: "排队中",
  running: "实现中",
  awaiting_acceptance: "验收中",
  changes_required: "待返工",
  completed: "已完成",
  cancelled: "已取消",
};

export const controlLabels: Record<ControlMode, string> = {
  managed: "托管",
  paused: "已暂停",
  human_control: "接管中",
};

export const blockLabels: Record<BlockReason, string> = {
  none: "",
  waiting_resource: "等待资源",
  waiting_decision: "待决",
  waiting_approval: "等待批准",
  acceptance_env_unavailable: "验收环境不可用",
  budget_exhausted: "实现次数用完",
  workspace_unavailable: "工作区不可用",
};

export const ratingLabels: Record<Rating, string> = {
  "": "未评级",
  p0: "p0 紧急",
  p1: "p1",
  p2: "p2",
  p3: "p3",
};

type State = Pick<TaskSummary, "lifecycle" | "control_mode" | "block_reason">;

export function taskLook(t: State): TaskLook {
  switch (t.lifecycle) {
    case "completed":
      return { tone: "ok", label: lifecycleLabels.completed };
    case "cancelled":
      return { tone: "idle", label: lifecycleLabels.cancelled };
  }
  if (t.control_mode === "human_control") {
    return { tone: "warn", label: controlLabels.human_control };
  }
  if (t.control_mode === "paused") {
    return { tone: "idle", label: controlLabels.paused };
  }
  if (t.block_reason !== "none" && t.block_reason !== "waiting_resource") {
    return { tone: "warn", label: blockLabels[t.block_reason] };
  }
  switch (t.lifecycle) {
    case "running":
    case "awaiting_acceptance":
      return { tone: "run", label: lifecycleLabels[t.lifecycle] };
    default:
      return { tone: "idle", label: lifecycleLabels[t.lifecycle] };
  }
}

/** Whether the task needs the owner: a question or a handback. */
export function needsOwner(t: State): boolean {
  return (
    !isFinished(t) && (t.control_mode === "human_control" || t.block_reason === "waiting_decision")
  );
}

export function isFinished(t: Pick<TaskSummary, "lifecycle">): boolean {
  return t.lifecycle === "completed" || t.lifecycle === "cancelled";
}
