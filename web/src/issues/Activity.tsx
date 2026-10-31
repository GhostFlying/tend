import { useEntryState } from "../app/router.ts";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type RefObject,
} from "react";
import type {
  IssueActivityEntry,
  IssueResult,
  IssueView,
  IssueTranscriptBlock,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { noOutbox } from "../queue/sendQueue.ts";
import { LiveStatus } from "../ui/LiveStatus.tsx";
import { conversationStatus } from "../ui/activityStatus.ts";
import { AgentTurn, OwnerMessage, OutgoingRow, SystemMessage } from "../conversation/Messages.tsx";
import { buildTranscriptEntries, type TranscriptEntry } from "../conversation/transcriptBlocks.ts";
import { captureAnchor, restoreAnchor, type ScrollAnchor } from "../conversation/scrollAnchor.ts";
import { IssueTurnRead } from "./turn.ts";
import { IssueResultCard, HistoricalResultCard } from "./Results.tsx";
import type { IssueReadingState } from "./reading.ts";
import type { Issues } from "./data.ts";
import styles from "./DetailPage.module.css";

const emptyEntries: IssueActivityEntry[] = [];

export function IssueActivity({
  issue,
  reads,
  reading,
  results,
  onIssue,
  onTask,
  scroll,
}: {
  issue: IssueView;
  reads: Issues;
  reading: IssueReadingState;
  results: IssueResult[];
  onIssue(id: string): void;
  onTask(id: string): void;
  scroll: RefObject<HTMLDivElement | null>;
}) {
  const { queue, conversation, live } = useServices();
  const canonical = useStore(live.conversation);
  const state = useStore(reads.activity.state);
  const pending = useStore(
    issue.conversation?.conversation_id && conversation?.id === issue.conversation.conversation_id
      ? (queue?.list ?? noOutbox)
      : noOutbox,
  );
  const [all, setAll] = useState(reading.activityExpanded);
  const [error, setError] = useState("");
  const [newItems, setNewItems] = useState(false);
  const following = useRef(reading.followsLatest());
  const readingAnchor = useRef<ScrollAnchor | null>(null);
  useEffect(() => {
    const node = scroll.current;
    if (!node) return;
    const moved = () => {
      following.current = node.scrollHeight - node.scrollTop - node.clientHeight < 100;
      if (following.current) reading.followLatest();
      else reading.pauseFollowing();
      readingAnchor.current = captureAnchor(node);
    };
    let frame = 0;
    const disclosure = (event: MouseEvent) => {
      if (
        !(event.target instanceof Element) ||
        !event.target.closest("summary, button[aria-expanded]")
      )
        return;
      following.current = false;
      reading.pauseFollowing();
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        frame = requestAnimationFrame(moved);
      });
    };
    node.addEventListener("scroll", moved);
    node.addEventListener("click", disclosure, true);
    return () => {
      cancelAnimationFrame(frame);
      node.removeEventListener("scroll", moved);
      node.removeEventListener("click", disclosure, true);
    };
  }, [scroll, reading]);
  const last = useRef(0);
  const previousPending = useRef(0);
  useEffect(() => {
    void reads.activity.open(issue.issue_id, reading.activity);
    const remember = () => reading.saveActivity(reads.activity.state.get().value);
    const stop = reads.activity.state.subscribe(remember);
    return () => {
      remember();
      stop();
      reads.activity.close();
    };
  }, [reads, issue.issue_id, reading]);
  const rows = state.value?.items ?? emptyEntries;
  const visible = useMemo(() => {
    if (all) return rows;
    const blocks = rows.filter((row) => row.block);
    if (blocks.length <= 3) return rows;
    const first = blocks.at(-3)!.position;
    return rows.filter((row) => row.position >= first);
  }, [rows, all]);
  const [continuations, setContinuations] = useState<Record<string, IssueTranscriptBlock>>({});
  const rememberContinuation = useCallback((block: IssueTranscriptBlock) => {
    setContinuations((previous) => ({ ...previous, [block.trigger.message_id]: block }));
  }, []);
  const transcript = useMemo(
    () =>
      buildTranscriptEntries(
        visible.flatMap((entry) =>
          entry.block ? [continuations[entry.block.trigger.message_id] ?? entry.block] : [],
        ),
      ),
    [visible, continuations],
  );
  const ids = new Set(
    rows.flatMap((row) =>
      row.block
        ? [row.block.trigger.message_id, ...(row.block.items ?? []).map((item) => item.message_id)]
        : [],
    ),
  );
  const lastSeq = rows.reduce(
    (last, row) =>
      Math.max(
        last,
        row.block?.trigger.seq ?? 0,
        ...(row.block?.items ?? []).map((item) => item.seq),
      ),
    canonical.items.at(-1)?.seq ?? 0,
  );
  const outgoing = pending.filter(
    (message) =>
      message.state !== "cancelled" &&
      (!message.messageId || !ids.has(message.messageId)) &&
      (message.state !== "sent" ||
        (!!message.messageId && (message.seq === undefined || message.seq > lastSeq))),
  );
  useLayoutEffect(() => {
    const end = scroll.current;
    const position = rows.at(-1)?.position ?? 0;
    if (position > last.current && last.current && end) {
      const near = following.current || reading.followsLatest();
      if (near) end.scrollTo({ top: end.scrollHeight });
      else requestAnimationFrame(() => setNewItems(true));
    }
    last.current = position;
    if (outgoing.length > previousPending.current && end) {
      reading.followLatest();
      end.scrollTo({ top: end.scrollHeight });
      requestAnimationFrame(() => setNewItems(false));
    }
    previousPending.current = outgoing.length;
  }, [rows, outgoing.length, scroll, reading]);
  const latestContent = JSON.stringify(rows);
  const previousContent = useRef("");
  useLayoutEffect(() => {
    const node = scroll.current;
    if (node && previousContent.current && previousContent.current !== latestContent) {
      if (following.current || reading.followsLatest()) node.scrollTo({ top: node.scrollHeight });
      else {
        if (readingAnchor.current) restoreAnchor(node, readingAnchor.current);
        requestAnimationFrame(() => setNewItems(true));
      }
    }
    if (node) readingAnchor.current = captureAnchor(node);
    previousContent.current = latestContent;
  }, [latestContent, scroll, reading]);
  const earlier = async () => {
    const node = scroll.current;
    const anchor = node ? captureAnchor(node) : null;
    try {
      if (!all) {
        setAll(true);
        reading.expandActivity();
        if (visible.length === rows.length) await reads.activity.earlier();
      } else await reads.activity.earlier();
      if (node && anchor) requestAnimationFrame(() => restoreAnchor(node, anchor));
    } catch {
      setError("更早记录读取失败，请重试。");
    }
  };
  const jump = () => {
    reading.followLatest();
    scroll.current?.scrollTo({ top: scroll.current.scrollHeight, behavior: "smooth" });
    setNewItems(false);
  };
  return (
    <div className={styles.activity}>
      {(state.value?.more || visible.length < rows.length) && (
        <button className={styles.textLink} onClick={() => void earlier()}>
          查看更早记录
        </button>
      )}
      {(state.error || error) && (
        <p role="status" className={styles.error}>
          {error || "活动读取失败，已有记录仍保留。"}
          <button
            onClick={() => {
              setError("");
              void reads.activity.refresh();
            }}
          >
            重新读取
          </button>
        </p>
      )}
      {!state.value && !state.error && <p role="status">正在读取讨论与进展…</p>}
      {state.value && !rows.length && (
        <p className={styles.muted}>暂无讨论记录。可以在下方补充要求。</p>
      )}
      {visible.map((entry) =>
        entry.block ? (
          <ActivityBlock
            key={entry.id}
            entry={entry}
            rows={transcript.filter((row) => row.key.startsWith(`${entry.block!.position}:`))}
            onContinuation={rememberContinuation}
          />
        ) : (
          <Milestone
            key={entry.id}
            entry={entry}
            results={results}
            onIssue={onIssue}
            onTask={onTask}
          />
        ),
      )}
      {outgoing.map((message) => (
        <OutgoingRow key={message.requestId} message={message} queue={queue} />
      ))}
      {newItems && (
        <button className={styles.newItems} onClick={jump}>
          有新进展 · 到最新 ↓
        </button>
      )}
      <div data-activity-end />
    </div>
  );
}
function ActivityBlock({
  entry,
  rows,
  onContinuation,
}: {
  entry: IssueActivityEntry;
  rows: TranscriptEntry[];
  onContinuation(block: IssueTranscriptBlock): void;
}) {
  const { client } = useServices();
  const continuation = useMemo(() => new IssueTurnRead(client), [client]);
  const extra = useStore(continuation.state);
  const [opened, setOpened] = useEntryState(`activity-open:${entry.id}`, false);
  const block = entry.block!;
  useEffect(() => {
    if (opened) void continuation.open(block.trigger.message_id);
  }, [opened, block, continuation]);
  useEffect(() => () => continuation.close(), [continuation]);
  useEffect(() => {
    if (extra.value) onContinuation(extra.value);
  }, [extra.value, onContinuation]);
  const items = rows.flatMap((row) =>
    row.kind === "agent" ? row.items : row.kind === "system" ? [row.item] : [],
  );
  const [expanded, setExpanded] = useEntryState(`activity-expanded:${entry.id}`, false);
  const long = items.some((item) => (item.body?.length ?? 0) > 400);
  const more = () => {
    if (!opened) setOpened(true);
    else if (extra.error) void continuation.refresh();
    else void continuation.more();
  };
  return (
    <article className={styles.discussion} data-scroll-id={entry.id}>
      {rows.map((row) => {
        switch (row.kind) {
          case "owner":
            return (
              <div key={row.key} className={styles.owner}>
                <div className={styles.author}>
                  你{" "}
                  <time>
                    {new Date(row.item.created_at).toLocaleTimeString("zh-CN", {
                      hour: "2-digit",
                      minute: "2-digit",
                    })}
                  </time>
                </div>
                <OwnerMessage item={row.item} presentation="issue" />
              </div>
            );
          case "agent":
            return (
              <div key={row.key}>
                <div className={styles.author}>
                  {entry.author} <small>负责人</small>
                </div>
                <AgentTurn
                  entry={row}
                  proseClassName={!expanded && long ? styles.replyClamp : undefined}
                />
                {long && (
                  <button
                    className={styles.textLink}
                    aria-expanded={expanded}
                    onClick={() => setExpanded(!expanded)}
                  >
                    {expanded ? "收起" : "展开全文"}
                  </button>
                )}
              </div>
            );
          case "system":
            return <SystemMessage key={row.key} item={row.item} />;
          case "block-more":
            return (
              <button key={row.key} onClick={() => void more()}>
                继续读取本轮消息
              </button>
            );
          default:
            return null;
        }
      })}
      {block.trigger.state === "processing" && !rows.some((row) => row.kind === "agent") && (
        <LiveStatus
          status={conversationStatus([], "processing", Date.parse(block.trigger.created_at))}
        />
      )}
      {items.some(
        (item) =>
          item.assistant_state && !["completed", "streaming"].includes(item.assistant_state),
      ) && (
        <p role="status" className={styles.muted}>
          本轮回复已中断、失败或结果未知；已生成内容和原请求仍保留。
        </p>
      )}
      {extra.error && (
        <p role="status">
          本轮后续记录读取失败
          <button onClick={() => void more()}>重试</button>
        </p>
      )}
    </article>
  );
}
function Milestone({
  entry,
  results,
  onIssue,
  onTask,
}: {
  entry: IssueActivityEntry;
  results: IssueResult[];
  onIssue(id: string): void;
  onTask(id: string): void;
}) {
  const result = results.find((result) => result.artifact.artifact_id === entry.artifact_id);
  if (entry.artifact_id)
    return (
      <div data-scroll-id={entry.id}>
        {result ? (
          <IssueResultCard result={result} onTask={onTask} />
        ) : (
          <HistoricalResultCard artifactID={entry.artifact_id} onTask={onTask} />
        )}
      </div>
    );
  return (
    <div className={styles.milestone} data-scroll-id={entry.id}>
      <span className={styles.milestoneDot} />
      <span>
        {entry.title}
        <time>{new Date(entry.created_at).toLocaleString("zh-CN")}</time>
      </span>
      {entry.task_id ? (
        <button className={styles.textLink} onClick={() => onTask(entry.task_id!)}>
          查看 →
        </button>
      ) : entry.issue_id && entry.kind !== "issue.progress_updated" ? (
        <button className={styles.textLink} onClick={() => onIssue(entry.issue_id!)}>
          查看 →
        </button>
      ) : null}
    </div>
  );
}
