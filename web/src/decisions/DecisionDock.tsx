// The open decisions, docked above the composer one at a time, with
// paging between them.
import type { DecisionView, Snapshot, TaskSummary } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { Store, useStore } from "../state/store.ts";
import { DecisionCard } from "./DecisionCard.tsx";
import styles from "./DecisionDock.module.css";

/** The decision the dock shows; the sidebar sets it when one is picked. */
export const dockFocus = new Store<string | null>(null);

export function openDecisions(snapshot: Snapshot | null): DecisionView[] {
  return (snapshot?.decisions ?? []).filter((d) => d.status === "open");
}

export function taskOf(snapshot: Snapshot | null, taskId: string | undefined) {
  return taskId ? snapshot?.tasks?.find((t: TaskSummary) => t.task_id === taskId) : undefined;
}

export function DecisionDock() {
  const { live } = useServices();
  const snapshot = useStore(live.snapshot);
  const focus = useStore(dockFocus);
  const decisions = openDecisions(snapshot);
  if (decisions.length === 0) {
    return null;
  }
  const at = Math.max(
    0,
    decisions.findIndex((d) => d.decision_id === focus),
  );
  const d = decisions[at]!;
  const go = (i: number) =>
    dockFocus.set(decisions[(i + decisions.length) % decisions.length]!.decision_id);
  const pager =
    decisions.length > 1 ? (
      <>
        <button type="button" onClick={() => go(at - 1)} aria-label="上一个待决事项">
          ‹
        </button>
        <span className={styles.count}>
          {at + 1} / {decisions.length}
        </span>
        <button type="button" onClick={() => go(at + 1)} aria-label="下一个待决事项">
          ›
        </button>
      </>
    ) : undefined;
  return (
    <div className={styles.dock}>
      <DecisionCard
        key={`${d.decision_id}@${d.version}`}
        decision={d}
        task={taskOf(snapshot, d.task_id)}
        more={pager}
      />
    </div>
  );
}
