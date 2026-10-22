// The message box. A message goes into the outbox with what the owner saw
// when writing it: the reply it answers and the last item shown (v0.1
// §6.2). While a reply is in progress and nothing is written, the send
// button stops the reply instead (D69).
import { useEffect, useMemo, useState, type KeyboardEvent } from "react";
import type { Connection } from "../api/client.ts";
import { notify } from "../app/notice.ts";
import { Interrupt } from "./interrupt.ts";
import { useServices } from "../app/services.tsx";
import { Send, Stop } from "../shell/icons.tsx";
import { useStore } from "../state/store.ts";
import styles from "./Composer.module.css";
import { clock, latestReply, processing } from "./model.ts";

/** A touch screen, where Enter starts a new line and the button sends. */
const touch = window.matchMedia("(pointer: coarse)").matches;

export function Composer({ connection }: { connection: Connection }) {
  const { client, live, queue } = useServices();
  const conversation = useStore(live.conversation);
  const [draft, setDraft] = useState("");
  const interrupt = useMemo(() => new Interrupt(client), [client]);
  const pending = useStore(interrupt.state);
  const blocked = queue === null || connection.status === "not-owner";
  const body = draft.trim();
  const reply = latestReply(conversation.items);
  const busy = processing(conversation.items);

  useEffect(() => {
    interrupt.observe(conversation.items);
  }, [interrupt, conversation.items]);

  useEffect(() => {
    if (pending?.phase === "failed") notify(`没能停止回复：${pending.error}`, "warn");
  }, [pending?.phase, pending?.error]);

  useEffect(() => {
    if (connection.status !== "online") return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let delay = 1_000;
    const recover = async () => {
      await interrupt.resume();
      if (!cancelled && interrupt.state.get()?.phase === "unconfirmed") {
        timer = setTimeout(() => void recover(), delay);
        delay = Math.min(delay * 2, 30_000);
      }
    };
    void recover();
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [interrupt, connection.status, pending?.envelope.request_id]);

  const send = async () => {
    if (queue === null || blocked || body === "") {
      return;
    }
    setDraft("");
    const lastSeq = conversation.items.at(-1)?.seq;
    try {
      await queue.enqueue({
        body,
        ...(reply ? { replyTo: reply.message_id } : {}),
        ...(lastSeq ? { lastSeenSeq: lastSeq } : {}),
      });
    } catch {
      // Not written down, so not sent either; give the text back.
      setDraft(draft);
    }
  };

  const stopping =
    pending !== null && pending.messageId === busy?.message_id && pending.phase !== "failed";
  const stop = async () => {
    if (!busy || stopping) return;
    await interrupt.start(busy.message_id);
    void live.reload();
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // Enter sends on a keyboard; on a touch screen it starts a new line.
    if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing && !touch) {
      e.preventDefault();
      void send();
    }
  };

  let hint = "";
  if (queue === null) {
    hint = "这个浏览器不能保存待发送的消息，暂时不能发送";
  } else if (busy && stopping) {
    hint =
      pending?.phase === "unknown"
        ? "停止结果未知，等待消息的最终状态"
        : (pending?.error ?? "正在停止…");
  } else if (connection.status === "offline") {
    hint = "离线：消息先存在这台设备上，连上后自动发送";
  } else if (reply) {
    hint = `回复 ${clock(Date.parse(reply.created_at))} 的消息`;
  }

  const showStop = busy !== undefined && body === "";
  return (
    <div className={styles.composer}>
      <textarea
        rows={2}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder={touch ? "和主 Agent 说…" : "和主 Agent 说…（Enter 发送，Shift+Enter 换行）"}
        aria-label="消息"
        disabled={queue === null}
      />
      <div className={styles.bar}>
        <span className={styles.hint}>{hint}</span>
        {showStop ? (
          <button
            type="button"
            className={`${styles.send} ${styles.stop}`}
            onClick={() => void stop()}
            disabled={stopping}
            title="停止回复"
            aria-label="停止回复"
          >
            <Stop />
          </button>
        ) : (
          <button
            type="button"
            className={styles.send}
            onClick={() => void send()}
            disabled={blocked || body === ""}
            title="发送"
            aria-label="发送"
          >
            <Send />
          </button>
        )}
      </div>
    </div>
  );
}
