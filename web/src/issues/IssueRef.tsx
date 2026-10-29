import { useEffect, useMemo } from "react";
import type { IssueView } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { WorkItemRead } from "../workitems/live.ts";
import styles from "../conversation/TaskRef.module.css";

/** Text contains only a candidate identity; the current Issue API confirms it. */
export function IssueRef({ id }: { id: string }) {
  const { client, live, openIssue } = useServices();
  const read = useMemo(() => new WorkItemRead<IssueView>(client), [client]);
  const state = useStore(read.state);
  useEffect(() => {
    void read.open(`/api/v1/issues/${encodeURIComponent(id)}`);
    const stop = live.snapshot.subscribe(() => void read.refresh());
    return () => {
      stop();
      read.close();
    };
  }, [client, live, read, id]);
  const issue = state.value;
  if (!issue || issue.issue_id !== id) return <>{id}</>;
  return (
    <button className={styles.chip} onClick={() => openIssue(issue.issue_id)} title={id}>
      <span className={styles.title}>{issue.plan.title}</span>
    </button>
  );
}
