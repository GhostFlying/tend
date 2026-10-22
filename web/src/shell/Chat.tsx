// The one conversation with the main agent. Until the conversation view
// arrives (F7) it shows the outbox: what this device wrote and whether
// tend has it.
import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import type { Connection } from "../api/client.ts";
import type { OutgoingMessage } from "../queue/outbox.ts";
import type { SendQueue } from "../queue/sendQueue.ts";
import { Store, useStore } from "../state/store.ts";
import styles from "./Chat.module.css";
import { ConnectionBadge } from "./ConnectionBadge.tsx";
import controls from "./controls.module.css";
import { ArrowDown, Send } from "./icons.tsx";

const noMessages = new Store<readonly OutgoingMessage[]>([]);

const emptyNote =
  "这里是和主 Agent 的对话。发出的消息先存在这台设备上，tend 收到后才算发出；" +
  "断网或关掉页面也不会丢。";

export function Chat({ connection, queue }: { connection: Connection; queue: SendQueue | null }) {
  const messages = useStore(queue?.list ?? noMessages);
  const end = useRef<HTMLDivElement>(null);
  const toLatest = () => end.current?.scrollIntoView({ block: "end", behavior: "smooth" });

  const count = messages.length;
  useEffect(() => {
    end.current?.scrollIntoView({ block: "end" });
  }, [count]);

  return (
    <>
      <header className={styles.head}>
        <h1>主 Agent</h1>
        <span className={styles.sub}>一个会话，所有设备共享</span>
        <span className={styles.spacer} />
        <span className={styles.mobileOnly}>
          <ConnectionBadge connection={connection} compact />
        </span>
        <button
          type="button"
          className={controls.iconBtn}
          onClick={toLatest}
          title="跳到最新"
          aria-label="跳到最新"
        >
          <ArrowDown />
        </button>
      </header>
      <div className={styles.transcript}>
        <div className={styles.lane}>
          {count === 0 ? (
            <p className={styles.empty}>{emptyNote}</p>
          ) : (
            messages.map((m) => <Outgoing key={m.requestId} message={m} queue={queue} />)
          )}
          <div ref={end} />
        </div>
      </div>
      <div className={styles.seat}>
        <div className={styles.seatInner}>
          <Composer connection={connection} queue={queue} />
        </div>
      </div>
    </>
  );
}

function Outgoing({ message, queue }: { message: OutgoingMessage; queue: SendQueue | null }) {
  const { requestId } = message;
  return (
    <div className={styles.user}>
      <div className={styles.bubble}>{message.body}</div>
      <div className={styles.state} data-state={message.state}>
        {message.state === "sending" && "发送中…"}
        {message.state === "sent" && "已收下"}
        {message.state === "unknown" && (
          <>
            发送结果未知 ·{" "}
            <button
              type="button"
              className={controls.link}
              onClick={() => queue?.reconcile(requestId)}
            >
              查看
            </button>{" "}
            ·{" "}
            <button
              type="button"
              className={controls.link}
              onClick={() => queue?.resend(requestId)}
            >
              重发
            </button>
          </>
        )}
        {message.state === "failed" && (
          <>
            发送失败{message.error ? `：${message.error}` : ""} ·{" "}
            <button
              type="button"
              className={controls.link}
              onClick={() => queue?.discard(requestId)}
            >
              删除
            </button>
          </>
        )}
      </div>
    </div>
  );
}

function Composer({ connection, queue }: { connection: Connection; queue: SendQueue | null }) {
  const [draft, setDraft] = useState("");
  const blocked = queue === null || connection.status === "not-owner";
  const body = draft.trim();

  const send = async () => {
    if (queue === null || blocked || body === "") {
      return;
    }
    setDraft("");
    try {
      await queue.enqueue({ body });
    } catch {
      // Not written down, so not sent either; give the text back.
      setDraft(draft);
    }
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // Enter sends on a keyboard; on a touch screen it starts a new line.
    if (
      e.key === "Enter" &&
      !e.shiftKey &&
      !e.nativeEvent.isComposing &&
      !window.matchMedia("(pointer: coarse)").matches
    ) {
      e.preventDefault();
      void send();
    }
  };

  let hint = "";
  if (queue === null) {
    hint = "这个浏览器不能保存待发送的消息，暂时不能发送";
  } else if (connection.status === "offline") {
    hint = "离线：消息先存在这台设备上，连上后自动发送";
  }

  return (
    <div className={styles.composer}>
      <textarea
        rows={2}
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder="和主 Agent 说…（Enter 发送，Shift+Enter 换行）"
        aria-label="消息"
        disabled={queue === null}
      />
      <div className={styles.bar}>
        <span className={styles.hint}>{hint}</span>
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
      </div>
    </div>
  );
}
