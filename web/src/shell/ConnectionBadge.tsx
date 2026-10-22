import type { Connection } from "../api/client.ts";
import styles from "./ConnectionBadge.module.css";
import { StatusDot, type Tone } from "./StatusDot.tsx";

const look: Record<Connection["status"], { tone: Tone; label: string }> = {
  connecting: { tone: "idle", label: "正在连接" },
  online: { tone: "ok", label: "已连接" },
  offline: { tone: "warn", label: "正在重连" },
  "not-owner": { tone: "err", label: "不是 owner" },
};

function describe(c: Connection): string {
  switch (c.status) {
    case "online":
      return c.login ? `已登录：${c.login}` : "已连接";
    case "offline":
      return `${c.reason}，正在重连`;
    case "not-owner":
      return c.reason;
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
  return (
    <span className={styles.badge} title={describe(connection)} role="status">
      <StatusDot tone={tone} />
      {compact ? <span className={styles.hidden}>{label}</span> : label}
    </span>
  );
}
