// The scrolling conversation. It opens at the latest page and follows new
// content only while the reader is near the bottom; otherwise it offers a
// jump to the latest. Scrolling near the top reads the page before, and
// the reading position stays where it was.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useServices } from "../app/services.tsx";
import { noOutbox } from "../queue/sendQueue.ts";
import { ArrowDown } from "../shell/icons.tsx";
import { LiveStatus } from "../ui/LiveStatus.tsx";
import { conversationStatus } from "../ui/activityStatus.ts";
import { useStore } from "../state/store.ts";
import {
  ChatReplies,
  Divider,
  OutgoingRow,
  OwnerMessage,
  UrgentMessage,
  SystemMessage,
} from "./Messages.tsx";
import { buildEntries } from "./model.ts";
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
  const conversation = useStore(live.conversation);
  const queueState = useStore(live.queueView.state);
  const owner = queueState.pages.owner;
  const fallbackOutbox = useStore(
    queueState.supported === false ? (queue?.list ?? noOutbox) : noOutbox,
  );
  const entries = useMemo(
    () =>
      buildEntries(
        conversation.items.filter(
          (item) =>
            !item.control?.withdrawn_at &&
            (item.author !== "system" ||
              !item.choices?.length ||
              item.choices.some((choice) => choice.status === "open")),
        ),
        fallbackOutbox,
      ),
    [conversation.items, fallbackOutbox],
  );
  const active = owner?.active;
  const busy =
    owner?.interruptible_message_id ??
    (active?.status === "processing" ? active.item.message_id : undefined);
  const since =
    owner?.processing_started_at ??
    (busy && conversation.processingSince.get(busy)) ??
    (active ? Date.parse(active.item.created_at) : null);

  const [atBottom, setAtBottom] = useState(true);
  const following = useRef(true);
  const scrollportSize = useRef({ width: 0, height: 0 });
  const wasVisible = useRef(visible);
  const contentKey = JSON.stringify(entries);
  const previousContent = useRef("");
  const readingAnchor = useRef<ScrollAnchor | null>(null);
  /** Where the reader was when the page before was asked for. */
  const anchor = useRef<ScrollAnchor | null>(null);

  const loadEarlier = () => {
    const el = scroller.current;
    if (!visible || !el || !conversation.more || conversation.loadingEarlier || anchor.current) {
      return;
    }
    anchor.current = captureAnchor(el);
    void live.loadEarlier();
  };

  const onScroll = () => {
    const el = scroller.current;
    if (!visible || !el || el.clientHeight === 0) return;
    if (
      el.clientWidth !== scrollportSize.current.width ||
      el.clientHeight !== scrollportSize.current.height
    ) {
      scrollportSize.current = { width: el.clientWidth, height: el.clientHeight };
      if (following.current) el.scrollTop = el.scrollHeight;
      readingAnchor.current = captureAnchor(el);
      return;
    }
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
    const entered = !wasVisible.current;
    if (entered && !following.current && readingAnchor.current)
      restoreAnchor(el, readingAnchor.current);
    wasVisible.current = true;
    const a = anchor.current;
    if (a) {
      // Appended live output must not count as prepended page height.
      restoreAnchor(el, a);
      if (!conversation.loadingEarlier) anchor.current = null;
    } else if (following.current && (previousContent.current !== contentKey || entered)) {
      el.scrollTop = el.scrollHeight;
    } else if (
      !following.current &&
      previousContent.current !== contentKey &&
      readingAnchor.current
    ) {
      restoreAnchor(el, readingAnchor.current);
    }
    previousContent.current = contentKey;
    readingAnchor.current = captureAnchor(el);
  }, [contentKey, visible, conversation.loadingEarlier]);

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
    const update = () => {
      scrollportSize.current = { width: el.clientWidth, height: el.clientHeight };
      if (el.clientHeight === 0) {
        setAtBottom(true);
        return;
      }
      if (following.current) {
        el.scrollTop = el.scrollHeight;
        setAtBottom(true);
      }
      readingAnchor.current = captureAnchor(el);
    };
    let disclosureFrame = 0;
    const disclosure = (event: MouseEvent) => {
      if (
        !(event.target instanceof Element) ||
        !event.target.closest("summary, button[aria-expanded]")
      )
        return;
      const viewportWidth = innerWidth;
      const viewportHeight = innerHeight;
      const width = el.clientWidth;
      const height = el.clientHeight;
      cancelAnimationFrame(disclosureFrame);
      disclosureFrame = requestAnimationFrame(() => {
        // A viewport resize is not a request to stop following new output.
        if (
          el.clientWidth !== width ||
          el.clientHeight !== height ||
          innerWidth !== viewportWidth ||
          innerHeight !== viewportHeight
        ) {
          update();
          return;
        }
        const near = el.scrollHeight - el.scrollTop - el.clientHeight < nearBottomPx;
        following.current = near;
        setAtBottom(near);
        readingAnchor.current = captureAnchor(el);
      });
    };
    el.addEventListener("click", disclosure, true);
    const observer = new ResizeObserver(update);
    observer.observe(el);
    update();
    return () => {
      observer.disconnect();
      cancelAnimationFrame(disclosureFrame);
      el.removeEventListener("click", disclosure, true);
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
              case "urgent":
                return <UrgentMessage key={e.key} item={e.item} />;
              case "outgoing":
                return <OutgoingRow key={e.key} message={e.message} queue={queue} />;
              case "agent":
                return <ChatReplies key={e.key} items={e.items} />;
            }
          })}
          {busy && <Working since={typeof since === "number" ? since : null} />}
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

function Working({ since }: { since: number | null }) {
  return <LiveStatus status={conversationStatus([], "processing", since)} />;
}
