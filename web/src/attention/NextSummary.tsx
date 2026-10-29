import { AttentionActionTime } from "./ActionTime.tsx";
import type { AttentionPage } from "../api/generated/api.ts";
import type { ReadState } from "../workitems/live.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import styles from "./Inbox.module.css";

export function NextSummary({
  onOpen,
  summary,
  onIssue,
}: {
  onOpen(id?: string): void;
  summary: ReadState<AttentionPage>;
  onIssue(id: string): void;
}) {
  const { live } = useServices();
  const state = useStore(live.attention.next.state);
  const issues = useStore(live.issues.list.state);

  const items = state.value?.items ?? [];
  const actions = items.filter(
    (item) => item.kind === "user_action" && ["open", "blocked"].includes(item.status),
  );
  const attentions = (summary.value?.items ?? []).filter(
    (item) => item.status === "open" && item.kind !== "information",
  );
  const extra = actions.filter(
    (item) => !item.attention_id || !attentions.some((a) => a.attention_id === item.attention_id),
  );
  const risks = items.filter((item) => item.status === "blocked");
  return (
    <section aria-label="需要你处理" className={styles.summary}>
      <button
        type="button"
        className={`${styles.muted} ${styles.summaryTitle}`}
        aria-label="打开待处理事项"
        onClick={() => onOpen()}
      >
        需要你处理
      </button>
      {!summary.value && (
        <p className={styles.muted}>{summary.error ? "暂时无法读取，保留原状态。" : "正在读取…"}</p>
      )}
      {summary.error && summary.value && (
        <p className={styles.muted}>暂时无法更新，显示上次读取。</p>
      )}
      {summary.value && attentions.length === 0 && extra.length === 0 && (
        <p className={styles.muted}>没有需要你处理的事。</p>
      )}
      {attentions.map((item) => {
        const plan = actions.find((entry) => entry.attention_id === item.attention_id);
        return (
          <div key={item.attention_id}>
            <button onClick={() => onOpen(item.attention_id)}>
              <strong>{item.title}</strong>
              <small>{item.kind === "decision" ? "待决策" : "待行动"} · 处理 →</small>
              <small>
                <AttentionActionTime item={item} />
              </small>
              <small>
                {plan?.expected_at
                  ? `计划处理：${new Date(plan.expected_at).toLocaleString("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" })}`
                  : "计划处理未定"}
              </small>
            </button>
            {item.issue_id && (
              <button className={styles.issueLink} onClick={() => onIssue(item.issue_id!)}>
                所属Issue：
                {issues.value?.items?.find((i) => i.issue_id === item.issue_id)?.title ||
                  item.issue_id}
              </button>
            )}
          </div>
        );
      })}
      {summary.value?.more && <button onClick={() => onOpen()}>查看更多待处理</button>}
      {extra.map((item) => (
        <button key={item.entry_id} onClick={() => onOpen(item.attention_id)}>
          {item.expected_at !== undefined && (
            <small>
              {new Date(item.expected_at).toLocaleString("zh-CN", {
                month: "numeric",
                day: "numeric",
                hour: "2-digit",
                minute: "2-digit",
              })}
            </small>
          )}
          <strong>{item.title}</strong>
          <small>{item.reason}</small>
        </button>
      ))}
      {!!state.value?.deadlines?.length && (
        <div className={styles.riskSummary}>
          <small>近期约定</small>
          {state.value.deadlines.map((deadline) => (
            <button key={deadline.deadline_id} onClick={() => onIssue(deadline.issue_id)}>
              <strong>{deadline.title}</strong>
              <small>
                {deadline.due_at
                  ? `截止：${new Date(deadline.due_at).toLocaleString("zh-CN")}`
                  : "截止时间未定"}
              </small>
              {deadline.latest_start_at !== undefined && (
                <small>
                  最晚开始：{new Date(deadline.latest_start_at).toLocaleString("zh-CN")}
                </small>
              )}
              {deadline.source_needs_review && <small>来源已更新，约定待核对</small>}
            </button>
          ))}
        </div>
      )}
      {risks.length > 0 && (
        <div className={styles.riskSummary}>
          <small>近期风险</small>
          {risks.map((item) => (
            <button key={item.entry_id} onClick={() => onOpen(item.attention_id)}>
              <strong>{item.title}</strong>
            </button>
          ))}
        </div>
      )}
    </section>
  );
}
