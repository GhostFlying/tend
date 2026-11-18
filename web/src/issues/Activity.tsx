import { ApiError } from "../api/client.ts";
import { WorkItemRead } from "../workitems/live.ts";
import {
  CandidateIdentity,
  candidateLabel,
  conflictActionLabel,
  DeliveryProgress,
} from "./DeliveryProgress.tsx";
import { EventNotification } from "./EventNotification.tsx";
import { MessageReceipts, ReceiptDetails } from "./MessageReceipts.tsx";
import { ExternalSourceMessage } from "../conversation/SourceDetails.tsx";
import { entryStore, useEntryState } from "../app/router.ts";
import {
  Component,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
  type RefObject,
} from "react";
import type {
  IssueActivityEntry,
  IssueConversationItem,
  IssueResult,
  IssueView,
  IssueTranscriptBlock,
} from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";
import { noOutbox } from "../queue/sendQueue.ts";
import {
  AgentTurn,
  OwnerMessage,
  UrgentMessage,
  OutgoingRow,
  SystemMessage,
} from "../conversation/Messages.tsx";
import { buildTranscriptEntries, type TranscriptEntry } from "../conversation/transcriptBlocks.ts";
import { captureAnchor, restoreAnchor, type ScrollAnchor } from "../conversation/scrollAnchor.ts";
import { Chevron } from "../shell/icons.tsx";
import { clock } from "../conversation/model.ts";
import { projectActivityGroups } from "./activity.ts";
import { DeliveryCandidateActivity } from "./DeliveryCandidateActivity.tsx";
import { IssueTurnRead } from "./turn.ts";
import { IssueProcessReads, type ProcessDetails } from "./process.ts";
import { activityTurnRows } from "./turnRows.ts";
import { ActivityResultCard } from "./Results.tsx";
import type { IssueReadingState } from "./reading.ts";
import type { Issues } from "./data.ts";
import styles from "./DetailPage.module.css";

const emptyEntries: IssueActivityEntry[] = [];
type ProcessScrollPosition = ScrollAnchor | "end";

