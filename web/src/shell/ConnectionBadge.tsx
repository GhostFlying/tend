import { useEffect, useId, useRef, useState } from "react";
import type { Connection } from "../api/client.ts";
import styles from "./ConnectionBadge.module.css";
import { StatusDot, type Tone } from "./StatusDot.tsx";

const look: Record<Connection["status"], { tone: Tone; label: string }> = {
  connecting: { tone: "idle", label: "正在连接" },
  online: { tone: "ok", label: "已连接" },
  offline: { tone: "warn", label: "正在重连" },
  "not-owner": { tone: "err", label: "不是 owner" },
};

function describe(connection: Exclude<Connection, { status: "offline" }>): string {
  switch (connection.status) {
    case "online":
      return connection.login ? `已登录：${connection.login}` : "已连接";
    case "not-owner":
      return connection.reason;
    case "connecting":
      return "正在按这台设备的 tailnet 身份登录";
  }
}

/** The link to tend; compact shows only the dot. */
export function ConnectionBadge({
  connection,
  compact = false,
}: {
  connection: Connection;
  compact?: boolean;
}) {
  const { tone, label } = look[connection.status];
  const offline = connection.status === "offline";
  return (
    <>
      <span
        className={offline ? styles.hidden : styles.badge}
        title={offline ? undefined : describe(connection)}
        role="status"
        aria-label={label}
      >
        {offline ? null : <StatusDot tone={tone} />}
        {compact && !offline ? <span className={styles.hidden}>{label}</span> : label}
      </span>
      {offline ? <ReconnectBadge reason={connection.reason} compact={compact} /> : null}
    </>
  );
}

type OpenBy = "hover" | "focus" | "activation";

function ReconnectBadge({ reason, compact }: { reason: string | undefined; compact: boolean }) {
  const { tone, label } = look.offline;
  const id = useId();
  const root = useRef<HTMLSpanElement>(null);
  const [openBy, setOpenBy] = useState<OpenBy | null>(null);
  const open = openBy !== null;
  const detail = reason?.trim();

  useEffect(() => {
    if (!open) return;
    function dismissOnEscape(event: KeyboardEvent) {
      if (event.key === "Escape") setOpenBy(null);
    }
    function dismissOutside(event: PointerEvent) {
      if (event.target instanceof Node && !root.current?.contains(event.target)) setOpenBy(null);
    }
    document.addEventListener("keydown", dismissOnEscape);
    document.addEventListener("pointerdown", dismissOutside);
    return () => {
      document.removeEventListener("keydown", dismissOnEscape);
      document.removeEventListener("pointerdown", dismissOutside);
    };
  }, [open]);

  return (
    <span
      ref={root}
      className={styles.anchor}
      onPointerEnter={(event) => {
        if (event.pointerType === "mouse") setOpenBy((current) => current ?? "hover");
      }}
      onPointerLeave={(event) => {
        if (event.pointerType === "mouse") {
          setOpenBy((current) => (current === "hover" ? null : current));
        }
      }}
    >
      <button
        type="button"
        className={`${styles.badge} ${styles.trigger}`}
        aria-label={label}
        aria-expanded={open}
        aria-controls={id}
        aria-describedby={id}
        onFocus={(event) => {
          if (event.currentTarget.matches(":focus-visible")) setOpenBy("focus");
        }}
        onBlur={() => setOpenBy((current) => (current === "hover" ? current : null))}
        onClick={() => setOpenBy((current) => (current === null ? "activation" : null))}
      >
        <StatusDot tone={tone} />
        {compact ? null : label}
      </button>
      <span id={id} className={styles.panelPosition} hidden={!open}>
        <span className={styles.panel}>{detail ? `浏览器观察：${detail}` : "原因未知"}</span>
      </span>
    </span>
  );
}
