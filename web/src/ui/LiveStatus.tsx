import { useEffect, useState, type ReactNode } from "react";
import { activitySummary, elapsedLabel, type ActivityStatus } from "./activityStatus.ts";
import styles from "./LiveStatus.module.css";

/** The polite region contains only the stage; timer ticks are not announcements. */
export function LiveStatus({ status, children }: { status: ActivityStatus; children?: ReactNode }) {
  const [clock, setClock] = useState(() => ({ now: Date.now(), observedLive: status.active }));
  useEffect(() => {
    if (!status.active) return;
    const timer = setInterval(() => setClock({ now: Date.now(), observedLive: true }), 1000);
    return () => clearInterval(timer);
  }, [status.active]);
  return (
    <span
      className={styles.line}
      data-active={status.active}
      data-tone={status.tone}
      data-disclosure={!!children}
    >
      <span className={styles.indicator} aria-hidden="true">
        {status.active
          ? "◌"
          : status.tone === "error"
            ? "!"
            : status.tone === "warning"
              ? "·"
              : "✓"}
      </span>
      <span className={styles.label} role="status" aria-live="polite" aria-atomic="true">
        {status.active
          ? status.label
          : children
            ? status.label
            : activitySummary(status, clock.observedLive ? clock.now : NaN)}
      </span>
      {children}
      {!!children &&
        !status.active &&
        status.startedAt !== null &&
        (status.endedAt !== null || clock.observedLive) && (
          <span className={styles.timer}>
            · 用时 {elapsedLabel((status.endedAt ?? clock.now) - status.startedAt)}
          </span>
        )}
      {status.active && status.startedAt !== null && (
        <span className={styles.timer} aria-hidden="true">
          {elapsedLabel(clock.now - status.startedAt)}
        </span>
      )}
    </span>
  );
}
