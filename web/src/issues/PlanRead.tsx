import { CommitHash } from "../i18n/identifiers.tsx";
import type { IssueEvidence, IssueView } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { Markdown } from "../conversation/Markdown.tsx";
import { issueStateLabel } from "./Hierarchy.tsx";
import {
  factLabels,
  stageLabels,
  followOnLabels,
  closureLabels,
  evidenceLabel,
  deliveryLabels,
} from "../workitems/model.ts";
import styles from "./Issues.module.css";

/** Read only the selected snapshot, including facts absent from newer revisions. */
export function IssuePlanRead({
  issue,
  showIntent = false,
}: {
  issue: IssueView;
  showIntent?: boolean;
}) {
  const { openTask } = useServices();
  const plan = issue.plan;
  return (
    <>
      <p className={styles.muted}>
        项目：{plan.project || "未分组"} · {issueStateLabel[issue.state]}
      </p>
      {showIntent && (
        <>
          <h3>目标</h3>
          <Markdown text={plan.intent} />
        </>
      )}
      <h3>范围</h3>
      <Markdown text={plan.scope || "该版本未记录范围。"} />
      <h3>完成条件</h3>
      {plan.completion_criteria?.length ? (
        <ul>
          {plan.completion_criteria.map((value, index) => (
            <li key={index}>{value}</li>
          ))}
        </ul>
      ) : (
        <p className={styles.muted}>该版本未记录完成条件。</p>
      )}
      <h3>背景与约束</h3>
      {(plan.background ?? []).map((fact, index) => (
        <section key={index}>
          <Markdown text={fact.text} />
          <small className={styles.muted}>
            {factLabels[fact.kind] ?? fact.kind} · {fact.source}
          </small>
        </section>
      ))}
      {!plan.background?.length && <p className={styles.muted}>该版本未记录背景与约束。</p>}
      <details>
        <summary>阶段与关联任务（{plan.stages?.length ?? 0}）</summary>
        {(plan.stages ?? []).map((stage) => (
          <section key={stage.stage_id}>
            <h4>
              {stage.title} · {stageLabels[stage.status] ?? stage.status}
            </h4>
            {stage.reason && <Markdown text={stage.reason} />}
            <p>
              阶段：{stage.stage_id}
              {stage.parent_id ? ` · 上级：${stage.parent_id}` : ""}
            </p>
            <p>关联任务：{stage.task_ids?.join("、") || "未记录"}</p>
            <p>前置：{stage.depends_on?.join("、") || "未记录"}</p>
            <IssueEvidenceRead items={stage.evidence} />
          </section>
        ))}
      </details>
      <details>
        <summary>登记交付（{plan.deliveries?.length ?? 0}）</summary>
        {(plan.deliveries ?? []).map((delivery) => (
          <p key={delivery.delivery_id}>
            {delivery.provider} · {delivery.repository} #{delivery.number} ·{" "}
            {delivery.url || "未记录链接"}
            {delivery.host_id ? ` · ${delivery.host_id}` : ""}
          </p>
        ))}
      </details>
      <h3>后续事项</h3>
      {(plan.follow_ons ?? []).map((follow) => (
        <section key={follow.follow_on_id}>
          <p>
            {followOnLabels[follow.kind] ?? follow.kind} ·{" "}
            {followOnLabels[follow.status] ?? follow.status}
          </p>
          <Markdown text={follow.text} />
          <small className={styles.muted}>
            来源：{follow.source}
            {follow.transferred_to ? ` · 承接：${follow.transferred_to}` : ""}
          </small>
          <IssueEvidenceRead items={follow.evidence} />
        </section>
      ))}
      {!plan.follow_ons?.length && <p className={styles.muted}>该版本未记录后续事项。</p>}
      <h3>收尾记录</h3>
      {issue.closure ? (
        <>
          <p>{closureLabels[issue.closure.kind] ?? issue.closure.kind}</p>
          <Markdown text={issue.closure.reason} />
          <IssueEvidenceRead items={issue.closure.evidence} />
        </>
      ) : (
        <p className={styles.muted}>该版本未记录收尾。</p>
      )}
      <h3>默认工作区来源任务</h3>
      {plan.default_workspace_task_id ? (
        <button className={styles.button} onClick={() => openTask(plan.default_workspace_task_id!)}>
          查看来源任务
        </button>
      ) : (
        <p className={styles.muted}>该版本未记录来源任务。</p>
      )}
    </>
  );
}

export function IssueEvidenceRead({
  items,
}: {
  items: readonly IssueEvidence[] | null | undefined;
}) {
  if (!items?.length) return null;
  return (
    <details>
      <summary>留存依据 · {items.length}</summary>
      {items.map((item, index) => (
        <section key={index}>
          <p>
            {evidenceLabel(item.kind)} · {item.reference}
          </p>
          <Markdown text={item.summary} />
          {item.observation && (
            <div className={styles.muted}>
              <p>
                观察：{deliveryLabels[item.observation.state] ?? item.observation.state} · 代次
                {item.observation.generation}
              </p>
              {item.observation.checked_at && (
                <p>{new Date(item.observation.checked_at).toLocaleString("zh-CN")}</p>
              )}
              {item.observation.head_sha && (
                <p>
                  Head：
                  <CommitHash value={item.observation.head_sha} />
                </p>
              )}
              {item.observation.merge_sha && (
                <p>
                  Merge：
                  <CommitHash value={item.observation.merge_sha} />
                </p>
              )}
              {item.observation.error_kind && <p>观察错误：{item.observation.error_kind}</p>}
              <details>
                <summary>观察原始记录</summary>
                <pre>{JSON.stringify(item.observation, null, 2)}</pre>
              </details>
            </div>
          )}
          {item.upgrade && (
            <details>
              <summary>升级留存记录</summary>
              <pre>{JSON.stringify(item.upgrade, null, 2)}</pre>
            </details>
          )}
        </section>
      ))}
    </details>
  );
}
