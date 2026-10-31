import { buildTranscriptEntries, type TranscriptEntry } from "./transcriptBlocks.ts";
import { notify } from "../app/notice.ts";
// The scrolling conversation. It opens at the latest page and follows new
// content only while the reader is near the bottom; otherwise it offers a
// jump to the latest. Scrolling near the top reads the page before, and
// the reading position stays where it was.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useServices } from "../app/services.tsx";
import { noOutbox } from "../queue/sendQueue.ts";
import { ArrowDown } from "../shell/icons.tsx";
import { StatusDot } from "../shell/StatusDot.tsx";
import { useStore } from "../state/store.ts";
import { AgentTurn, Divider, OutgoingRow, OwnerMessage, SystemMessage } from "./Messages.tsx";
import { buildEntries, duration, processing } from "./model.ts";
import styles from "./Transcript.module.css";
import { captureAnchor, restoreAnchor, type ScrollAnchor } from "./scrollAnchor.ts";

/** How close to the bottom still counts as reading the latest. */
const nearBottomPx = 80;
/** How close to the top starts reading the page before. */
const nearTopPx = 300;

const emptyNote =
  "这里是和主 Agent 的对话。发出的消息先存在这台设备上，tend 收到后才算发出；" +
  "断网或关掉页面也不会丢。";

