import type { ExecutionState, TaskSummary, WorkItemView } from "../api/generated/api.ts";
import { isFinished } from "../tasks/status.ts";

export type TaskTab = "current" | "ended" | "all";
const priority: Record<ExecutionState, number> = {
  waiting_owner: 0,
  unknown: 1,
  running: 2,
  accepting: 2,
  starting: 3,
  recovering: 3,
  queued: 4,
  paused: 5,
  idle: 6,
  completed: 7,
  cancelled: 7,
};
export function currentState(task: TaskSummary): ExecutionState {
  if (isFinished(task)) return task.lifecycle as "completed" | "cancelled";
  // Missing execution facts never prove that a worker is running.
  return task.execution?.state ?? "idle";
}
export function uniqueTasks(item: WorkItemView): TaskSummary[] {
  return [...new Map((item.tasks ?? []).map((task) => [task.task_id, task])).values()];
}
export function selectTasks(tasks: TaskSummary[], tab: TaskTab, state: ExecutionState | null) {
  return tasks
    .filter((task) => tab === "all" || isFinished(task) === (tab === "ended"))
    .filter((task) => !state || currentState(task) === state)
    .sort((a, b) => {
      if (isFinished(a) && isFinished(b)) {
        return (
          (b.ended_at ?? b.completed_at ?? 0) - (a.ended_at ?? a.completed_at ?? 0) ||
          b.task_id.localeCompare(a.task_id)
        );
      }
      return (
        priority[currentState(a)] - priority[currentState(b)] || a.task_id.localeCompare(b.task_id)
      );
    });
}
export function progressCounts(tasks: TaskSummary[]) {
  const counts = new Map<ExecutionState, number>();
  for (const task of tasks) {
    const state = currentState(task);
    if (!isFinished(task)) counts.set(state, (counts.get(state) ?? 0) + 1);
  }
  return [...counts.entries()].sort(([a], [b]) => priority[a] - priority[b] || a.localeCompare(b));
}
