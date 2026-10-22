// The transcript's rows: the owner's messages as right-hand bubbles with
// their state below, and what the main agent did for each as full-width
// Markdown with its tool uses.
import type { ConversationItem } from "../api/generated/api.ts";
import type { OutgoingMessage } from "../queue/outbox.ts";
import type { SendQueue } from "../queue/sendQueue.ts";
import controls from "../shell/controls.module.css";
import { Markdown } from "./Markdown.tsx";
import styles from "./Messages.module.css";
import { clock, dayLabel, duration, messageStateLabels, type Entry } from "./model.ts";
import { ToolRow } from "./ToolRow.tsx";

export function Divider({ at }: { at: number }) {
  return <div className={styles.divider}>{dayLabel(at)}</div>;
}

export function OwnerMessage({ item }: { item: ConversationItem }) {
  const state = item.state ?? "queued";
  return (
    <div className={styles.user}>
      <div className={styles.bubble}>{item.body}</div>
      <div className={styles.state} data-state={state}>
        {messageStateLabels[state]} · {clock(Date.parse(item.created_at))}
      </div>
    </div>
  );
}

/** A message of this device that tend has not shown in the conversation yet. */
export function OutgoingRow({
  message,
  queue,
}: {
  message: OutgoingMessage;
  queue: SendQueue | null;
}) {
  const { requestId } = message;
  return (
    <div className={styles.user}>
      <div className={styles.bubble}>{message.body}</div>
      <div className={styles.state} data-state={message.state}>
        {message.state === "sending" && "发送中…"}
        {message.state === "sent" && `已收下 · ${clock(message.createdAt)}`}
        {message.state === "unknown" && (
          <>
            发送结果未知 ·{" "}
            <button
              type="button"
              className={controls.link}
              onClick={() => void queue?.reconcile(requestId)}
            >
              查看
            </button>{" "}
            ·{" "}
            <button
              type="button"
              className={controls.link}
              onClick={() => void queue?.resend(requestId)}
            >
              重发
            </button>{" "}
            ·{" "}
            <button
              type="button"
              className={controls.link}
              onClick={() => void queue?.discard(requestId)}
            >
              删除
            </button>
          </>
        )}
        {message.state === "failed" && (
          <>
            发送失败{message.error ? `：${message.error}` : ""} ·{" "}
            <button
              type="button"
              className={controls.link}
              onClick={() => void queue?.discard(requestId)}
            >
              删除
            </button>
          </>
        )}
      </div>
    </div>
  );
}

export function AgentTurn({ entry }: { entry: Extract<Entry, { kind: "agent" }> }) {
  return (
    <div className={styles.agent}>
      {entry.items.map((it) =>
        it.tool ? (
          <ToolRow key={it.message_id} tool={it.tool} />
        ) : (
          <Markdown key={it.message_id} text={it.body ?? ""} />
        ),
      )}
      {entry.done && (
        <div className={styles.foot}>
          {clock(entry.done.at)}
          {entry.done.tookMs !== undefined && ` · 用了 ${duration(entry.done.tookMs)}`}
        </div>
      )}
    </div>
  );
}
