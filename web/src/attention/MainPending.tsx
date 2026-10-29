import { AttentionActionTime } from "./ActionTime.tsx";
import type { AttentionPage } from "../api/generated/api.ts";
import type { ReadState } from "../workitems/live.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import styles from "./MainPending.module.css";

export function MainPending({
  summary,
  onOpen,
  onIssue,
}: {
  summary: ReadState<AttentionPage>;
  onOpen(id?: string): void;
  onIssue(id: string): void;
}) {
  const { live } = useServices();
  const issues = useStore(live.issues.list.state);
  const items =
    summary.value?.items?.filter((item) => item.status === "open" && item.kind !== "information") ??
    [];
  const item = items[0];
  if (!item) return null;
  return (
    <aside className={styles.card} aria-label="当前待处理">
      <div>
        <small>{item.kind === "decision" ? "需要你决定" : "需要你处理"}</small>
        <strong>{item.title}</strong>
        <small>
          <AttentionActionTime item={item} />
        </small>
        {item.issue_id && (
          <button className={styles.issue} onClick={() => onIssue(item.issue_id!)}>
            所属Issue：
            {issues.value?.items?.find((i) => i.issue_id === item.issue_id)?.title || item.issue_id}
          </button>
        )}
      </div>
      <button className={styles.action} onClick={() => onOpen(item.attention_id)}>
        查看并处理
      </button>
      {(items.length > 1 || summary.value?.more) && (
        <button className={styles.more} onClick={() => onOpen()}>
          查看其余待处理{summary.value?.more ? "（有更多）" : `（${items.length - 1}）`}
        </button>
      )}
    </aside>
  );
}