export function Transcript({ visible }: { visible: boolean }) {
  const { live, queue } = useServices();
  const scroller = useRef<HTMLDivElement>(null);
  const raw = useStore(live.conversation);
  const blocks = useStore(live.queueView.state);
  const conversation = blocks.supported
    ? {
        ...raw,
        loaded: blocks.loaded,
        more: blocks.more,
        loadingEarlier: blocks.loadingEarlier,
        error: blocks.error,
      }
    : raw;
  const outbox = useStore(queue?.list ?? noOutbox);
  const entries = useMemo<TranscriptEntry[]>(
    () =>
      blocks.supported === undefined
        ? []
        : blocks.supported
          ? buildTranscriptEntries(blocks.blocks)
          : buildEntries(conversation.items, outbox),
    [blocks.supported, blocks.blocks, conversation.items, outbox],
  );
  const active = blocks.pages.owner?.active;
  const busy =
    active?.status === "processing"
      ? active.item
      : blocks.supported
        ? Object.values(blocks.pages)
            .flatMap((p) => p.items ?? [])
            .find((e) => e.status === "processing")?.item
        : processing(conversation.items);

  const [atBottom, setAtBottom] = useState(true);
  const following = useRef(true);
  const wasVisible = useRef(visible);
  const readingAnchor = useRef<ScrollAnchor | null>(null);
  const preserveContentResize = useRef(false);
  const preserveContentResizeTimer = useRef<number | undefined>(undefined);
  /** Where the reader was when the page before was asked for. */
  const anchor = useRef<ScrollAnchor | null>(null);

  const loadEarlier = () => {
    const el = scroller.current;
    if (!visible || !el || !conversation.more || conversation.loadingEarlier || anchor.current) {
      return;
    }
    anchor.current = captureAnchor(el);
    void (blocks.supported ? live.queueView.earlier() : live.loadEarlier());
  };

  const onScroll = () => {
    const el = scroller.current;
    if (!visible || !el || el.clientHeight === 0) return;
    const near = el.scrollHeight - el.scrollTop - el.clientHeight < nearBottomPx;
    if (near) anchor.current = null;
    else if (anchor.current) anchor.current = captureAnchor(el);
    following.current = near;
    setAtBottom(near);
    readingAnchor.current = captureAnchor(el);
    if (el.scrollTop < nearTopPx) {
      loadEarlier();
    }
  };

  useLayoutEffect(() => {
    const el = scroller.current;
    if (!visible || !el || el.clientHeight === 0) {
      wasVisible.current = false;
      return;
    }
    if (!wasVisible.current && !following.current && readingAnchor.current)
      restoreAnchor(el, readingAnchor.current);
    wasVisible.current = true;
    const a = anchor.current;
    if (a) {
      // Appended live output must not count as prepended page height.
      restoreAnchor(el, a);
      if (!conversation.loadingEarlier) anchor.current = null;
    } else if (following.current) {
      el.scrollTop = el.scrollHeight;
    }
    readingAnchor.current = captureAnchor(el);
  });

  useEffect(() => {
    // A first page too short to scroll cannot be scrolled up to load more.
    const el = scroller.current;
    if (
      visible &&
      el &&
      el.clientHeight > 0 &&
      conversation.loaded &&
      el.scrollHeight <= el.clientHeight
    ) {
      loadEarlier();
    }
  });

  useEffect(() => {
    const el = scroller.current;
    if (!visible || !el) return;
    const update = (entries: ResizeObserverEntry[] = []) => {
      if (el.clientHeight === 0) {
        setAtBottom(true);
        return;
      }
      const contentResized = entries.some((entry) => entry.target !== el);
      // Focus can remain inside the transcript while the viewport changes.
      // Only a captured control activation proves that existing content grew
      // at the reader's request; focus alone must not stop following output.
      const userExpandedContent = contentResized && preserveContentResize.current;
      if (contentResized && preserveContentResize.current) {
        preserveContentResize.current = false;
        window.clearTimeout(preserveContentResizeTimer.current);
        preserveContentResizeTimer.current = undefined;
      }
      if (following.current && !userExpandedContent) {
        el.scrollTop = el.scrollHeight;
        setAtBottom(true);
      } else {
        const near = el.scrollHeight - el.scrollTop - el.clientHeight < nearBottomPx;
        if (near) anchor.current = null;
        following.current = near;
        setAtBottom(near);
      }
      readingAnchor.current = captureAnchor(el);
    };
    const observer = new ResizeObserver(update);
    observer.observe(el);
    if (el.firstElementChild) observer.observe(el.firstElementChild);
    update();
    return () => {
      observer.disconnect();
      window.clearTimeout(preserveContentResizeTimer.current);
      preserveContentResize.current = false;
      preserveContentResizeTimer.current = undefined;
    };
  }, [visible]);

  const toLatest = useCallback(() => {
    const el = scroller.current;
    if (el) {
      anchor.current = null;
      following.current = true;
      setAtBottom(true);
      el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
    }
  }, []);

  return (
    <div className={styles.wrap}>
      <div
        className={styles.transcript}
        ref={scroller}
        onScroll={onScroll}
        onClickCapture={() => {
          // A control inside an existing message can grow that message. Keep
          // the control in view instead of treating the resize as new output.
          preserveContentResize.current = true;
          window.clearTimeout(preserveContentResizeTimer.current);
          preserveContentResizeTimer.current = window.setTimeout(() => {
            preserveContentResize.current = false;
            preserveContentResizeTimer.current = undefined;
          }, 500);
        }}
        role="region"
        aria-label="对话记录"
      >
        <div className={styles.lane}>
          {conversation.loadingEarlier && <div className={styles.note}>正在读取更早的消息…</div>}
          {conversation.loaded && !conversation.more && entries.length > 0 && (
            <div className={styles.note}>对话从这里开始</div>
          )}
          {entries.length === 0 && (
            <p className={styles.empty}>
              {conversation.loaded || conversation.error ? emptyNote : "正在读取对话…"}
            </p>
          )}
          {entries.map((e) => {
            switch (e.kind) {
              case "divider":
                return <Divider key={e.key} at={e.at} />;
              case "system":
                return <SystemMessage key={e.key} item={e.item} scrollID={e.key} />;
              case "owner":
                return <OwnerMessage key={e.key} item={e.item} />;
              case "outgoing":
                return <OutgoingRow key={e.key} message={e.message} queue={queue} />;
              case "agent":
                return <AgentTurn key={e.key} entry={e} />;
              case "block-more":
                return (
                  <button
                    key={e.key}
                    onClick={() =>
                      void live.queueView
                        .moreBlock(e.block)
                        .catch((error) => notify(String(error), "warn"))
                    }
                  >
                    继续读取本轮消息
                  </button>
                );
            }
          })}
          {busy && (
            <Working
              since={
                conversation.processingSince.get(busy.message_id) ?? Date.parse(busy.created_at)
              }
            />
          )}
        </div>
      </div>
      {!atBottom && (
        <button type="button" className={styles.jump} onClick={toLatest} aria-label="跳到最新">
          <ArrowDown />
          跳到最新
        </button>
      )}
    </div>
  );
}

/** "正在处理" with the time since the turn started. */
function Working({ since }: { since: number }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1_000);
    return () => clearInterval(t);
  }, []);
  return (
    <div className={styles.working} role="status">
      <StatusDot tone="run" />
      <span className={styles.shimmer}>正在处理…</span>
      <span className={styles.elapsed}>· {duration(now - since)}</span>
    </div>
  );
}
