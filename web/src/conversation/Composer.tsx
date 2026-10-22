// The message box. A message goes into the outbox with what the owner saw
// when writing it: the reply it answers and the last item shown (v0.1
// §6.2). While a reply is in progress and nothing is written, the send
// button stops the reply instead (D69).
import { useState, type KeyboardEvent } from "react";
import type { Connection } from "../api/client.ts";
import { notify } from "../app/notice.ts";
import { runOp } from "../app/ops.ts";
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
  const [stopping, setStopping] = useState<string | null>(null);
  const blocked = queue === null || connection.status === "not-owner";
  const body = draft.trim();
  const reply = latestReply(conversation.items);
  const busy = processing(conversation.items);

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

  const stop = async () => {
    if (!busy || stopping === busy.message_id) {
      return;
    }
    setStopping(busy.message_id);
    const outcome = await runOp(client, {
      operation: "conversation.interrupt_reply",
      target: { type: "message", id: busy.message_id },
      payload: { message_id: busy.message_id },
    });
    if (outcome.kind !== "done") {
      setStopping(null);
      notify(`没能停止回复：${outcome.message}`, "warn");
      return;
    }
    // The message's state shows when the turn stops; if it has not after
    // a while, the button can be pressed again.
    setTimeout(() => setStopping(null), 15_000);
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
  } else if (connection.status === "offline") {
    hint = "离线：消息先存在这台设备上，连上后自动发送";
  } else if (busy && stopping === busy.message_id) {
    hint = "正在停止…";
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
            disabled={stopping === busy.message_id}
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
