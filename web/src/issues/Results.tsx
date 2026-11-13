import { useEntryState } from "../app/router.ts";
import { useEffect, useMemo, useState, useRef } from "react";
import type { ArtifactDetail, IssueResultPage } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { ArtifactRef } from "../shell/Details.tsx";
import { ArtifactDelivery } from "../tasks/ArtifactDelivery.tsx";
import { resultStatus, type ResultCardValue } from "./resultStatus.ts";
import styles from "./DetailPage.module.css";

export { resultStatus } from "./resultStatus.ts";
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
  const reviewRead = useMemo(() => new WorkItemRead<ArtifactDetail>(client), [client]);
  const reviewState = useStore(reviewRead.state);
  const [reviewExpanded, setReviewExpanded] = useState(false);
  const full =
    state.value?.artifact.artifact_id === result.artifact.artifact_id ? state.value : null;
  const reviewerArtifact = full?.reviewer_artifact_id ?? result.reviewer_artifact_id;
  useEffect(() => {
    if (reviewExpanded && reviewerArtifact)
      void reviewRead.open(`/api/v1/artifacts/${encodeURIComponent(reviewerArtifact)}`);
    return () => reviewRead.close();
  }, [reviewRead, reviewExpanded, reviewerArtifact]);
  const hasReport =
    result.artifact.ref !== null &&
    typeof result.artifact.ref === "object" &&
    !!(result.artifact.ref as Record<string, unknown>).report;
  const noun = hasReport ? "报告" : "成果";
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
    <article
      className={styles.delivery}
      data-artifact={result.artifact.artifact_id}
      data-tone={result.acceptances?.at(-1)?.verdict === "changes_required" ? "error" : undefined}
    >
      <div className={styles.sectionHead}>
        <strong>{result.title}</strong>
        <small data-verdict={result.acceptances?.at(-1)?.verdict}>
          {resultStatus(full ? { ...result, ...full } : result)}
        </small>
      </div>
      <div className={styles.deliveryFull}>
        <ArtifactDelivery
          compact={!expanded || !full}
          value={expanded && full ? full.artifact.ref : result.artifact.ref}
        />
      </div>
      {expanded && state.loading && !full && <p role="status">正在读取完整{noun}…</p>}
      {expanded && full && <ArtifactRef value={full.artifact.ref} />}
      {expanded && state.error && (
        <p role="status">
          {full ? `完整${noun}暂时无法刷新。` : `完整${noun}读取失败，当前仍为摘要。`}
          <button onClick={open}>重试</button>
        </p>
      )}
      {reviewExpanded && reviewState.loading && !reviewState.value && (
        <p role="status">正在读取原审阅报告…</p>
      )}
      {reviewExpanded &&
        reviewState.value &&
        reviewState.value.artifact.artifact_id === reviewerArtifact && (
          <section aria-label="原独立审阅报告">
            <strong>独立审阅报告</strong>
            <ArtifactDelivery value={reviewState.value.artifact.ref} />
            <ArtifactRef value={reviewState.value.artifact.ref} />
          </section>
        )}
      {reviewExpanded && reviewState.error && (
        <p role="status">
          原审阅报告读取失败。
          <button
            onClick={() => {
              if (reviewerArtifact)
                void reviewRead.open(`/api/v1/artifacts/${encodeURIComponent(reviewerArtifact)}`);
            }}
          >
            重试
          </button>
        </p>
      )}
      <div className={styles.deliveryActions}>
        {reviewerArtifact && (
          <button className={styles.textLink} onClick={() => setReviewExpanded(!reviewExpanded)}>
            {reviewExpanded ? "收起审阅报告" : "阅读独立审阅报告 →"}
          </button>
        )}
        {!expanded && (
          <button className={styles.textLink} onClick={open}>
            阅读完整{noun} →
          </button>
        )}
        {expanded && full && (
          <button className={styles.textLink} onClick={() => setExpanded(false)}>
            收起{noun}
          </button>
        )}
        <button className={styles.textLink} onClick={() => onTask(result.task_id)}>
          {reviewerArtifact
            ? "任务与审阅记录 →"
            : result.acceptance_policy?.required === false
              ? "任务与结果记录 →"
              : "任务与验收记录 →"}
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

/** Keep one activity card and DOM path while its independently read index arrives. */
export function ActivityResultCard({
  artifactID,
  result,
  onTask,
}: {
  artifactID: string;
  result?: ResultCardValue | undefined;
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
  const indexed = !!result;
  useEffect(() => {
    if (!visible || indexed) return;
    void read.open(`/api/v1/artifacts/${encodeURIComponent(artifactID)}`);
    const stop = live.subscribeEvents((event) => {
      if (event.object_type !== "message") void read.refresh();
    });
    const reset = live.snapshot.subscribe(() => void read.refresh());
    return () => {
      stop();
      reset();
      // Retain the fallback metadata when an index takes over, without retaining IO.
      read.pause();
    };
  }, [visible, indexed, read, artifactID, live]);
  const value = result ?? (state.value ? { ...state.value, title: "交付成果" } : null);
  return (
    <div ref={ref}>
      {value ? (
        <IssueResultCard result={value} onTask={onTask} />
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
