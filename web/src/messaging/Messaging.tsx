import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { Connection } from "../api/client.ts";
import type { MessagingMessage, MessagingTrace } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { Link } from "../app/router.ts";
import { useStore } from "../state/store.ts";
import { ComposerDraft } from "../conversation/draft.ts";
import { Markdown } from "../conversation/Markdown.tsx";
import { FilePreview } from "../tasks/FilePreview.tsx";
import { SendQueue, noOutbox } from "../queue/sendQueue.ts";
import { openOutbox, type OutgoingMessage } from "../queue/outbox.ts";
import { runOp } from "../app/ops.ts";
import { Button } from "../ui/Button.tsx";
import { Sheet } from "../ui/Modal.tsx";
import { Textarea } from "../ui/Field.tsx";
import { Close, Send } from "../shell/icons.tsx";
import { ConnectionBadge } from "../shell/ConnectionBadge.tsx";
import { MessagingFeed } from "./feed.ts";
import { Interaction, interactionStatus } from "./Interaction.tsx";
import styles from "./Messaging.module.css";

const processing: Record<string, string> = {
  pending: "待处理",
  processing: "正在处理",
  waiting_user: "等待你的回答",
  waiting_execution: "等待执行",
  waiting_assignment: "等待负责人可用",
  recovering: "正在恢复",
  needs_review: "等待核对",
  unknown: "处理结果尚未确认",
  failed: "处理失败",
  interrupted: "已中断",
  processed: "已处理",
};
const delivery: Record<string, string> = {
  accepted: "渠道已接受",
  queued: "等待发送",
  sending: "正在发送",
  unknown: "投递结果未知",
  failed: "投递失败",
  unavailable: "渠道不可用",
  cancelled: "已取消",
};
const time = (at: number) =>
  new Date(at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

function messageBody(m: MessagingMessage): string {
  if (m.author === "system" && m.reason?.startsWith("attention_changed:")) {
    const notice = /^Attention \S+ \(([^)]+)\):\s*([\s\S]*)$/.exec(m.body);
    if (notice) return notice[1] === "open" ? notice[2]! : `事项已更新：${notice[2]}`;
  }
  return m.body;
}

