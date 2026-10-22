// The scrolling conversation. It opens at the latest page and follows new
// content only while the reader is near the bottom; otherwise it offers a
// jump to the latest. Scrolling near the top reads the page before, and
// the reading position stays where it was.
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useServices } from "../app/services.tsx";
import { noOutbox } from "../queue/sendQueue.ts";
import { ArrowDown } from "../shell/icons.tsx";
import { StatusDot } from "../shell/StatusDot.tsx";
import { useStore } from "../state/store.ts";
import { AgentTurn, Divider, OutgoingRow, OwnerMessage } from "./Messages.tsx";
import { buildEntries, duration, processing } from "./model.ts";
import styles from "./Transcript.module.css";

/** How close to the bottom still counts as reading the latest. */
const nearBottomPx = 80;
/** How close to the top starts reading the page before. */
const nearTopPx = 300;

const emptyNote =
  "这里是和主 Agent 的对话。发出的消息先存在这台设备上，tend 收到后才算发出；" +
  "断网或关掉页面也不会丢。";

/** jump counts requests to jump to the latest, such as the header's button. */
export function Transcript({ jump }: { jump: number }) {
  const { live, queue } = useServices();
  const scroller = useRef<HTMLDivElement>(null);
  const conversation = useStore(live.conversation);
  const outbox = useStore(queue?.list ?? noOutbox);
  const entries = useMemo(
    () => buildEntries(conversation.items, outbox),
    [conversation.items, outbox],
  );
  const busy = processing(conversation.items);

  const [atBottom, setAtBottom] = useState(true);
  const following = useRef(true);
  /** Where the reader was when the page before was asked for. */
  const anchor = useRef<{ firstSeq: number; height: number; top: number } | null>(null);
  const firstSeq = conversation.items[0]?.seq ?? 0;

  const loadEarlier = () => {
    const el = scroller.current;
    if (!el || !conversation.more || conversation.loadingEarlier || anchor.current) {
      return;
    }
    anchor.current = { firstSeq, height: el.scrollHeight, top: el.scrollTop };
    void live.loadEarlier().finally(() => {
      // Nothing came: let the next scroll try again.
      if (anchor.current?.firstSeq === live.conversation.get().items[0]?.seq) {
        anchor.current = null;
      }
    });
  };

  const onScroll = () => {
    const el = scroller.current;
    if (!el) {
      return;
    }
    const near = el.scrollHeight - el.scrollTop - el.clientHeight < nearBottomPx;
    following.current = near;
    setAtBottom(near);
    if (el.scrollTop < nearTopPx) {
      loadEarlier();
    }
  };

  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) {
      return;
    }
    const a = anchor.current;
    if (a && a.firstSeq !== firstSeq) {
      // Earlier items went in above: keep what was on screen in place.
      el.scrollTop = el.scrollHeight - a.height + a.top;
      anchor.current = null;
    } else if (following.current) {
      el.scrollTop = el.scrollHeight;
    }
  });

  useEffect(() => {
    // A first page too short to scroll cannot be scrolled up to load more.
    const el = scroller.current;
    if (el && conversation.loaded && el.scrollHeight <= el.clientHeight) {
      loadEarlier();
    }
  });

  const toLatest = () => {
    const el = scroller.current;
    if (el) {
      following.current = true;
      el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
    }
  };

  useEffect(() => {
    const el = scroller.current;
    if (jump > 0 && el) {
      following.current = true;
      el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
    }
  }, [jump]);

  return (
    <div className={styles.wrap}>
      <div className={styles.transcript} ref={scroller} onScroll={onScroll}>
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
              case "owner":
                return <OwnerMessage key={e.key} item={e.item} />;
              case "outgoing":
                return <OutgoingRow key={e.key} message={e.message} queue={queue} />;
              case "agent":
                return <AgentTurn key={e.key} entry={e} />;
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
        <button type="button" className={styles.jump} onClick={toLatest}>
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
