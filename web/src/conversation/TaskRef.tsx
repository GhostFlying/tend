// A task ID in a reply, shown as a chip with the task's current title and
// status (D72). It opens the task's details. An ID tend does not know
// stays plain text.
import { useEffect } from "react";
import type { TaskSummary } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { StatusDot } from "../shell/StatusDot.tsx";
import { useStore } from "../state/store.ts";
import { taskLook } from "../tasks/status.ts";
import styles from "./TaskRef.module.css";

/** The task as the snapshot lists it, or looked up; undefined while unknown. */
export function useTaskSummary(taskId: string): TaskSummary | null | undefined {
  const { live } = useServices();
  const snapshot = useStore(live.snapshot);
  const lookups = useStore(live.lookups);
  const listed = snapshot?.tasks?.find((t) => t.task_id === taskId);
  const looked = lookups.get(taskId);
  const missing = snapshot !== null && !listed && looked === undefined;
  useEffect(() => {
    if (missing) {
      void live.lookUp(taskId);
    }
  }, [live, taskId, missing]);
  return listed ?? looked;
}

export function TaskRef({ taskId }: { taskId: string }) {
  const { openTask, openIssue } = useServices();
  const task = useTaskSummary(taskId);
  if (!task) {
    return <>{taskId}</>;
  }
  const look = taskLook(task);
  return (
    <>
      <button
        type="button"
        className={styles.chip}
        onClick={() => openTask(taskId)}
        title={`${look.label} · ${taskId}`}
      >
        <StatusDot tone={look.tone} />
        <span className={styles.title}>{task.title}</span>
      </button>
      {task.primary_issue_id && (
        <button
          className={styles.chip}
          onClick={() => openIssue(task.primary_issue_id!)}
          title={task.primary_issue_id}
        >
          <span className={styles.title}>
            所属Issue：{task.primary_issue_title || task.primary_issue_id}
          </span>
        </button>
      )}
    </>
  );
}
