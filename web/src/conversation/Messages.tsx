import { useServices } from "../app/services.tsx";
import { noticeTitle } from "../upgrades/status.ts";
// The transcript's rows: the owner's messages as right-hand bubbles with
// their state below, and what the main agent did for each as full-width
// Markdown with its tool uses.
import { memo } from "react";
import type { AssistantState, ConversationItem } from "../api/generated/api.ts";
import type { OutgoingMessage } from "../queue/outbox.ts";
import type { SendQueue } from "../queue/sendQueue.ts";
import controls from "../shell/controls.module.css";
import { Markdown } from "./Markdown.tsx";
import styles from "./Messages.module.css";
import { clock, dayLabel, duration, messageStateLabels, type Entry } from "./model.ts";
import { ToolRow } from "./ToolRow.tsx";
import { MessageImages, OutgoingImages } from "./Images.tsx";
import { outgoingImages } from "./images.ts";

export function Divider({ at }: { at: number }) {
  return <div className={styles.divider}>{dayLabel(at)}</div>;
}

export function OwnerMessage({ item }: { item: ConversationItem }) {
  const state = item.state ?? "queued";
  return (
    <div className={styles.user} data-scroll-id={item.message_id}>
      {!!item.images?.length && <MessageImages images={item.images} />}
      {item.body && <div className={styles.bubble}>{item.body}</div>}
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
  const images = outgoingImages(message);
  return (
    <div className={styles.user} data-scroll-id={`outbox:${requestId}`}>
      {!!images.length && <OutgoingImages images={images} />}
      {message.body && <div className={styles.bubble}>{message.body}</div>}
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
      {entry.items.map((it) => (
        <AgentItem key={it.message_id} item={it} />
      ))}
      {entry.done && (
        <div className={styles.foot}>
          {clock(entry.done.at)}
          {entry.done.tookMs !== undefined && ` · 用了 ${duration(entry.done.tookMs)}`}
        </div>
      )}
    </div>
  );
}

const assistantLabels: Record<Exclude<AssistantState, "completed">, string> = {
  streaming: "正在生成…",
  interrupted: "回复已停止",
  failed: "回复失败，保留已生成内容",
  unknown: "回复结果未知，保留已生成内容",
};

const AgentItem = memo(function AgentItem({ item }: { item: ConversationItem }) {
  const status = item.assistant_state;
  return (
    <div data-scroll-id={item.message_id}>
      {item.tool ? (
        <ToolRow tool={item.tool} />
      ) : (
        <>
          <Markdown text={item.body ?? ""} />
          {status && status !== "completed" && (
            <div className={styles.assistantState} data-state={status} role="status">
              {assistantLabels[status]}
            </div>
          )}
        </>
      )}
    </div>
  );
});

export function SystemMessage({ item }: { item: ConversationItem }) {
  const { openUpgrade } = useServices();
  const title = noticeTitle(item.notice_type);
  return (
    <section className={styles.system} data-scroll-id={item.message_id} aria-label={`${title}通知`}>
      <div className={styles.systemHead}>
        {title} <time dateTime={item.created_at}>{clock(Date.parse(item.created_at))}</time>
      </div>
      <Markdown text={item.body ?? ""} />
      {item.upgrade_id && (
        <button type="button" onClick={() => openUpgrade(item.upgrade_id!)}>
          查看发布详情
        </button>
      )}
    </section>
  );
}
