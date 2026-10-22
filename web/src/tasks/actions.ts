// The owner's task controls, through the same operations the tend CLI
// uses (internal/cli): control.set_mode for pause, resume (hand back) and
// take over, tasks.cancel for cancel. Each names the control revision the
// owner saw, so a write after someone else's change is refused as stale
// rather than applied to a task the owner did not see (invariant 3).
import type { ControlMode, TaskSummary } from "../api/generated/api.ts";
import { notify } from "../app/notice.ts";
import { runOp, type OpOutcome } from "../app/ops.ts";
import type { Services } from "../app/services.tsx";
import { taskLook } from "./status.ts";

const modeDone: Record<ControlMode, string> = {
  paused: "已暂停",
  managed: "已交还给 tend",
  human_control: "已接管：tend 不再推进它，处理完后交还",
};

type Task = Pick<TaskSummary, "task_id" | "title" | "control_revision">;

export function setControl(s: Services, task: Task, mode: ControlMode): Promise<OpOutcome> {
  return taskWrite(s, task, "control.set_mode", { mode }, modeDone[mode]);
}

export function cancelTask(s: Services, task: Task): Promise<OpOutcome> {
  return taskWrite(s, task, "tasks.cancel", {}, "已取消");
}

async function taskWrite(
  s: Services,
  task: Task,
  operation: string,
  payload: unknown,
  doneText: string,
): Promise<OpOutcome> {
  const outcome = await runOp(s.client, {
    operation,
    target: { type: "task", id: task.task_id },
    expected_control_revision: task.control_revision,
    payload,
  });
  switch (outcome.kind) {
    case "done":
      notify(`『${task.title}』${doneText}`);
      break;
    case "stale": {
      const now = outcome.current ? `，现在是「${taskLook(outcome.current).label}」` : "";
      notify(`『${task.title}』刚被改过${now}。已刷新，请再确认一次`, "warn");
      break;
    }
    case "refused":
      notify(`tend 没有执行：${outcome.message}`, "warn");
      break;
    case "unknown":
      notify(outcome.message, "warn");
      break;
  }
  void s.live.refreshSnapshot();
  if (s.live.detail.get()?.taskId === task.task_id) {
    void s.live.refreshDetail();
  }
  return outcome;
}
