import { Link } from "../app/router.ts";
import { useInboxCount } from "./useInboxCount.ts";
import { AttentionActionTime } from "./ActionTime.tsx";
import type { AttentionPage } from "../api/generated/api.ts";
import type { ReadState } from "../workitems/live.ts";
import styles from "./MainPending.module.css";

export function MainPending({ summary }: { summary: ReadState<AttentionPage> }) {
  const items =
    summary.value?.items?.filter((item) => item.status === "open" && item.kind !== "information") ??
    [];
  const item = items[0];
  const pending = useInboxCount(summary.value ?? undefined);
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
            {item.issue_title || "所属 Issue 名称暂不可用"}
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
