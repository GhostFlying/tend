// The message box. A message goes into the outbox with what the owner saw
// when writing it: the reply it answers and the last item shown (v0.1
// §6.2). While a reply is in progress and nothing is written, the send
// button stops the reply instead (D69).
import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import type { Snapshot } from "../api/generated/api.ts";
import { MessageAttachments } from "./Attachments.tsx";
import type { Connection } from "../api/client.ts";
import { notify } from "../app/notice.ts";
import { ContextUsage } from "./ContextUsage.tsx";
import { Settings } from "./Settings.tsx";
import { Interrupt } from "./interrupt.ts";
import { useServices } from "../app/services.tsx";
import { Send, Stop } from "../shell/icons.tsx";
import { useStore } from "../state/store.ts";
import styles from "./Composer.module.css";
import { clock, latestReply, processing } from "./model.ts";
import { LocalAttachment } from "./Attachments.tsx";
import { Close } from "../shell/icons.tsx";

/** A touch screen, where Enter starts a new line and the button sends. */
const touch = window.matchMedia("(pointer: coarse)").matches;

export function Composer({ connection }: { connection: Connection }) {
  const { client, live, queue, conversation: recipient, draft: composerDraft } = useServices();
  const conversation = useStore(live.conversation);
  const queueState = useStore(live.queueView.state);
  const {
    text: draft,
    files,
    focus,
    attachments,
    replyTo,
    source,
    loaded,
    error: draftError,
  } = useStore(composerDraft.state);
  const textarea = useRef<HTMLTextAreaElement>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const saving = useRef(false);
  const [persisting, setPersisting] = useState(false);
  const [dragging, setDragging] = useState(false);
  const [confirmedChoice, setConfirmedChoice] = useState<string | null>(null);
  const choiceKey = source?.decision ? JSON.stringify([source.message_id, source.decision]) : null;
  const confirmChoice = choiceKey !== null && confirmedChoice === choiceKey;
  useEffect(() => {
    if (!focus) return;
    const frame = requestAnimationFrame(() => {
      const el = textarea.current;
      el?.focus();
      el?.setSelectionRange(el.value.length, el.value.length);
    });
    return () => cancelAnimationFrame(frame);
  }, [focus]);
  const interrupt = useMemo(() => new Interrupt(client), [client]);
  const pending = useStore(interrupt.state);
  const blocked = queue === null || connection.status === "not-owner" || !loaded;
  const body = draft.trim();
  const reply = latestReply(conversation.items);
  const active = queueState.pages.owner?.active;
  const busy =
    active?.status === "processing"
      ? active.item
      : queueState.supported
        ? Object.values(queueState.pages)
            .flatMap((p) => p.items ?? [])
            .find((e) => e.status === "processing")?.item
        : processing(conversation.items);

  const queued = !!queueState.pages.owner?.total || !!queueState.pages.background?.total;

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
    if (
      queue === null ||
      blocked ||
      saving.current ||
      (!body && !files.length && !attachments.length)
    ) {
      return;
    }
    saving.current = true;
    setPersisting(true);

    const lastSeq = conversation.items.at(-1)?.seq;
    try {
      let decision = source?.decision;
      if (decision) {
        if (!confirmChoice) {
          notify("请确认取回消息中的原选择，或清除它再发送", "warn");
          return;
        }
        const snapshot = await client.get<Snapshot>("/api/v1/snapshot");
        const candidate = decision;
        const current = snapshot.decisions?.find((d) => d.decision_id === candidate.decision_id);
        if (!current || current.status !== "open" || current.version !== decision.version) {
          decision = undefined;
          notify("原选择已失效，本次仅发送文字与附件", "warn");
        }
      }
      await composerDraft.flush();
      await queue.enqueue({
        body,
        files,
        attachments,
        ...(decision ? { decision } : {}),
        ...(replyTo || reply ? { replyTo: replyTo || reply!.message_id } : {}),
        ...(lastSeq ? { lastSeenSeq: lastSeq } : {}),
      });
      composerDraft.clear();
      await composerDraft.flush();
      setConfirmedChoice(null);
    } catch {
      // Not written down, so not sent either; give the entire draft back.

      notify("消息未能保存到发送队列，文字和附件已恢复，请重试", "warn");
    } finally {
      saving.current = false;
      setPersisting(false);
    }
  };

  const addFiles = (files: File[]) => {
    if (!files.length || blocked || saving.current) return;
    composerDraft.addFiles(files);
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
  } else if (queued && !busy) {
    hint = "加入队列，按接收顺序依次处理";
  } else if (reply) {
    hint = busy
      ? "加入队列，当前回复结束后处理"
      : `回复 ${clock(Date.parse(reply.created_at))} 的消息`;
  }

  const showStop = busy !== undefined && body === "" && !files.length && !attachments.length;
  return (
    <div
      className={styles.composer}
      data-dragging={dragging || undefined}
      onDragOver={(event) => {
        if (!event.dataTransfer.types.includes("Files")) return;
        event.preventDefault();
        event.dataTransfer.dropEffect = blocked ? "none" : "copy";
        setDragging(true);
      }}
      onDragLeave={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setDragging(false);
      }}
      onDrop={(event) => {
        if (!event.dataTransfer.types.includes("Files")) return;
        event.preventDefault();
        setDragging(false);
        addFiles(Array.from(event.dataTransfer.files));
      }}
    >
      {draftError && <p role="alert">{draftError}</p>}
      {!!attachments.length && (
        <div>
          <MessageAttachments attachments={attachments} />
          {attachments.map((file, index) => (
            <button key={file.file_id} onClick={() => composerDraft.removeAttachment(index)}>
              移除 {file.name}
            </button>
          ))}
        </div>
      )}
      {source?.decision && (
        <label>
          <input
            type="checkbox"
            checked={confirmChoice}
            onChange={(e) => setConfirmedChoice(e.target.checked ? choiceKey : null)}
          />
          确认原选择：{source.decision.option}（发送前核对当前版本）
          <button onClick={() => composerDraft.clearChoice()}>清除原选择</button>
        </label>
      )}
      {!!files.length && (
        <ul className={styles.files} aria-label="待发送附件">
          {files.map((file, index) => (
            <li key={index}>
              <LocalAttachment blob={file} name={file.name} />
              <button
                type="button"
                aria-label={`移除附件 ${file.name}`}
                title={`移除附件 ${file.name}`}
                onClick={() => composerDraft.removeFile(index)}
              >
                <Close />
              </button>
            </li>
          ))}
        </ul>
      )}
      <textarea
        ref={textarea}
        rows={2}
        value={draft}
        onChange={(e) => composerDraft.write(e.target.value)}
        onKeyDown={onKeyDown}
        onPaste={(event) => {
          const files = Array.from(event.clipboardData.files);
          if (!files.length) return;
          event.preventDefault();
          addFiles(files);
        }}
        placeholder={`发给 ${recipient?.title ?? "主 Agent"}…${touch ? "" : "（Enter 发送，Shift+Enter 换行）"}`}
        aria-label="消息"
        disabled={blocked}
        readOnly={persisting}
      />
      <div className={styles.bar}>
        <input
          ref={fileInput}
          type="file"
          multiple
          hidden
          aria-label="选择附件"
          onChange={(event) => {
            const files = Array.from(event.currentTarget.files ?? []);
            event.currentTarget.value = "";
            addFiles(files);
          }}
        />
        <button
          type="button"
          className={styles.attach}
          aria-label="添加附件"
          title="选择文件或粘贴、拖入附件"
          onClick={() => fileInput.current?.click()}
          disabled={blocked || persisting}
        >
          <svg
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.8"
            strokeLinecap="round"
            strokeLinejoin="round"
            aria-hidden
          >
            <path d="m21 11-8.5 8.5a6 6 0 0 1-8.5-8.5l9-9a4 4 0 0 1 5.7 5.7l-9 9a2 2 0 0 1-2.8-2.8l8.5-8.5" />
          </svg>
        </button>
        <span className={styles.hint} role="status">
          {dragging ? "松开以添加附件" : hint}
        </span>
        <Settings connection={connection} />
        <ContextUsage connection={connection} />
        {busy && !showStop && (
          <button
            type="button"
            className={styles.attach}
            disabled={stopping}
            onClick={() => void stop()}
            aria-label="停止当前回复"
          >
            <Stop />
          </button>
        )}
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
            disabled={blocked || persisting || (!body && !files.length && !attachments.length)}
            title={busy || queued ? "加入队列" : "发送"}
            aria-label={busy || queued ? "加入队列" : "发送"}
          >
            <Send />
          </button>
        )}
      </div>
    </div>
  );
}