export function Messaging({ visible, connection }: { visible: boolean; connection: Connection }) {
  const { client } = useServices();
  const feed = useMemo(() => new MessagingFeed(client), [client]);
  const [draft] = useState(() => new ComposerDraft("im"));
  const value = useStore(draft.state);
  const data = useStore(feed.state);
  const [queue, setQueue] = useState<SendQueue | null>(null);
  const outgoing = useStore(queue?.list ?? noOutbox);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [confirmAction, setConfirmAction] = useState(false);
  const [details, setDetails] = useState<MessagingMessage | null>(null);
  const viewport = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLTextAreaElement>(null);
  const following = useRef(true);
  const olderAnchor = useRef<{ height: number; top: number } | null>(null);
  const [atEnd, setAtEnd] = useState(true);
  const [savedQuote, setSavedQuote] = useState<{
    id: string;
    message?: MessagingMessage;
    error?: string;
  } | null>(null);
  const localQuote = data.items.find((m) => m.message_id === value.replyTo);
  const quote = localQuote ?? (savedQuote?.id === value.replyTo ? savedQuote?.message : undefined);
  const missingQuote = !!value.replyTo && !quote;
  useEffect(() => {
    if (!value.replyTo || data.items.some((m) => m.message_id === value.replyTo)) return;
    const id = value.replyTo;
    const controller = new AbortController();
    void client
      .get<MessagingMessage>(
        `/api/v1/messaging/messages/${encodeURIComponent(id)}`,
        controller.signal,
      )
      .then((message) => setSavedQuote({ id, message }))
      .catch((e) => {
        if (!controller.signal.aborted) setSavedQuote({ id, error: String(e) });
      });
    return () => controller.abort();
  }, [client, value.replyTo, data.items]);
  const staleQuote = !!quote?.interaction && quote.interaction.state !== "open";
  const pending = useMemo(
    () => [
      ...new Map(
        data.items
          .filter((m) => m.interaction?.state === "open")
          .map((m) => [m.interaction!.interaction_id, m]),
      ).values(),
    ],
    [data.items],
  );
  const cleanup = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => {
    clearTimeout(cleanup.current);
    feed.start();
    void draft.open();
    let active = true;
    void openOutbox()
      .then((outbox) =>
        SendQueue.open({
          outbox,
          sender: client,
          operation: "messaging.send",
          scope: "im",
          keepSentMs: 0,
        }),
      )
      .then((q) => {
        if (active) {
          setQueue(q);
          void q.resume();
        }
      })
      .catch(() => {
        if (active) setError("此浏览器无法保存发送队列，暂时不能发送。草稿仍保留。");
      });
    return () => {
      active = false;
      cleanup.current = setTimeout(() => {
        void feed.stop();
        void draft.close();
      }, 0);
    };
  }, [client, draft, feed]);
  useEffect(() => {
    if (connection.status === "online") void queue?.resume();
  }, [connection.status, queue]);
  useLayoutEffect(() => {
    const el = viewport.current;
    if (!el || !visible) return;
    if (olderAnchor.current) {
      el.scrollTop = olderAnchor.current.top + el.scrollHeight - olderAnchor.current.height;
      olderAnchor.current = null;
    } else if (following.current) el.scrollTop = el.scrollHeight;
  }, [data.items, visible, outgoing]);
  useEffect(() => {
    const el = viewport.current;
    if (!el) return;
    const observer = new ResizeObserver(() => {
      if (following.current && visible) el.scrollTop = el.scrollHeight;
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, [visible]);
  const reply = (message: MessagingMessage) => {
    setConfirmAction(false);
    draft.state.set({ ...draft.state.get(), replyTo: message.message_id });
    input.current?.focus();
  };
  const clearQuote = () => {
    setConfirmAction(false);
    draft.state.set({ ...draft.state.get(), replyTo: undefined });
  };
  const send = async () => {
    if (
      !queue ||
      saving ||
      staleQuote ||
      missingQuote ||
      (!value.text.trim() && !value.files.length && !value.attachments.length)
    )
      return;
    if (
      confirmAction &&
      quote?.interaction?.object_type === "attention" &&
      (value.files.length || value.attachments.length)
    ) {
      setError("完成说明请以文字提交；附件可以另发消息补充。");
      return;
    }
    setSaving(true);
    setError("");
    try {
      await queue.enqueue({
        body: value.text,
        files: value.files,
        attachments: value.attachments,
        ...(value.replyTo ? { replyTo: value.replyTo } : {}),
        ...(quote?.interaction?.object_type === "attention" && confirmAction
          ? {
              interaction: {
                id: quote.interaction.interaction_id,
                version: quote.interaction.version,
                confirm: true,
              },
            }
          : {}),
      });
      draft.clear();
      setConfirmAction(false);
      following.current = true;
      void feed.refresh();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };
  const choose = async (message: MessagingMessage, option: string) => {
    if (!queue || saving) return;
    setSaving(true);
    setError("");
    try {
      await queue.enqueue({
        body: message.interaction!.options?.find((o) => o.option_id === option)?.label ?? option,
        interaction: {
          id: message.interaction!.interaction_id,
          version: message.interaction!.version,
          option,
        },
      });
      void feed.refresh();
    } catch (e) {
      setError(String(e));
    } finally {
      setSaving(false);
    }
  };
  const recover = async (message: OutgoingMessage) => {
    setError("");
    try {
      const old = draft.state.get();
      const files = (message.uploads ?? [])
        .filter((u) => !u.receipt)
        .map((u) => new File([u.blob], u.name, { type: u.blob.type }));
      const references = [
        ...(message.references ?? []),
        ...(message.uploads ?? []).flatMap((u) => (u.receipt ? [u.receipt] : [])),
      ];
      draft.state.set({
        ...old,
        text: [old.text, message.body].filter(Boolean).join("\n\n"),
        files: [...old.files, ...files],
        attachments: [
          ...new Map([...old.attachments, ...references].map((a) => [a.file_id, a])).values(),
        ],
        replyTo: undefined,
      });
      await draft.flush();
      await queue?.discard(message.requestId);
      input.current?.focus();
    } catch (e) {
      setError(String(e));
    }
  };
  const older = async () => {
    const el = viewport.current;
    if (!el) return;
    olderAnchor.current = { height: el.scrollHeight, top: el.scrollTop };
    try {
      await feed.older();
    } catch (e) {
      olderAnchor.current = null;
      setError(String(e));
    }
  };
  const draftState = (change: Partial<typeof value>) =>
    draft.state.set({ ...draft.state.get(), ...change });
  return (
    <div className={styles.root} aria-label="Tend 对话">
      <header className={styles.header}>
        <h1 tabIndex={-1}>Tend</h1>
        <span className={styles.mobileConnection}>
          <ConnectionBadge connection={connection} compact />
        </span>
        <span className={styles.spacer} />
        {pending.length > 0 && (
          <Button
            variant="link"
            onClick={() =>
              document
                .getElementById(`im-${pending[0]!.message_id}`)
                ?.scrollIntoView({ block: "center" })
            }
          >
            {pending.length} 条待回答
          </Button>
        )}
        <Link className={styles.diagnostic} to={{ page: "chat" }}>
          诊断
        </Link>
      </header>
      <div
        ref={viewport}
        className={styles.timeline}
        onScroll={() => {
          const el = viewport.current!;
          following.current = el.scrollHeight - el.scrollTop - el.clientHeight < 70;
          setAtEnd(following.current);
        }}
      >
        <div className={styles.lane}>
          {data.more && (
            <Button variant="link" onClick={() => void older()}>
              查看更早消息
            </Button>
          )}
          {data.loading && (
            <p role="status" className={styles.muted}>
              正在读取消息…
            </p>
          )}
          {!data.loading && !data.items.length && (
            <div className={styles.empty}>
              <h2>和 Tend 说说你想做的事</h2>
              <p>回复和需要你决定的事情都会出现在这里。</p>
            </div>
          )}
          {data.items.map((m) => (
            <article
              key={m.message_id}
              id={`im-${m.message_id}`}
              className={styles.message}
              data-owner={m.author === "owner"}
            >
              <div className={styles.author}>
                {m.author === "owner" ? "你" : m.author === "external" ? "外部来源" : "Tend"}
                {m.channel_id === "telegram" && <span>Telegram</span>}
              </div>
              {m.interaction ? (
                <Interaction
                  message={m}
                  busy={
                    !queue ||
                    saving ||
                    outgoing.some(
                      (o) =>
                        (o.state === "sending" || o.state === "unknown") &&
                        o.envelope.target?.id === m.interaction!.interaction_id,
                    )
                  }
                  onAnswer={(message, option) => void choose(message, option)}
                  onReply={reply}
                />
              ) : (
                <div className={styles.body}>
                  {m.reply_to && (
                    <button
                      className={styles.reference}
                      onClick={() =>
                        document
                          .getElementById(`im-${m.reply_to}`)
                          ?.scrollIntoView({ block: "center" })
                      }
                    >
                      回复：
                      {data.items.find((v) => v.message_id === m.reply_to)?.interaction?.question ??
                        data.items.find((v) => v.message_id === m.reply_to)?.body.slice(0, 80) ??
                        "原消息"}
                    </button>
                  )}
                  <Markdown text={messageBody(m)} />
                </div>
              )}
              {(m.attachments ?? []).length > 0 && (
                <div className={styles.files}>
                  {(m.attachments ?? []).map((f) => (
                    <FilePreview key={f.file_id} file={f} scope="conversation/attachments" />
                  ))}
                </div>
              )}
              {(m.issue_ids ?? []).length > 0 && !m.interaction && (
                <div className={styles.links}>
                  {(m.issue_ids ?? []).map((id) => (
                    <Link key={id} to={{ page: "issue", id }}>
                      查看相关 Issue
                    </Link>
                  ))}
                </div>
              )}
              <footer className={styles.meta}>
                <time>{time(m.created_at)}</time>
                {m.direction === "input" && (
                  <span>
                    {m.agent_read_at ? `Tend 已读 ${time(m.agent_read_at)}` : "尚未确认已读"} ·{" "}
                    {processing[m.processing] ?? "状态待核对"}
                  </span>
                )}
                <Button
                  variant="link"
                  size="s"
                  onClick={() => reply(m)}
                  disabled={m.interaction?.state !== undefined && m.interaction.state !== "open"}
                >
                  回复
                </Button>
                <Button variant="link" size="s" onClick={() => setDetails(m)}>
                  详情
                </Button>
              </footer>
            </article>
          ))}
          {outgoing
            .filter(
              (o) => o.state !== "sent" || !data.items.some((m) => m.message_id === o.messageId),
            )
            .map((o) => (
              <article key={o.requestId} className={styles.message} data-owner>
                <div className={styles.body}>
                  {o.body || (o.uploads ?? []).map((f) => f.name).join("、")}
                </div>
                <div className={styles.meta}>
                  {o.state === "sending"
                    ? "正在发送"
                    : o.state === "sent"
                      ? "已提交"
                      : o.state === "unknown"
                        ? "发送结果未知"
                        : "发送失败"}
                  {o.error && <span>{o.error}</span>}
                  {o.state === "failed" && (
                    <Button variant="link" onClick={() => void recover(o)}>
                      恢复为新消息
                    </Button>
                  )}
                  {o.state === "unknown" && (
                    <Button variant="link" onClick={() => void queue?.reconcile(o.requestId)}>
                      核对原请求
                    </Button>
                  )}
                </div>
              </article>
            ))}
        </div>
      </div>
      {!atEnd && (
        <Button
          className={styles.latest}
          onClick={() => {
            following.current = true;
            viewport.current?.scrollTo({ top: viewport.current.scrollHeight });
            setAtEnd(true);
          }}
        >
          跳到最新
        </Button>
      )}
      <div className={styles.seat}>
        <div className={styles.composerWrap}>
          {(error || data.error || value.error) && (
            <p className={styles.error} role="status">
              {error || data.error || value.error}
            </p>
          )}
          {data.error && (
            <Button variant="link" onClick={() => void feed.refresh()}>
              重新读取消息
            </Button>
          )}
          {value.saved.length > 0 && (
            <details className={styles.savedDrafts}>
              <summary>暂存草稿 · {value.saved.length}</summary>
              {value.saved.map((saved) => (
                <Button
                  key={saved.id}
                  variant="link"
                  onClick={() => {
                    setConfirmAction(false);
                    void draft
                      .select(saved)
                      .then(() => input.current?.focus())
                      .catch((e) => setError(String(e)));
                  }}
                >
                  {saved.text.slice(0, 60) || "附件草稿"} · 恢复
                </Button>
              ))}
            </details>
          )}
          {value.replyTo && (
            <div className={styles.quote}>
              <span>
                {staleQuote ? interactionStatus(quote.interaction!) : "回复这条消息"}
                <br />
                <b>
                  {quote?.interaction?.question ??
                    quote?.body.slice(0, 100) ??
                    (savedQuote?.id === value.replyTo && savedQuote.error
                      ? "原消息读取失败，请重试或取消引用"
                      : "正在读取原消息…")}
                </b>
              </span>
              <Button variant="ghost" icon aria-label="取消引用，保留文字" onClick={clearQuote}>
                <Close />
              </Button>
            </div>
          )}
          {staleQuote && (
            <p className={styles.error}>
              此问题已不再接受回答。文字已保留，取消引用后可以作为新消息发送。
            </p>
          )}
          {quote?.interaction?.object_type === "attention" && !staleQuote && (
            <label className={styles.confirm}>
              <input
                type="checkbox"
                checked={confirmAction}
                onChange={(e) => setConfirmAction(e.target.checked)}
              />
              我确认这件事已完成，并提交下面的完成说明
            </label>
          )}
          <form
            className={styles.composer}
            onSubmit={(e) => {
              e.preventDefault();
              void send();
            }}
          >
            <Textarea
              ref={input}
              aria-label="发消息给 Tend"
              placeholder="发消息给 Tend…"
              value={value.text}
              rows={2}
              onChange={(e) => draftState({ text: e.target.value })}
              onKeyDown={(e) => {
                if (
                  e.key === "Enter" &&
                  !e.shiftKey &&
                  !e.nativeEvent.isComposing &&
                  !window.matchMedia("(pointer: coarse)").matches
                ) {
                  e.preventDefault();
                  void send();
                }
              }}
            />
            {(value.files.length > 0 || value.attachments.length > 0) && (
              <ul className={styles.selectedFiles}>
                {value.attachments.map((file, n) => (
                  <li key={file.file_id}>
                    {file.name}
                    <Button variant="link" onClick={() => draft.removeAttachment(n)}>
                      移除
                    </Button>
                  </li>
                ))}
                {value.files.map((file, n) => (
                  <li key={`${file.name}-${n}`}>
                    {file.name}
                    <Button variant="link" onClick={() => draft.removeFile(n)}>
                      移除
                    </Button>
                  </li>
                ))}
              </ul>
            )}
            <div className={styles.composerActions}>
              <label className={styles.attach} aria-label="添加附件">
                <input
                  type="file"
                  multiple
                  aria-label="添加附件"
                  onChange={(e) => {
                    draftState({ files: [...value.files, ...Array.from(e.target.files ?? [])] });
                    e.target.value = "";
                  }}
                />
                <svg
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.6"
                  aria-hidden
                >
                  <path d="m8 13 7-7a3 3 0 0 1 4 4L9 20a5 5 0 0 1-7-7L13 2a2 2 0 0 1 4 4L7 16" />
                </svg>
              </label>
              <span className={styles.hint}>Enter 发送 · Shift+Enter 换行</span>
              <Button
                variant="primary"
                icon
                type="submit"
                aria-label={
                  confirmAction && quote?.interaction?.object_type === "attention"
                    ? "确认已完成"
                    : "发送"
                }
                disabled={
                  !queue ||
                  !value.loaded ||
                  (confirmAction &&
                    quote?.interaction?.object_type === "attention" &&
                    !value.text.trim()) ||
                  saving ||
                  staleQuote ||
                  missingQuote ||
                  (!value.text.trim() && !value.files.length && !value.attachments.length)
                }
              >
                <Send />
              </Button>
            </div>
          </form>
        </div>
      </div>
      {details && (
        <MessageDetails
          message={data.items.find((m) => m.message_id === details.message_id) ?? details}
          onClose={() => setDetails(null)}
        />
      )}
    </div>
  );
}
function MessageDetails({ message, onClose }: { message: MessagingMessage; onClose(): void }) {
  const { client } = useServices();
  const [trace, setTrace] = useState<MessagingTrace | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    void client
      .get<MessagingTrace>(
        `/api/v1/messaging/messages/${encodeURIComponent(message.message_id)}/trace`,
        controller.signal,
      )
      .then(setTrace)
      .catch((e) => {
        if (!controller.signal.aborted) setError(String(e));
      });
    return () => controller.abort();
  }, [
    client,
    message.message_id,
    message.agent_read_at,
    message.delivery_state,
    message.processing,
  ]);
  return (
    <Sheet open title="消息详情" onClose={onClose}>
      <div className={styles.details}>
        <p>来源：{message.channel_id === "telegram" ? "Telegram" : "Web"}</p>
        <p>
          阅读：
          {message.agent_read_at
            ? `Tend 已读 ${time(message.agent_read_at)}`
            : "尚未确认 Tend 已读"}
        </p>
        <p>处理：{processing[message.processing] ?? "已发布"}</p>
        {trace?.receipts.deliveries?.map((d) => (
          <p key={d.delivery_id}>
            {d.channel_id === "telegram" ? "Telegram" : "Web"}：{delivery[d.state] ?? "状态待核对"}
          </p>
        ))}
        {message.channel_id === "telegram" && message.author !== "owner" && (
          <p>Telegram 不提供用户阅读回执。</p>
        )}
        <Button
          onClick={() =>
            void runOp(client, {
              operation: "messaging.read",
              target: { type: "message", id: message.message_id },
              payload: { content_revision: message.content_revision },
            }).then((result) =>
              setError(result.kind === "done" ? "已记录 Web 阅读确认" : result.message),
            )
          }
        >
          确认我已阅读此消息
        </Button>
        {error && <p role="status">{error}</p>}
        <Link to={{ page: "chat" }}>查看主 Agent 诊断</Link>
        {trace && (
          <details>
            <summary>执行追踪</summary>
            <pre>{JSON.stringify({ turns: trace.turns, tasks: trace.tasks }, null, 2)}</pre>
          </details>
        )}
      </div>
    </Sheet>
  );
}
