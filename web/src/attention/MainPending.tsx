import { useEffect, useMemo } from "react";
import { WorkItemRead } from "../workitems/live.ts";
import { Link } from "../app/router.ts";
import { useInboxCount } from "./useInboxCount.ts";
import { AttentionActionTime } from "./ActionTime.tsx";
import type { AttentionPage, IssueView } from "../api/generated/api.ts";
import type { ReadState } from "../workitems/live.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import styles from "./MainPending.module.css";

export function MainPending({ summary }: { summary: ReadState<AttentionPage> }) {
  const { client, live } = useServices();
  const issues = useStore(live.issues.list.state);
  const items =
    summary.value?.items?.filter((item) => item.status === "open" && item.kind !== "information") ??
    [];
  const item = items[0];
  const pending = useInboxCount(summary.value ?? undefined);
  // The sidebar no longer fetches a list just to resolve this one title.
  const read = useMemo(() => new WorkItemRead<IssueView>(client), [client]);
  const issue = useStore(read.state);
  const issueID = item?.issue_id;
  useEffect(() => {
    if (issueID) void read.open(`/api/v1/issues/${encodeURIComponent(issueID)}`);
    return () => read.close();
  }, [read, issueID]);
  if (!item)
    return pending.count > 0 ? (
      <aside className={styles.card} aria-label="当前待处理">
        <strong>有 {pending.count} 项需要你处理</strong>
        <Link className={styles.action} to="/inbox">
          查看并处理
        </Link>
      </aside>
    ) : null;
  return (
    <aside className={styles.card} aria-label="当前待处理">
      <div>
        <small>{item.kind === "decision" ? "需要你决定" : "需要你处理"}</small>
        <strong>{item.title}</strong>
        <small>
          <AttentionActionTime item={item} />
        </small>
        {item.issue_id && (
          <Link className={styles.issue} to={{ page: "issue", id: item.issue_id }}>
            所属 Issue：
            {(issue.value?.issue_id === item.issue_id ? issue.value.plan.title : undefined) ||
              issues.value?.items?.find((i) => i.issue_id === item.issue_id)?.title ||
              "所属 Issue"}
          </Link>
        )}
      </div>
      <Link className={styles.action} to={{ page: "attention", id: item.attention_id }}>
        查看并处理
      </Link>
      {(pending.count > 1 || pending.more) && (
        <Link className={styles.more} to="/inbox">
          查看其余待处理{summary.value?.more ? "（有更多）" : `（${pending.count - 1}）`}
        </Link>
      )}
    </aside>
  );
}
