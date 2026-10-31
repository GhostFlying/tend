import type { ExecutionCounts, TaskSummary } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useTaskSummary } from "../conversation/TaskRef.tsx";
import { executionCounts, executionLook, queueReasonText } from "./execution.ts";
import styles from "./Execution.module.css";

export function ExecutionSummary({ counts }: { counts?: ExecutionCounts | undefined }) {
  if (!counts) return null;
  const facts = executionCounts(counts);
  return (
    <span className={styles.counts} aria-label="执行概况">
      {facts.length ? (
        facts.map((fact) => (
          <span className={styles.badge} data-state={fact.state} key={fact.state}>
            {fact.text}
          </span>
        ))
      ) : (
        <span className={styles.empty}>尚无执行任务</span>
      )}
    </span>
  );
}

export function TaskExecution({
  task,
  showState = true,
}: {
  task: TaskSummary;
  showState?: boolean;
}) {
  const execution = task.execution;
  if (!execution) return null;
  const reason = execution.queue_reason;
  const reasonText = queueReasonText(task);
  if (!showState && !reasonText) return null;
  const look = executionLook(execution);
  return (
    <div className={styles.task} aria-label="任务执行状态">
      {showState && (
        <span className={styles.badge} data-state={execution.state}>
          {look.label}
        </span>
      )}
      {reasonText && (
        <span className={styles.reason} title={reason?.message}>
          <span>{reasonText}</span>
          {reason?.task_id && (
            <WaitingTask id={reason.task_id} occupied={reason.code === "workspace_busy"} />
          )}
          {reason?.host_id && <span>主机 {reason.host_id}</span>}
        </span>
      )}
    </div>
  );
}
function WaitingTask({ id, occupied }: { id: string; occupied: boolean }) {
  const { openTask } = useServices();
  const task = useTaskSummary(id);
  const label = occupied ? "占用任务" : "前序任务";
  return (
    <button
      type="button"
      className={styles.reference}
      aria-label={`查看${label}：${task?.title ?? "查看任务"}`}
      onClick={() => openTask(id)}
    >
      {label}：{task?.title ?? "查看任务"} ›
    </button>
  );
}
