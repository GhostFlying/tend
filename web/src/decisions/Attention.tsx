// What needs the owner: the open decisions and the tasks they took over.
// The sidebar lists them as rows; the phone's 待处理 tab shows them as
// cards to answer and hand back right there.
import type { Snapshot, TaskSummary } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import controls from "../shell/controls.module.css";
import { StatusDot } from "../shell/StatusDot.tsx";
import list from "../shell/list.module.css";
import { useStore } from "../state/store.ts";
import { isFinished } from "../tasks/status.ts";
import styles from "./Attention.module.css";
import { DecisionCard, HandbackCard } from "./DecisionCard.tsx";
import { dockFocus, openDecisions, taskOf } from "./DecisionDock.tsx";
import { question } from "./text.ts";

export function takenOver(snapshot: Snapshot | null): TaskSummary[] {
  return (snapshot?.tasks ?? []).filter(
    (t) => t.control_mode === "human_control" && !isFinished(t),
  );
}

/** How many things wait for the owner. */
export function attentionCount(snapshot: Snapshot | null): number {
  return openDecisions(snapshot).length + takenOver(snapshot).length;
}

export function Attention() {
  const { live, openTask } = useServices();
  const snapshot = useStore(live.snapshot);
  const decisions = openDecisions(snapshot);
  const held = takenOver(snapshot);
  const count = decisions.length + held.length;

  return (
    <section className={styles.root} aria-label="需要你处理">
      <div className={list.sectionTitle}>
        <span>需要你处理</span>
        {count > 0 && <span className={list.count}>{count}</span>}
      </div>
      {count === 0 && (
        <p className={controls.empty}>{snapshot ? "没有需要你处理的事。" : "正在读取…"}</p>
      )}
      <div className={styles.rows}>
        {decisions.map((d) => {
          const task = taskOf(snapshot, d.task_id);
          return (
            <button
              key={d.decision_id}
              type="button"
              className={list.row}
              title={question(d).text}
              onClick={() => {
                dockFocus.set(d.decision_id);
                if (d.task_id) {
                  openTask(d.task_id);
                }
              }}
            >
              <StatusDot tone="warn" />
              <span className={list.label}>
                {task ? `${task.title}：` : ""}
                {question(d).short}
              </span>
              <span className={list.meta}>待决</span>
            </button>
          );
        })}
        {held.map((t) => (
          <button
            key={t.task_id}
            type="button"
            className={list.row}
            onClick={() => openTask(t.task_id)}
          >
            <StatusDot tone="warn" />
            <span className={list.label}>{t.title}：等你交还</span>
            <span className={list.meta}>接管中</span>
          </button>
        ))}
      </div>
      <div className={styles.cards}>
        {decisions.map((d) => (
          <DecisionCard
            key={`${d.decision_id}@${d.version}`}
            decision={d}
            task={taskOf(snapshot, d.task_id)}
          />
        ))}
        {held.map((t) => (
          <HandbackCard key={t.task_id} task={t} />
        ))}
      </div>
    </section>
  );
}
