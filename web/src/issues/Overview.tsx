import { AssigneeName } from "./AssigneeName.tsx";
import { useEffect } from "react";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import styles from "./Issues.module.css";
export function IssueOverview({
  onSelect,
  onAll,
  active,
}: {
  onSelect(id: string): void;
  onAll(): void;
  active: boolean;
}) {
  const { live } = useServices();
  const state = useStore(live.issues.list.state);
  useEffect(() => {
    if (active) void live.issues.openList({ parentID: "" });
  }, [live, active]);
  const items = state.value?.items ?? [];
  return (
    <section className={styles.overview} aria-label="持续跟进">
      <p className={styles.muted}>持续跟进</p>
      {items.slice(0, 3).map((item) => (
        <button key={item.issue_id} onClick={() => onSelect(item.issue_id)}>
          <strong>{item.title}</strong>
          <small className={styles.muted}>
            <AssigneeName assignee={item.assignee} />
            负责
          </small>
        </button>
      ))}
      {state.value && items.length === 0 && <p className={styles.muted}>暂无持续跟进的Issue。</p>}
      {state.error && <p className={styles.muted}>暂时无法读取，保留上次结果。</p>}
      {(items.length > 3 || state.value?.more) && <button onClick={onAll}>查看全部Issue</button>}
    </section>
  );
}
