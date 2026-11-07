import { useEffect, useMemo, useState } from "react";
import type { SharedDeliveryReview, SharedDeliveryReviewPage } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { CommitHash } from "../i18n/identifiers.tsx";
import { Markdown } from "../conversation/Markdown.tsx";
import { useStore } from "../state/store.ts";
import { WorkItemRead } from "../workitems/live.ts";
import { AssigneeName } from "./AssigneeName.tsx";
import styles from "./Issues.module.css";

const statusLabels: Record<string, string> = {
  pending: "待安排审阅",
  reviewing: "正在审阅",
  waiting: "等待审阅证据",
  changes_requested: "待集中处理审阅意见",
  passed: "审阅通过",
  escalated: "已交上级负责人判断",
};

export function sharedReviewStatus(review: Pick<SharedDeliveryReview, "status" | "valid">): string {
  if (review.status === "passed" && !review.valid) return "原审阅结论待核对当前适用性";
  return statusLabels[review.status] || review.status;
}

export function IssueSharedReviews({ issueID }: { issueID: string }) {
  const { client, live } = useServices();
  const read = useMemo(() => new WorkItemRead<SharedDeliveryReviewPage>(client), [client]);
  const state = useStore(read.state);
  const [cursors, setCursors] = useState([""]);
  const cursor = cursors.at(-1)!;
  useEffect(() => {
    const query = new URLSearchParams({ limit: "50" });
    if (cursor) query.set("after", cursor);
    void read.open(`/api/v1/issues/${encodeURIComponent(issueID)}/delivery-reviews?${query}`);
    const stop = live.subscribeEvents((event) => {
      if (event.object_id === issueID || event.kind.includes("delivery")) void read.refresh();
    });
    const reset = live.snapshot.subscribe(() => void read.refresh());
    return () => {
      stop();
      reset();
      read.close();
    };
  }, [issueID, cursor, read, live]);
  return (
    <section aria-label="共享 PR 审阅">
      <h3>PR 审阅</h3>
      <p className={styles.muted}>同一 PR 共用审阅结果；候选或需求变化后由负责人核对适用性。</p>
      {state.error && (
        <p role="status" className={styles.warning}>
          审阅记录读取失败。<button onClick={() => void read.refresh()}>重新读取</button>
        </p>
      )}
      {!state.value && !state.error && <p role="status">正在读取审阅记录…</p>}
      {(state.value?.items ?? []).map((review) => (
        <ReviewRecord key={review.review_id} review={review} />
      ))}
      {state.value && !state.value.items?.length && (
        <p className={styles.muted}>暂无共享 PR 审阅记录。</p>
      )}
      {cursors.length > 1 && (
        <button onClick={() => setCursors((old) => old.slice(0, -1))}>上一页审阅</button>
      )}
      {state.value?.more && state.value.next && (
        <button onClick={() => setCursors((old) => [...old, state.value!.next!])}>
          下一页审阅
        </button>
      )}
    </section>
  );
}

function ReviewRecord({ review }: { review: SharedDeliveryReview }) {
  const { openTask, openIssue } = useServices();
  const latest = review.rounds?.at(-1);
  return (
    <article className={styles.taskPeek}>
      <h4>
        {review.repository} · #{review.number}
      </h4>
      <p>{sharedReviewStatus(review)}</p>
      {review.reviewer && (
        <p>
          审阅者：
          <AssigneeName assignee={{ kind: "agent", ref: review.reviewer }} />
        </p>
      )}
      <p className={styles.muted}>
        候选 <CommitHash value={review.head_sha} /> · 已安排 {review.rounds?.length ?? 0} 轮
      </p>
      {review.reason && <Markdown text={review.reason} />}
      {latest?.result?.findings?.length ? (
        <ul>
          {latest.result.findings.map((finding) => (
            <li key={finding.id}>
              <strong>{finding.priority}</strong>
              <Markdown text={finding.body} />
              {review.dispositions
                ?.filter((item) => item.finding_id === finding.id)
                .map((item) => (
                  <p key={item.finding_id}>
                    处置：
                    {item.action === "fixed"
                      ? "已修复"
                      : item.action === "not_adopted"
                        ? "不采纳"
                        : item.action}{" "}
                    · {item.reason}
                  </p>
                ))}
            </li>
          ))}
        </ul>
      ) : null}
      <details>
        <summary>查看审阅过程与关联</summary>
        {(review.budget_authorizations ?? []).map((authorization) => (
          <p key={authorization.request_id}>
            额外复查授权 · 第 {authorization.round} 轮 · {authorization.reason}
          </p>
        ))}
        {(review.rounds ?? []).map((round) => (
          <p key={round.number}>
            第 {round.number} 轮 ·{" "}
            {round.reviewer_role === "accept" || round.acceptance_id
              ? "验收中的整 PR 审阅"
              : "报告任务审阅"}{" "}
            ·{" "}
            <button onClick={() => openTask(round.task_id)}>查看实际审阅任务</button>
            {round.supplement_task_id && (
              <>
                {" "}
                ·{" "}
                <button onClick={() => openTask(round.supplement_task_id!)}>
                  查看原审阅者的补证任务
                </button>
              </>
            )}
            {round.result ? ` · ${round.result.verdict}` : ""}
          </p>
        ))}
        {(review.links ?? []).map((link) => (
          <p key={`${link.issue_id}:${link.delivery_id}:${link.task_id ?? ""}`}>
            <button onClick={() => openIssue(link.issue_id)}>查看关联 Issue</button>
            {link.task_id && (
              <>
                {" "}
                · <button onClick={() => openTask(link.task_id!)}>查看关联任务</button>
              </>
            )}
          </p>
        ))}
      </details>
      {review.escalation_issue_id && (
        <button onClick={() => openIssue(review.escalation_issue_id!)}>
          查看承接判断的上级 Issue
        </button>
      )}
    </article>
  );
}
