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
import { Chevron } from "../shell/icons.tsx";
import { clock } from "../conversation/model.ts";
import { groupActivityEvents, identifyActivityGroups } from "./activity.ts";
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
  const scrollportSize = useRef({ width: 0, height: 0 });
  const dataFrame = useRef(0);
  const expectedScroll = useRef<number | null>(null);
  const correctScroll = useCallback((node: HTMLElement, correct: () => void) => {
    const before = node.scrollTop;
    correct();
    if (node.scrollTop !== before) expectedScroll.current = node.scrollTop;
  }, []);
  const pagination = useRef<{
    trigger: string;
    top: number;
    ids: Set<string>;
    ready: boolean;
  } | null>(null);
  useEffect(() => {
    const node = scroll.current;
    if (!node) return;
    scrollportSize.current = { width: node.clientWidth, height: node.clientHeight };
    const resized = () => {
      if (
        node.clientWidth === scrollportSize.current.width &&
        node.clientHeight === scrollportSize.current.height
      )
        return false;
      scrollportSize.current = { width: node.clientWidth, height: node.clientHeight };
      if (following.current || reading.followsLatest()) {
        correctScroll(node, () => node.scrollTo({ top: node.scrollHeight }));
        setNewItems(false);
      } else if (readingAnchor.current)
        correctScroll(node, () => restoreAnchor(node, readingAnchor.current!));
      // A resize can enqueue a scroll event without changing scrollTop.
      // That event must not reinterpret paused intent using the new height.
      expectedScroll.current = node.scrollTop;
      readingAnchor.current = captureAnchor(node);
      return true;
    };
    const moved = () => {
      if (resized()) return;
      if (node.scrollHeight - node.scrollTop - node.clientHeight < 100) setNewItems(false);
      const programmatic =
        expectedScroll.current !== null && Math.abs(node.scrollTop - expectedScroll.current) < 1;
      expectedScroll.current = null;
      if (!programmatic && pagination.current && !pagination.current.ready)
        pagination.current = null;
      if (!programmatic) {
        following.current = node.scrollHeight - node.scrollTop - node.clientHeight < 100;
        if (following.current) reading.followLatest();
        else reading.pauseFollowing();
      }
      readingAnchor.current = captureAnchor(node);
    };
    let frame = 0;
    const disclosure = (event: MouseEvent) => {
      if (
        !(event.target instanceof Element) ||
        !event.target.closest("summary, button[aria-expanded]")
      )
        return;
      cancelAnimationFrame(dataFrame.current);
      following.current = false;
      reading.pauseFollowing();
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        frame = requestAnimationFrame(moved);
      });
    };
    const observer = new ResizeObserver(resized);
    observer.observe(node);
    node.addEventListener("scroll", moved);
    node.addEventListener("click", disclosure, true);
    return () => {
      cancelAnimationFrame(frame);
      cancelAnimationFrame(dataFrame.current);
      observer.disconnect();
      node.removeEventListener("scroll", moved);
      node.removeEventListener("click", disclosure, true);
    };
  }, [scroll, reading, correctScroll]);
  const last = useRef(0);
  const previousPending = useRef(0);
  useEffect(() => {
    void reads.activity.open(issue.issue_id, reading.activity);
    const remember = () => reading.saveActivity(reads.activity.state.get().value);
    const stop = reads.activity.state.subscribe(remember);
    return () => {
      remember();
      stop();
      pagination.current = null;
      reads.activity.close();
    };
  }, [reads, issue.issue_id, reading]);
  const rows = state.value?.items ?? emptyEntries;
  const [eventIdentities] = useState(() => new Map<string, string>());
  const visible = useMemo(() => {
    if (all) return rows;
    const blocks = rows.filter((row) => row.block);
    if (blocks.length <= 3) return rows;
    const first = blocks.at(-3)!.position;
    return rows.filter((row) => row.position >= first);
  }, [rows, all]);
  const activityGroups = useMemo(
    () => identifyActivityGroups(groupActivityEvents(visible), eventIdentities),
    [visible, eventIdentities],
  );
  const beginContinuation = useCallback(
    (trigger: string, control: HTMLButtonElement, ids: string[]) => {
      cancelAnimationFrame(dataFrame.current);
      following.current = false;
      reading.pauseFollowing();
      pagination.current = {
        trigger,
        top: control.getBoundingClientRect().top,
        ids: new Set(ids),
        ready: false,
      };
      setNewItems(false);
    },
    [reading],
  );
  const cancelContinuation = useCallback((trigger: string) => {
    if (pagination.current?.trigger === trigger) pagination.current = null;
  }, []);
  const [continuations, setContinuations] = useEntryState<Record<string, IssueTranscriptBlock>>(
    `activity-continuations:${issue.issue_id}`,
    {},
  );
  const [writeContinuations] = useState(() => setContinuations);
  const continuationCache = useRef(continuations);
  const rememberContinuation = useCallback(
    (block: IssueTranscriptBlock, explicit: boolean) => {
      if (explicit && pagination.current?.trigger === block.trigger.message_id)
        pagination.current.ready = true;
      continuationCache.current = {
        ...continuationCache.current,
        [block.trigger.message_id]: block,
      };
      writeContinuations(continuationCache.current);
    },
    [writeContinuations],
  );
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
      const near =
        following.current ||
        reading.followsLatest() ||
        end.scrollHeight - end.scrollTop - end.clientHeight < 100;
      if (near) correctScroll(end, () => end.scrollTo({ top: end.scrollHeight }));
      else
        requestAnimationFrame(() =>
          setNewItems(end.scrollHeight - end.scrollTop - end.clientHeight >= 100),
        );
    }
    last.current = position;
    if (outgoing.length > previousPending.current && end) {
      pagination.current = null;
      reading.followLatest();
      correctScroll(end, () => end.scrollTo({ top: end.scrollHeight }));
      requestAnimationFrame(() => setNewItems(false));
    }
    previousPending.current = outgoing.length;
  }, [rows, outgoing.length, scroll, reading, correctScroll]);
  const activityContent = JSON.stringify([rows, transcript]);
  const latestContent = JSON.stringify([issue, rows, transcript]);
  const previousContent = useRef("");
  const previousFirst = useRef(rows[0]?.position ?? 0);
  const previousNotification = useRef(activityContent);
  const previousHadActivity = useRef(rows.length > 0);
  useLayoutEffect(() => {
    const notificationContent = JSON.stringify([
      rows.filter((row) => row.position >= previousFirst.current),
      transcript.filter((row) =>
        rows.some(
          (entry) =>
            entry.position >= previousFirst.current &&
            entry.block &&
            row.key.startsWith(`${entry.block.position}:`),
        ),
      ),
    ]);
    const node = scroll.current;
    if (node && pagination.current?.ready) {
      const boundary = pagination.current;
      const block = continuations[boundary.trigger];
      pagination.current = null;
      cancelAnimationFrame(dataFrame.current);
      dataFrame.current = requestAnimationFrame(() => {
        const nodes = [...node.querySelectorAll<HTMLElement>("[data-scroll-id]")];
        for (const message of block?.items ?? []) {
          if (boundary.ids.has(message.message_id)) continue;
          const row = nodes.find((item) => item.dataset.scrollId === message.message_id);
          const group = [...node.querySelectorAll<HTMLElement>("button[aria-controls]")].find(
            (control) =>
              control
                .getAttribute("aria-controls")
                ?.split(" ")
                .some(
                  (id) => id === row?.id || !!document.getElementById(id)?.contains(row ?? null),
                ),
          );
          const item = row?.checkVisibility() ? row : group?.checkVisibility() ? group : null;
          if (!item) continue;
          correctScroll(node, () =>
            node.scrollTo({
              top: node.scrollTop + item.getBoundingClientRect().top - boundary.top,
            }),
          );
          break;
        }
        readingAnchor.current = captureAnchor(node);
      });
    } else if (node && previousContent.current && previousContent.current !== latestContent) {
      if (following.current || reading.followsLatest()) {
        correctScroll(node, () => node.scrollTo({ top: node.scrollHeight }));
        setNewItems(false);
      } else {
        const anchor = readingAnchor.current;
        if (anchor) {
          correctScroll(node, () => restoreAnchor(node, anchor));
          cancelAnimationFrame(dataFrame.current);
          dataFrame.current = requestAnimationFrame(() => {
            correctScroll(node, () => restoreAnchor(node, anchor));
            readingAnchor.current = captureAnchor(node);
          });
        }
        if (previousHadActivity.current && previousNotification.current !== notificationContent)
          requestAnimationFrame(() =>
            setNewItems(node.scrollHeight - node.scrollTop - node.clientHeight >= 100),
          );
      }
    }
    if (node) readingAnchor.current = captureAnchor(node);
    previousContent.current = latestContent;
    previousNotification.current = activityContent;
    previousFirst.current = rows[0]?.position ?? 0;
    previousHadActivity.current = rows.length > 0;
  }, [
    latestContent,
    activityContent,
    rows,
    transcript,
    scroll,
    reading,
    continuations,
    correctScroll,
  ]);
  const earlier = async () => {
    const node = scroll.current;
    const anchor = node ? captureAnchor(node) : null;

    try {
      if (!all) {
        setAll(true);
        reading.expandActivity();
        if (visible.length === rows.length) await reads.activity.earlier();
      } else await reads.activity.earlier();
      if (node && anchor)
        requestAnimationFrame(() => correctScroll(node, () => restoreAnchor(node, anchor)));
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
      {activityGroups.map(({ entries: group, key }) => {
        const entry = group[0]!;
        return entry.block ? (
          <ActivityBlock
            key={entry.id}
            entry={entry}
            rows={transcript.filter((row) => row.key.startsWith(`${entry.block!.position}:`))}
            onContinuation={rememberContinuation}
            onPagination={beginContinuation}
            onPaginationCancel={cancelContinuation}
            currentIssueId={issue.issue_id}
          />
        ) : group.length > 1 ? (
          <EventGroup
            key={key}
            entries={group}
            results={results}
            onIssue={onIssue}
            onTask={onTask}
          />
        ) : (
          <Milestone
            key={entry.id}
            entry={entry}
            results={results}
            onIssue={onIssue}
            onTask={onTask}
          />
        );
      })}
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
  onPagination,
  onPaginationCancel,
  currentIssueId,
}: {
  entry: IssueActivityEntry;
  currentIssueId: string;
  rows: TranscriptEntry[];
  onContinuation(block: IssueTranscriptBlock, explicit: boolean): void;
  onPagination(trigger: string, control: HTMLButtonElement, ids: string[]): void;
  onPaginationCancel(trigger: string): void;
}) {
  const { client } = useServices();
  const continuation = useMemo(() => new IssueTurnRead(client), [client]);
  const extra = useStore(continuation.state);
  const explicit = useRef(false);
  const [opened, setOpened] = useEntryState(`activity-open:${entry.id}`, false);
  const [depth, setDepth] = useEntryState(`activity-depth:${entry.id}`, 2);
  const block = entry.block!;
  useEffect(() => {
    if (opened) void continuation.open(block.trigger.message_id, depth);
  }, [opened, block, continuation, depth]);
  useEffect(
    () => () => {
      continuation.close();
      onPaginationCancel(block.trigger.message_id);
    },
    [continuation, onPaginationCancel, block.trigger.message_id],
  );
  useEffect(() => {
    if (extra.error) {
      explicit.current = false;
      onPaginationCancel(block.trigger.message_id);
    }
    if (extra.value && !extra.error) {
      onContinuation(extra.value, explicit.current);
      explicit.current = false;
    }
  }, [extra.value, extra.error, onContinuation, onPaginationCancel, block.trigger.message_id]);
  const items = rows.flatMap((row) =>
    row.kind === "agent" ? row.items : row.kind === "system" ? [row.item] : [],
  );
  const [expanded, setExpanded] = useEntryState(`activity-expanded:${entry.id}`, false);
  const long = items.some((item) => (item.body?.length ?? 0) > 400);
  const more = (control?: HTMLButtonElement) => {
    if (control) {
      explicit.current = true;
      onPagination(
        block.trigger.message_id,
        control,
        items.map((item) => item.message_id),
      );
    }
    if (!opened) setOpened(true);
    else if (extra.error) void continuation.refresh();
    else setDepth(depth + 1);
  };
  return (
    <article className={styles.discussion} data-scroll-id={entry.id}>
      {rows.map((row) => {
        switch (row.kind) {
          case "owner":
            return (
              <div key={row.key} className={styles.owner}>
                <div className={styles.author}>
                  <span className={styles.avatar} aria-hidden="true">
                    你
                  </span>
                  你{" "}
                  <time dateTime={row.item.created_at} title={row.item.created_at}>
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
              <div
                key={row.key}
                className={styles.reply}
                data-tone={
                  row.state === "failed" ||
                  row.items.some((item) => item.assistant_state === "failed")
                    ? "error"
                    : undefined
                }
              >
                <div className={styles.author}>
                  <span className={styles.avatar} aria-hidden="true">
                    {Array.from(entry.author || "Agent")[0]}
                  </span>
                  {entry.author} <small>负责人</small>
                  <time dateTime={row.items[0]?.created_at} title={row.items[0]?.created_at}>
                    {clock(Date.parse(row.items[0]?.created_at ?? block.trigger.created_at))}
                  </time>
                </div>
                <AgentTurn
                  entry={row}
                  currentIssueId={currentIssueId}
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
              <button key={row.key} onClick={(event) => void more(event.currentTarget)}>
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
  groupedFirst = false,
}: {
  entry: IssueActivityEntry;
  groupedFirst?: boolean;
  results: IssueResult[];
  onIssue(id: string): void;
  onTask(id: string): void;
}) {
  const result = results.find((result) => result.artifact.artifact_id === entry.artifact_id);
  if (entry.artifact_id)
    return (
      <div data-scroll-id={groupedFirst ? undefined : entry.id}>
        <p className={styles.muted}>
          {entry.author || "tend"} {entry.title} ·{" "}
          {new Date(entry.created_at).toLocaleString("zh-CN")}
        </p>
        {result ? (
          <IssueResultCard result={result} onTask={onTask} />
        ) : (
          <HistoricalResultCard artifactID={entry.artifact_id} onTask={onTask} />
        )}
      </div>
    );
  return (
    <div
      className={styles.milestone}
      data-tone={/failed|failure/.test(entry.kind) ? "error" : undefined}
      data-scroll-id={groupedFirst ? undefined : entry.id}
    >
      <span className={styles.milestoneDot} aria-hidden="true">
        ·
      </span>
      <span>
        <span>{entry.author || "tend"}</span> <span>{entry.title}</span>
        <time title={new Date(entry.created_at).toISOString()}> · {clock(entry.created_at)}</time>
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

function EventGroup({
  entries,
  results,
  onIssue,
  onTask,
}: {
  entries: IssueActivityEntry[];
  results: IssueResult[];
  onIssue(id: string): void;
  onTask(id: string): void;
}) {
  const first = entries[0]!;
  return (
    <details className={styles.eventGroup} data-event-group data-scroll-id={first.id}>
      <summary>
        <span className={styles.milestoneDot} aria-hidden="true">
          ·
        </span>
        <span>
          {first.author || "tend"} {[...new Set(entries.map((entry) => entry.title))].join("、")}
          <time title={new Date(first.created_at).toISOString()}> · {clock(first.created_at)}</time>
        </span>
        <Chevron />
      </summary>
      {entries.map((entry) => (
        <Milestone
          key={entry.id}
          entry={entry}
          groupedFirst={entry.id === first.id}
          results={results}
          onIssue={onIssue}
          onTask={onTask}
        />
      ))}
    </details>
  );
}
