import type { DecisionIssue, DecisionView } from "../api/generated/api.ts";
import styles from "./DecisionCard.module.css";

const categories: Record<string, string> = {
  environment: "环境故障",
  evidence: "证据缺口",
  authorization: "授权缺口",
};
const actors: Record<string, string> = {
  worker: "执行 Agent",
  main_agent: "主 Agent",
  owner: "你",
};
const statuses: Record<string, string> = {
  open: "待处理",
  resolved: "已解决",
  workaround: "已有替代方式",
  deferred: "已延期",
};

export function DecisionContext({ decision }: { decision: DecisionView }) {
  const briefing = decision.briefing;
  const issues = briefing?.issues ?? decision.worker?.issues ?? [];
  if (!briefing && issues.length === 0) return null;
  return (
    <section className={styles.context} aria-label="问题与决策说明">
      {briefing && (
        <>
          <p>
            <strong>主 Agent 说明</strong> · 修订 {briefing.revision}
          </p>
          <p>{briefing.summary}</p>
        </>
      )}
      {issues.map((issue: DecisionIssue) => (
        <div className={styles.issue} key={issue.issue_id}>
          <p>
            <strong>{categories[issue.category] ?? issue.category}</strong> ·{" "}
            {statuses[issue.status] ?? issue.status}
          </p>
          <p>{issue.summary}</p>
          <p>影响：{issue.impact}</p>
          <p>
            下一步：{issue.next_action} · 由{actors[issue.actor] ?? issue.actor}处理
          </p>
          <p className={styles.source}>依据：{issue.source}</p>
        </div>
      ))}
      {briefing && (
        <p>
          <strong>需要你决定：</strong>
          {briefing.owner_choice}
        </p>
      )}
    </section>
  );
}
