import type { IssueView } from "../api/generated/api.ts";
import { useEffect, useState } from "react";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { IssuePlanRead } from "./PlanRead.tsx";
import styles from "./Issues.module.css";
export function IssueHistory({ issue }: { issue: IssueView }) {
  const { live } = useServices();
  const read = live.issues.history;
  const state = useStore(read.state);
  const [selection, setSelection] = useState(String(issue.revision));
  const revision = Number(selection);
  const valid = Number.isInteger(revision) && revision >= 1 && revision <= issue.revision;
  useEffect(() => {
    if (valid) void live.issues.openHistory(issue.issue_id, revision);
    return () => read.close();
  }, [live, read, issue.issue_id, revision, valid]);
  const value = state.value;
  const matching = value?.issue_id === issue.issue_id && value.revision === revision;
  return (
    <>
      <h3>定义历史</h3>
      <label>
        版本
        <input
          type="number"
          min={1}
          max={issue.revision}
          value={selection}
          onChange={(event) => setSelection(event.target.value)}
        />
      </label>
      {matching ? (
        <article>
          <h3>{value.plan.title}</h3>
          <p>版本 {value.revision}</p>
          <IssuePlanRead issue={value} showIntent />
        </article>
      ) : state.error ? (
        <p role="status">
          历史读取失败。<button onClick={() => void read.refresh()}>重新读取</button>
        </p>
      ) : (
        <p className={styles.muted}>{valid ? "正在读取定义历史…" : "选择有效版本查看。"}</p>
      )}
    </>
  );
}
