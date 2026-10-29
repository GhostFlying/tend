import { useEffect, useMemo, useState } from "react";
import type { IssueTaskSummary, TaskDetail } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { taskLook } from "../tasks/status.ts";
import styles from "./Issues.module.css";

const runLabels: Record<string, string> = {
  starting: "正在启动",
  running: "执行中",
  waiting: "等待中",
  finished: "已结束",
  failed: "执行失败",
  interrupted: "已中断",
  unknown: "结果未知",
};
export function IssueTaskPeek({
  task,
  initial,
  onTask,
}: {
  task: IssueTaskSummary;
  initial: boolean;
  onTask(id: string): void;
}) {
  const { client, live } = useServices();
  const [open, setOpen] = useState(initial);
  const read = useMemo(() => new WorkItemRead<TaskDetail>(client), [client]);
  const state = useStore(read.state);
  useEffect(() => {
    if (!open) return;
    void read.open(`/api/v1/tasks/${encodeURIComponent(task.task_id)}`);
    const stop = live.snapshot.subscribe(() => void read.refresh());
    return () => {
      stop();
      read.close();
    };
  }, [read, open, task.task_id, live]);
  const detail = state.value;
  return (
    <details
      className={styles.taskPeek}
      open={open}
      onToggle={(e) => setOpen(e.currentTarget.open)}
    >
      <summary>
        <strong>{task.title}</strong> <span className={styles.muted}>{taskLook(task).label}</span>
      </summary>
      {open && (
        <>
          {state.error ? (
            <p className={styles.warning}>
              执行记录暂时无法更新，不推断任务空闲或结束。
              <button onClick={() => void read.refresh()}>重新读取</button>
            </p>
          ) : !detail ? (
            <p>正在读取执行记录…</p>
          ) : (
            <>
              <p className={styles.muted}>
                {detail.workspace_state
                  ? `工作区 ${detail.workspace_state.name || detail.workspace} · ${detail.workspace_state.host}`
                  : "尚未记录已绑定工作区"}
              </p>
              {(["implement", "accept"] as const).map((role) => {
                const run = detail.runs
                  ?.filter((r) => r.role === role && r.spec_version === detail.spec_version)
                  .at(-1);
                return (
                  <div className={styles.phase} key={role}>
                    <strong>{role === "implement" ? "实施" : "独立验收"}</strong>
                    <span>{run ? run.run_id : "尚未开始"}</span>
                    <span>
                      {run
                        ? runLabels[run.observed] || run.observed
                        : role === "accept" && detail.lifecycle === "running"
                          ? "等待实施交付"
                          : "等待安排"}
                    </span>
                    {run && <button onClick={() => onTask(task.task_id)}>查看执行记录 →</button>}
                  </div>
                );
              })}
              {detail.execution?.queue_reason && (
                <p className={styles.muted}>{detail.execution.queue_reason.message}</p>
              )}
              {detail.execution?.state === "unknown" && (
                <p className={styles.warning}>先核实原执行结果，不自动重跑。</p>
              )}
            </>
          )}
          <button className={styles.button} onClick={() => onTask(task.task_id)}>
            查看任务详情
          </button>
        </>
      )}
    </details>
  );
}
