import type { WorkerProcessText } from "../api/generated/api.ts";
import styles from "./ProcessText.module.css";

export function ProcessText({
  parts,
  title,
}: {
  parts: readonly WorkerProcessText[] | undefined;
  title: string;
}) {
  if (!parts?.length) return null;
  return (
    <details className={styles.text}>
      <summary>{title}</summary>
      {parts.map((part) => (
        <div key={JSON.stringify([part.thread_id, part.turn_id, part.item_id])}>
          <p>{part.text}</p>
          {part.truncated && <small>已采集片段到此为止</small>}
        </div>
      ))}
    </details>
  );
}