export function IssueActivity({
  issue,
  targetMessage,
  reads,
  reading,
  results,
  onIssue,
  onTask,
  onReadingMove,
  scroll,
}: {
  issue: IssueView;
  targetMessage?: string | undefined;
  reads: Issues;
  reading: IssueReadingState;
  results: IssueResult[];
  onIssue(id: string): void;
  onTask(id: string): void;
  onReadingMove(): void;
  scroll: RefObject<HTMLDivElement | null>;
}) {
  const { client, queue, conversation, live } = useServices();
  const canonical = useStore(live.conversation);
  const state = useStore(reads.activity.state);
  const pending = useStore(
    issue.conversation?.conversation_id && conversation?.id === issue.conversation.conversation_id
      ? (queue?.list ?? noOutbox)
      : noOutbox,
  );
  const [error, setError] = useState("");
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const historyBoundary = useRef<HTMLDivElement>(null);
  const historyFetching = useRef(false);
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
  const snapshotProcessLayout = useCallback(
    (node: HTMLDivElement): ProcessScrollPosition | null => {
      if (pagination.current?.ready) return null;
      if (following.current || reading.followsLatest()) {
        if (node.scrollHeight - node.scrollTop - node.clientHeight < 100) return "end";
        following.current = false;
        reading.pauseFollowing();
      }
      const anchor = captureAnchor(node);
      readingAnchor.current = anchor;
      return anchor;
    },
    [reading],
  );
  const restoreProcessLayout = useCallback(
    (node: HTMLDivElement, position: ProcessScrollPosition) => {
      const restore = () => {
        if (position === "end") node.scrollTop = node.scrollHeight;
        else restoreAnchor(node, position);
      };
      correctScroll(node, restore);
      const restoredTop = node.scrollTop;
      cancelAnimationFrame(dataFrame.current);
      dataFrame.current = requestAnimationFrame(() => {
        if (Math.abs(node.scrollTop - restoredTop) < 1) correctScroll(node, restore);
        readingAnchor.current = captureAnchor(node);
      });
    },
    [correctScroll],
  );
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
      const widthChanged = node.clientWidth !== scrollportSize.current.width;
      const atPreviousEnd =
        Math.abs(node.scrollHeight - node.scrollTop - scrollportSize.current.height) < 1;
      scrollportSize.current = { width: node.clientWidth, height: node.clientHeight };
      if (following.current || reading.followsLatest()) {
        // Async composer readiness must not replace an already restored reading point.
        if (widthChanged || atPreviousEnd)
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
  const rows = state.value?.items ?? emptyEntries;
  const activityRoot = useRef<HTMLDivElement>(null);
  const readable = state.value?.readable === true;
  const processReads = useMemo(() => new IssueProcessReads(client), [client]);
  useEffect(() => () => processReads.close(), [processReads, issue.issue_id]);
  useEffect(() => {
    if (!readable || !rows.length) return;
    let cancelIdle: (() => void) | undefined;
    let nextFrame = 0;
    const paint = requestAnimationFrame(() => {
      nextFrame = requestAnimationFrame(() => {
        const prefetch = () => {
          const viewport = scroll.current?.getBoundingClientRect();
          if (!viewport) return;
          const nearby = new Set(
            Array.from(
              activityRoot.current?.querySelectorAll<HTMLElement>("[data-discussion-turn]") ?? [],
            )
              .filter((node) => {
                const rect = node.getBoundingClientRect();
                return rect.bottom >= viewport.top - 300 && rect.top <= viewport.bottom + 300;
              })
              .map((node) => node.dataset.discussionTurn),
          );
          const turns = rows.filter((row) => row.block?.process?.has_tools).toReversed();
          const latest = turns[0]?.block;
          if (latest)
            void processReads.prefetch([
              latest,
              ...turns
                .slice(1)
                .filter((row) => nearby.has(row.id))
                .map((row) => row.block!),
            ]);
        };
        if ("requestIdleCallback" in window) {
          const idle = window.requestIdleCallback(prefetch, { timeout: 500 });
          cancelIdle = () => window.cancelIdleCallback(idle);
        } else {
          const timer = setTimeout(prefetch, 75);
          cancelIdle = () => clearTimeout(timer);
        }
      });
    });
    return () => {
      cancelAnimationFrame(paint);
      cancelAnimationFrame(nextFrame);
      cancelIdle?.();
    };
  }, [processReads, readable, rows, scroll]);
  const focusedActivity = useRef<{ node: HTMLElement; id: string } | null>(null);
  const [eventIdentities] = useState(() => new Map<string, string>());
  const activityGroups = useMemo(
    () => projectActivityGroups(rows, eventIdentities),
    [rows, eventIdentities],
  );
  useLayoutEffect(() => {
    const focused = focusedActivity.current;
    if (!focused || focused.node.isConnected) return;
    const row = Array.from(
      activityRoot.current?.querySelectorAll<HTMLElement>("[data-scroll-id]") ?? [],
    ).find((node) => node.dataset.scrollId === focused.id);
    focusedActivity.current = null;
    const control =
      row?.closest("details[data-event-group]")?.querySelector<HTMLElement>("summary") ??
      row?.querySelector<HTMLElement>("button, a[href], [tabindex]") ??
      row;
    control?.focus({ preventScroll: true });
  }, [activityGroups]);
  const beginContinuation = useCallback(
    (trigger: string, control: HTMLButtonElement, ids: string[]) => {
      onReadingMove();
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
    [reading, onReadingMove],
  );
  const cancelContinuation = useCallback((trigger: string) => {
    if (pagination.current?.trigger === trigger) pagination.current = null;
  }, []);
  const { key: historyEntry, store: continuationStore } = entryStore<
    Record<string, { block: IssueTranscriptBlock; depth: number }>
  >(`activity-continuations:${issue.issue_id}`, {});
  const continuations = useStore(continuationStore);
  const rememberContinuation = useCallback(
    (block: IssueTranscriptBlock, depth: number, explicit: boolean) => {
      if (explicit && pagination.current?.trigger === block.trigger.message_id)
        pagination.current.ready = true;
      continuationStore.set({
        ...continuationStore.get(),
        [block.trigger.message_id]: { block, depth },
      });
    },
    [continuationStore],
  );
  const transcript = useMemo(
    () =>
      buildTranscriptEntries(
        rows.flatMap((entry) =>
          entry.block ? [continuations[entry.block.trigger.message_id]?.block ?? entry.block] : [],
        ),
      ),
    [rows, continuations],
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
      const block = continuations[boundary.trigger]?.block;
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
  const earlier = useCallback(async () => {
    if (historyFetching.current) return;
    historyFetching.current = true;
    setLoadingEarlier(true);
    setError("");
    try {
      await reads.activity.earlier();
    } catch {
      setError("更早记录读取失败，已有讨论仍保留。");
    } finally {
      historyFetching.current = false;
      setLoadingEarlier(false);
    }
  }, [reads]);
  useEffect(() => {
    const root = scroll.current;
    const boundary = historyBoundary.current;
    if (!root || !boundary || !state.value?.more || state.error || error || loadingEarlier) return;
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting) void earlier();
      },
      { root, rootMargin: "100px 0px" },
    );
    observer.observe(boundary);
    return () => observer.disconnect();
  }, [
    scroll,
    state.value?.more,
    state.value?.next_before,
    state.error,
    error,
    loadingEarlier,
    earlier,
  ]);
  const conversationID = issue.conversation?.conversation_id;
  const anchorRead = useMemo(
    () =>
      new WorkItemRead<IssueConversationItem | null>({
        async get<T>(path: string, signal?: AbortSignal): Promise<T> {
          try {
            return await client.get<T>(path, signal);
          } catch (error) {
            if (!(error instanceof ApiError) || (error.status !== 404 && error.status !== 403))
              throw error;
            if (error.status === 403 || !conversationID) return null as T;
          }
          try {
            const query = new URLSearchParams({ conversation_id: conversationID });
            return await client.get<T>(`${path}?${query}`, signal);
          } catch (error) {
            if (error instanceof ApiError && (error.status === 404 || error.status === 403))
              return null as T;
            throw error;
          }
        },
      }),
    [client, conversationID],
  );
  const anchor = useStore(anchorRead.state).value;
  useEffect(() => {
    if (!targetMessage || !conversationID) return;
    void anchorRead.open(`/api/v1/messages/${encodeURIComponent(targetMessage)}`);
    return () => anchorRead.close();
  }, [anchorRead, conversationID, targetMessage]);
  const scopedAnchor =
    anchor &&
    anchor.message_id === targetMessage &&
    anchor.conversation_id === conversationID &&
    !anchor.control?.withdrawn_at
      ? anchor
      : null;
  const located = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!targetMessage) {
      located.current = undefined;
      return;
    }
    if (located.current === targetMessage) return;
    const root = scroll.current;
    const target = root?.querySelector<HTMLElement>(
      `[data-scroll-id="${CSS.escape(targetMessage)}"]`,
    );
    if (target && root) {
      const frame = requestAnimationFrame(() => {
        if (!target.isConnected || !target.getClientRects().length) return;
        located.current = targetMessage;
        reading.pauseFollowing();
        following.current = false;
        correctScroll(root, () => target.scrollIntoView({ block: "center" }));
        readingAnchor.current = captureAnchor(root);
      });
      return () => cancelAnimationFrame(frame);
    }
    if (
      scopedAnchor &&
      !rows.some(
        (row) =>
          row.block?.trigger.message_id === (scopedAnchor.reply_to ?? scopedAnchor.message_id),
      ) &&
      state.value?.more &&
      !state.loading &&
      !state.error &&
      !error &&
      !loadingEarlier
    ) {
      const frame = requestAnimationFrame(() => void earlier());
      return () => cancelAnimationFrame(frame);
    }
  }, [
    targetMessage,
    scopedAnchor,
    scroll,
    rows,
    transcript,
    reading,
    correctScroll,
    state,
    error,
    loadingEarlier,
    earlier,
  ]);
  const jump = () => {
    onReadingMove();
    reading.followLatest();
    scroll.current?.scrollTo({ top: scroll.current.scrollHeight, behavior: "smooth" });
    setNewItems(false);
  };
  return (
    <div
      ref={activityRoot}
      className={styles.activity}
      onFocusCapture={(event) => {
        const row = event.target.closest<HTMLElement>("[data-scroll-id]");
        focusedActivity.current = row?.dataset.scrollId
          ? { node: event.target, id: row.dataset.scrollId }
          : null;
      }}
      onBlurCapture={() => {
        focusedActivity.current = null;
      }}
    >
      {state.value?.more && (
        <div
          ref={historyBoundary}
          className={styles.historyBoundary}
          data-history-boundary
          aria-busy={loadingEarlier}
        >
          {loadingEarlier && <span role="status">正在读取更早记录…</span>}
        </div>
      )}
      {error && (
        <p role="status" className={styles.error}>
          {error}
          <button onClick={() => void earlier()}>重试读取更早记录</button>
        </p>
      )}
      {state.error && (
        <p role="status" className={styles.error}>
          活动读取失败，已有记录仍保留。
          <button onClick={() => void reads.activity.refresh()}>重新读取</button>
        </p>
      )}
      {!state.value && !state.error && <p role="status">正在读取讨论与进展…</p>}
      {state.value && !rows.length && (
        <p className={styles.muted}>暂无讨论记录。可以在下方补充要求。</p>
      )}
      {activityGroups.map(({ entries: group, key, candidate }) => {
        const entry = group[0]!;
        return entry.block ? (
          <ActivityBlock
            key={`${historyEntry}:${entry.id}`}
            entry={entry}
            processReads={readable ? processReads : undefined}
            targetMessage={
              entry.block.trigger.message_id === scopedAnchor?.reply_to ? targetMessage : undefined
            }
            processTarget={
              entry.block.trigger.message_id === scopedAnchor?.reply_to &&
              (scopedAnchor?.kind === "tool_use" || scopedAnchor?.tool)
                ? targetMessage
                : undefined
            }
            scrollTarget={targetMessage}
            restored={continuations[entry.block.trigger.message_id]}
            rows={transcript.filter((row) => row.key.startsWith(`${entry.block!.position}:`))}
            onContinuation={rememberContinuation}
            onPagination={beginContinuation}
            onPaginationCancel={cancelContinuation}
            onProcessSnapshot={snapshotProcessLayout}
            onProcessLayout={restoreProcessLayout}
            onReadingMove={onReadingMove}
            scroll={scroll}
            currentIssueId={issue.issue_id}
          />
        ) : entry.admission ? (
          <ReceiptDetails key={entry.id} className={styles.admission} data-scroll-id={entry.id}>
            <Milestone
              entry={entry}
              results={results}
              onIssue={onIssue}
              onTask={onTask}
              groupedFirst
            />
          </ReceiptDetails>
        ) : candidate ? (
          <DeliveryCandidateActivity
            key={key}
            anchor={key}
            entries={group}
            newCandidate={candidate.newCandidate}
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

interface ProcessLayoutProps {
  change: object | null;
  scroll: RefObject<HTMLDivElement | null>;
  snapshot(node: HTMLDivElement): ProcessScrollPosition | null;
  restore(node: HTMLDivElement, position: ProcessScrollPosition): void;
  children: ReactNode;
}

class ProcessLayout extends Component<ProcessLayoutProps> {
  override getSnapshotBeforeUpdate(previous: Readonly<ProcessLayoutProps>) {
    const { change, scroll, snapshot } = this.props;
    // Read before the DOM changes, even if the browser has yet to emit scroll.
    return previous.change && change && change !== previous.change && scroll.current
      ? snapshot(scroll.current)
      : null;
  }

  override componentDidUpdate(
    _previous: Readonly<ProcessLayoutProps>,
    _state: unknown,
    position: ProcessScrollPosition | null,
  ) {
    const node = this.props.scroll.current;
    if (node && position) this.props.restore(node, position);
  }

  override render() {
    return this.props.children;
  }
}

function ActivityBlock({
  entry,
  targetMessage,
  processTarget,
  scrollTarget,
  restored,
  rows,
  onContinuation,
  onPagination,
  onPaginationCancel,
  onProcessSnapshot,
  onProcessLayout,
  onReadingMove,
  scroll,
  currentIssueId,
  processReads,
}: {
  entry: IssueActivityEntry;
  processReads?: IssueProcessReads | undefined;
  targetMessage?: string | undefined;
  processTarget?: string | undefined;
  scrollTarget?: string | undefined;
  restored?: { block: IssueTranscriptBlock; depth: number } | undefined;
  currentIssueId: string;
  rows: TranscriptEntry[];
  onContinuation(block: IssueTranscriptBlock, depth: number, explicit: boolean): void;
  onPagination(trigger: string, control: HTMLButtonElement, ids: string[]): void;
  onPaginationCancel(trigger: string): void;
  onProcessSnapshot(node: HTMLDivElement): ProcessScrollPosition | null;
  onProcessLayout(node: HTMLDivElement, position: ProcessScrollPosition): void;
  onReadingMove(): void;
  scroll: RefObject<HTMLDivElement | null>;
}) {
  const { client } = useServices();
  const readable = !!processReads;
  const continuation = useMemo(() => new IssueTurnRead(client, readable), [client, readable]);
  const extra = useStore(continuation.state);
  const explicit = useRef(false);
  const [opened, setOpened] = useEntryState(`activity-open:${entry.id}`, false);
  const [depth, setDepth] = useEntryState(`activity-depth:${entry.id}`, 2);
  const [restoredDepth] = useState(() => restored?.depth ?? 0);
  const block = entry.block!;
  const urgent = Boolean(block.trigger.issue_owner_steer);
  const replyAuthor = urgent ? undefined : entry.author;
  const replySourceLabel =
    urgent || block.trigger.author === "external"
      ? "回应来源消息"
      : block.trigger.author === "owner"
        ? "回复你的消息"
        : "回应系统通知";
  const triggerNode = useRef<HTMLDivElement>(null);
  const [traceOpen, setTraceOpen] = useEntryState(`activity-process-open:${entry.id}`, false);
  const [savedProcessDepth, saveProcessDepth] = useEntryState(
    `activity-process-depth:${entry.id}`,
    1,
  );
  const [process, setProcess] = useState<{
    value?: ProcessDetails;
    loading: boolean;
    error?: string;
  }>({ loading: false });
  const pendingProcess = useRef<Promise<ProcessDetails> | null>(null);
  const loadedProcess = useRef<ProcessDetails | null>(null);
  const requestedProcessDepth = useRef(savedProcessDepth);
  const requestProcess = useCallback(
    (depth = requestedProcessDepth.current) => {
      if (!processReads) return;
      const wanted = Math.max(depth, requestedProcessDepth.current);
      requestedProcessDepth.current = wanted;
      const loaded = loadedProcess.current;
      if (
        pendingProcess.current ||
        (loaded?.source === block && (loaded.depth >= wanted || !loaded.more))
      )
        return;
      setProcess((state) => ({ ...(state.value ? { value: state.value } : {}), loading: true }));
      const pending = processReads.load(block, "high", wanted);
      pendingProcess.current = pending;
      void pending.then(
        (value) => {
          if (pendingProcess.current === pending) {
            pendingProcess.current = null;
            loadedProcess.current = value;
            setProcess({ value, loading: false });
          }
        },
        (error: unknown) => {
          if (pendingProcess.current === pending) {
            pendingProcess.current = null;
            setProcess((state) => ({
              ...state,
              loading: false,
              error: error instanceof Error ? error.message : String(error),
            }));
          }
        },
      );
    },
    [block, processReads],
  );
  useEffect(
    () => () => {
      pendingProcess.current = null;
      loadedProcess.current = null;
    },
    [processReads],
  );
  useEffect(() => {
    if (traceOpen) requestProcess();
  }, [process.value, requestProcess, traceOpen]);
  useEffect(() => {
    if (process.value && process.value.depth > savedProcessDepth)
      saveProcessDepth(process.value.depth);
  }, [process.value, savedProcessDepth, saveProcessDepth]);
  useEffect(() => {
    if (!processTarget || process.error) return;
    const value = process.value;
    if (!value) requestProcess();
    else if (value.more && !value.items.some((item) => item.message_id === processTarget))
      requestProcess(value.depth + 1);
  }, [processTarget, process.error, process.value, requestProcess]);
  const processBlock = readable
    ? process.value
      ? {
          ...block,
          process: { ...block.process, items: process.value.items, more: false },
        }
      : block
    : (opened || targetMessage) && extra.value && !extra.error
      ? extra.value
      : (restored?.block ?? block);
  useEffect(() => {
    if (
      opened ||
      (targetMessage &&
        (readable ? !processTarget && block.more : block.more || block.process?.more))
    )
      void continuation.open(
        block.trigger.message_id,
        Math.max(depth, restoredDepth),
        targetMessage,
      );
  }, [opened, block, continuation, depth, restoredDepth, targetMessage, readable, processTarget]);
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
      onContinuation(extra.value, Math.max(continuation.depth(), restoredDepth), explicit.current);
      explicit.current = false;
    }
  }, [
    extra.value,
    extra.error,
    continuation,
    restoredDepth,
    onContinuation,
    onPaginationCancel,
    block.trigger.message_id,
  ]);
  const items = rows.flatMap((row) =>
    row.kind === "agent" ? row.items : row.kind === "system" ? [row.item] : [],
  );
  const [expanded, setExpanded] = useEntryState(`activity-expanded:${entry.id}`, false);
  const long = items.some((item) => item.author === "main_agent" && (item.body?.length ?? 0) > 400);
  const more = (control?: HTMLButtonElement) => {
    if (control) {
      explicit.current = true;
      onPagination(
        block.trigger.message_id,
        control,
        items.map((item) => item.message_id),
      );
    }
    if (extra.error && opened) {
      void continuation.refresh();
      return;
    }
    setDepth(continuation.nextDepth(Math.max(opened ? depth + 1 : depth, restoredDepth + 1)));
    if (!opened) setOpened(true);
  };
  const displayRows = activityTurnRows(rows, processBlock, readable);
  const firstReply = displayRows.find((row) => row.kind === "agent");
  const summaryError = process.value?.source === block ? undefined : processBlock.process?.error;
  const standaloneError = summaryError ?? (firstReply ? undefined : process.error);
  const discussion = (
    <article
      className={styles.discussion}
      data-scroll-id={entry.id}
      data-discussion-turn={entry.id}
    >
      {standaloneError && (
        <p role="status" className={styles.error}>
          {process.error ?? standaloneError}
          <button
            disabled={readable && process.loading}
            onClick={() => {
              if (readable) {
                requestProcess();
                setTraceOpen(true);
              } else if (!opened) setOpened(true);
              else void continuation.refresh();
            }}
          >
            重新读取公开过程
          </button>
        </p>
      )}
      {displayRows.map((row) => {
        switch (row.kind) {
          case "source":
            return (
              <div
                key={row.key}
                data-scroll-id={row.item.message_id}
                tabIndex={-1}
                ref={triggerNode}
              >
                <ExternalSourceMessage item={row.item} />
                <MessageReceipts item={row.item} />
              </div>
            );
          case "owner":
            return (
              <div
                key={row.key}
                ref={row.item.message_id === block.trigger.message_id ? triggerNode : undefined}
                tabIndex={-1}
                className={styles.owner}
              >
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
                <MessageReceipts item={row.item} />
              </div>
            );
          case "urgent":
            return (
              <div
                key={row.key}
                ref={row.item.message_id === block.trigger.message_id ? triggerNode : undefined}
                tabIndex={-1}
              >
                <UrgentMessage item={row.item} author={entry.author} />
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
                    {Array.from(replyAuthor || "Agent")[0]}
                  </span>
                  {replyAuthor || "Agent 身份未记录"}
                  {(urgent || ["owner", "external", "system"].includes(block.trigger.author)) &&
                    row.items.some(
                      (item) =>
                        item.kind === "message" &&
                        !item.tool &&
                        item.author === "main_agent" &&
                        item.reply_to === block.trigger.message_id,
                    ) && (
                      <button
                        type="button"
                        className={styles.textLink}
                        onClick={() => {
                          onReadingMove();
                          triggerNode.current?.focus({ preventScroll: true });
                          triggerNode.current?.scrollIntoView({ block: "center" });
                        }}
                      >
                        {replySourceLabel} #{block.trigger.seq}
                      </button>
                    )}
                  <time dateTime={row.items[0]?.created_at} title={row.items[0]?.created_at}>
                    {clock(Date.parse(row.items[0]?.created_at ?? block.trigger.created_at))}
                  </time>
                </div>
                <AgentTurn
                  entry={row}
                  targetMessage={scrollTarget}
                  processUnavailable={!!processBlock.process?.error}
                  process={
                    readable && row === firstReply
                      ? {
                          open: traceOpen,
                          ready: !!process.value,
                          activeTool: block.process?.active_tool,
                          latestActivity: null,
                          toggle: () => {
                            if (!traceOpen) requestProcess();
                            setTraceOpen(!traceOpen);
                          },
                          footer: (
                            <>
                              {process.loading && <p role="status">正在读取公开过程…</p>}
                              {process.error && !summaryError && (
                                <p role="status">
                                  公开过程读取失败
                                  <button onClick={() => requestProcess()}>重试</button>
                                </p>
                              )}
                              {!process.loading &&
                                !process.error &&
                                process.value &&
                                (process.value.more ? (
                                  <button onClick={() => requestProcess(process.value!.depth + 1)}>
                                    继续读取公开过程
                                  </button>
                                ) : (
                                  !process.value.items.length && (
                                    <p>本轮没有已保存的公开执行步骤。</p>
                                  )
                                ))}
                            </>
                          ),
                        }
                      : undefined
                  }
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
            return (
              <div
                key={row.key}
                ref={row.item.message_id === block.trigger.message_id ? triggerNode : undefined}
                tabIndex={-1}
              >
                <SystemMessage item={row.item} presentation="issue" />
              </div>
            );
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
  return (
    <ProcessLayout
      change={readable && traceOpen ? process : null}
      scroll={scroll}
      snapshot={onProcessSnapshot}
      restore={onProcessLayout}
    >
      {discussion}
    </ProcessLayout>
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
        <ActivityResultCard artifactID={entry.artifact_id} result={result} onTask={onTask} />
      </div>
    );
  return (
    <div
      className={styles.milestone}
      tabIndex={-1}
      data-tone={/failed|failure/.test(entry.kind) ? "error" : undefined}
      data-scroll-id={groupedFirst ? undefined : entry.id}
    >
      <div className={styles.milestoneLine} data-activity-event-heading>
        <span>
          <span>{entry.author || "tend"}</span> <span>{entry.title}</span>
          <time title={new Date(entry.created_at).toISOString()}> · {clock(entry.created_at)}</time>
        </span>
        {entry.task_id ? (
          <button className={styles.textLink} onClick={() => onTask(entry.task_id!)}>
            查看 →
          </button>
        ) : entry.issue_id &&
          entry.kind !== "issue.progress_updated" &&
          entry.kind !== "issue.delivery_review_changed" &&
          entry.kind !== "work_item.delivery_changed" ? (
          <button className={styles.textLink} onClick={() => onIssue(entry.issue_id!)}>
            查看 →
          </button>
        ) : null}
      </div>
      {entry.delivery ? (
        <DeliveryProgress item={entry.delivery} presentation="observation" />
      ) : null}
      {entry.issue_id &&
      (entry.kind === "issue.delivery_review_changed" ||
        entry.kind === "work_item.delivery_changed" ||
        entry.kind === "task.completed") ? (
        <EventNotification issueID={entry.issue_id} position={entry.position} />
      ) : null}
      {entry.progress && (
        <details>
          <summary>
            {entry.progress.delivery?.conflict_action
              ? conflictActionLabel(entry.progress.delivery.conflict_action)
              : entry.progress.summary ||
                (entry.progress.next_step ? "更新了下一步" : "清除了进展")}
          </summary>
          {entry.progress.delivery ? (
            <p>
              {candidateLabel(entry.progress.delivery)} ·{" "}
              <CandidateIdentity candidate={entry.progress.delivery} />
            </p>
          ) : null}
          {entry.progress.delivery?.conflict_action && entry.progress.summary ? (
            <p>{entry.progress.summary}</p>
          ) : null}
          <p>记录版本：{entry.progress.issue_revision}</p>
          {entry.progress.next_step && <p>当时下一步：{entry.progress.next_step}</p>}
        </details>
      )}
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
        <span>
          {first.author || "tend"} · {entries.length} 项活动 ·{" "}
          {[...new Set(entries.map((entry) => entry.title))].join("、")}
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
