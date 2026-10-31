import { useEntryState } from "../app/router.ts";
import { useEffect, useMemo, useState, useRef } from "react";
import type { ArtifactDetail, IssueResult, IssueResultPage } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { ArtifactRef } from "../shell/Details.tsx";
import { ArtifactDelivery } from "../tasks/ArtifactDelivery.tsx";
import styles from "./DetailPage.module.css";

type ResultCardValue = Pick<IssueResult, "artifact" | "task_id" | "title" | "acceptances">;

export function resultStatus(result: ResultCardValue): string {
  const verdict = result.acceptances?.at(-1)?.verdict;
  return verdict === "passed"
    ? "独立验收通过"
    : verdict === "changes_required"
      ? "独立验收需修改"
      : verdict === "blocked"
        ? "验收受阻"
        : verdict === "inconclusive"
          ? "验收结论待定"
          : "待独立验收";
}
export function IssueResultCard({
  result,
  onTask,
}: {
  result: ResultCardValue;
  onTask(id: string): void;
}) {
  const { client } = useServices();
  const read = useMemo(() => new WorkItemRead<ArtifactDetail>(client), [client]);
  const state = useStore(read.state);
  const [expanded, setExpanded] = useEntryState(`artifact:${result.artifact.artifact_id}`, false);
  useEffect(() => {
    if (expanded)
      void read.open(`/api/v1/artifacts/${encodeURIComponent(result.artifact.artifact_id)}`);
    return () => read.close();
  }, [read, expanded, result.artifact.artifact_id]);
  const open = () => {
    setExpanded(true);
    if (expanded)
      void read.open(`/api/v1/artifacts/${encodeURIComponent(result.artifact.artifact_id)}`);
  };
  return (
    <article className={styles.delivery} data-artifact={result.artifact.artifact_id}>
      <div className={styles.sectionHead}>
        <strong>{result.title}</strong>
        <small data-verdict={result.acceptances?.at(-1)?.verdict}>{resultStatus(result)}</small>
      </div>
      <ArtifactDelivery
        value={expanded ? (state.value?.artifact.ref ?? result.artifact.ref) : result.artifact.ref}
      />
      {expanded && state.loading && !state.value && <p role="status">正在读取完整交付…</p>}
      {expanded && state.value && <ArtifactRef value={state.value.artifact.ref} />}
      {state.error && (
        <p role="status">
          完整交付读取失败。<button onClick={open}>重试</button>
        </p>
      )}
      <div className={styles.deliveryActions}>
        {!expanded && (
          <button className={styles.textLink} onClick={open}>
            打开完整交付 →
          </button>
        )}
        <button className={styles.textLink} onClick={() => onTask(result.task_id)}>
          查看任务与验收 →
        </button>
      </div>
    </article>
  );
}

/** This paginated reader never replaces the page's current-delivery index. */
export function IssueResultsList({ id, onTask }: { id: string; onTask(id: string): void }) {
  const { client, live } = useServices();
  const read = useMemo(() => new WorkItemRead<IssueResultPage>(client), [client]);
  const state = useStore(read.state);
  const [cursors, setCursors] = useState([""]);
  const cursor = cursors.at(-1)!;
  useEffect(() => {
    void read.open(
      `/api/v1/issues/${encodeURIComponent(id)}/results${cursor ? `?after=${encodeURIComponent(cursor)}` : ""}`,
    );
    const stop = live.subscribeEvents(() => void read.refresh());
    const reset = live.snapshot.subscribe(() => void read.refresh());
    return () => {
      stop();
      reset();
      read.close();
    };
  }, [id, cursor, read, live]);
  return (
    <>
      <h2>全部执行交付</h2>
      {state.error && (
        <p role="status">
          交付读取失败。<button onClick={() => void read.refresh()}>重试</button>
        </p>
      )}
      {!state.value && !state.error && <p>正在读取交付…</p>}
      {state.value?.items?.map((result) => (
        <IssueResultCard key={result.artifact.artifact_id} result={result} onTask={onTask} />
      ))}
      {state.value && !state.value.items?.length && <p>暂无执行交付。</p>}
      {cursors.length > 1 && (
        <button onClick={() => setCursors(cursors.slice(0, -1))}>上一页交付</button>
      )}
      {state.value?.more && (
        <button onClick={() => setCursors([...cursors, state.value!.next!])}>下一页交付</button>
      )}
    </>
  );
}

/** Older timeline cards fetch only their artifact when they enter the viewport. */
export function HistoricalResultCard({
  artifactID,
  onTask,
}: {
  artifactID: string;
  onTask(id: string): void;
}) {
  const { client, live } = useServices();
  const read = useMemo(() => new WorkItemRead<ArtifactDetail>(client), [client]);
  const state = useStore(read.state);
  const [visible, setVisible] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) setVisible(true);
    });
    if (ref.current) observer.observe(ref.current);
    return () => observer.disconnect();
  }, []);
  useEffect(() => {
    if (!visible) return;
    void read.open(`/api/v1/artifacts/${encodeURIComponent(artifactID)}`);
    const stop = live.subscribeEvents((event) => {
      if (event.object_type !== "message") void read.refresh();
    });
    const reset = live.snapshot.subscribe(() => void read.refresh());
    return () => {
      stop();
      reset();
      read.close();
    };
  }, [visible, read, artifactID, live]);
  return (
    <div ref={ref}>
      {state.value ? (
        <IssueResultCard result={{ ...state.value, title: "执行交付" }} onTask={onTask} />
      ) : (
        <p role="status">
          {state.error ? (
            <>
              交付读取失败。<button onClick={() => void read.refresh()}>重试</button>
            </>
          ) : (
            "正在读取交付…"
          )}
        </p>
      )}
    </div>
  );
}
