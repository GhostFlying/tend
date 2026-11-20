import { useState } from "react";
import type { IssueActivityEntry } from "../api/generated/api.ts";
import { DeliveryProgress } from "./DeliveryProgress.tsx";
import { EventNotification } from "./EventNotification.tsx";
import styles from "./DetailPage.module.css";

export function DeliveryCandidateActivity({
  anchor,
  entries,
  newCandidate,
}: {
  anchor: string;
  entries: IssueActivityEntry[];
  newCandidate: boolean;
}) {
  const [historyOpen, setHistoryOpen] = useState(false);
  const delivery = entries.at(-1)!.delivery!;
  return (
    <article className={styles.milestone} data-delivery-candidate>
      <div data-scroll-id={`delivery-candidate:${anchor}`}>
        <DeliveryProgress item={delivery} presentation="candidate" newCandidate={newCandidate} />
      </div>
      <details
        className={styles.deliveryHistory}
        onToggle={(event) => setHistoryOpen(event.currentTarget.open)}
      >
        <summary>查看 {entries.length} 条观察记录</summary>
        {historyOpen &&
          entries.map((entry) => <DeliveryObservationRow key={entry.id} entry={entry} />)}
      </details>
    </article>
  );
}

function DeliveryObservationRow({ entry }: { entry: IssueActivityEntry }) {
  const [open, setOpen] = useState(false);
  const issueID = entry.issue_id || entry.delivery!.observation.issue_id;
  const time = new Date(entry.created_at);
  return (
    <details data-scroll-id={entry.id} onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>
        <time dateTime={time.toISOString()}>{time.toLocaleString("zh-CN")}</time>
        {" · "}
        {entry.title}
      </summary>
      {open && (
        <>
          <DeliveryProgress item={entry.delivery!} presentation="observation" />
          {issueID && entry.kind === "work_item.delivery_changed" && (
            <EventNotification issueID={issueID} position={entry.position} />
          )}
        </>
      )}
    </details>
  );
}
