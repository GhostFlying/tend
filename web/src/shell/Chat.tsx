// The one conversation with the main agent (D27): the transcript, and the
// seat docked at the bottom with notes and the composer.
import { useState } from "react";
import type { Connection } from "../api/client.ts";
import { dismissNotice, notice } from "../app/notice.ts";
import { Composer } from "../conversation/Composer.tsx";
import { Transcript } from "../conversation/Transcript.tsx";
import { DecisionDock } from "../decisions/DecisionDock.tsx";
import { theme, themeLabels, toggleTheme } from "../theme/theme.ts";
import { useStore } from "../state/store.ts";
import styles from "./Chat.module.css";
import { ConnectionBadge } from "./ConnectionBadge.tsx";
import controls from "./controls.module.css";
import { ArrowDown, Gear } from "./icons.tsx";

export function Chat({
  connection,
  visible,
  onSettings,
}: {
  connection: Connection;
  visible: boolean;
  onSettings(): void;
}) {
  const choice = useStore(theme);
  const [jump, setJump] = useState(0);
  const toLatest = () => setJump((n) => n + 1);

  return (
    <>
      <header className={styles.head}>
        <h1>主 Agent</h1>
        <span className={styles.sub}>一个会话，所有设备共享</span>
        <span className={styles.spacer} />
        <button
          type="button"
          className={controls.chipBtn}
          onClick={toggleTheme}
          aria-label={`切换主题：${themeLabels[choice]}`}
        >
          {themeLabels[choice]}
        </button>
        <button
          type="button"
          className={controls.iconBtn}
          onClick={onSettings}
          aria-label="设置"
          title="设置"
        >
          <Gear />
        </button>
        <span className={styles.mobileOnly}>
          <ConnectionBadge connection={connection} compact />
        </span>
        <button
          type="button"
          className={controls.iconBtn}
          onClick={toLatest}
          title="跳到最新"
          aria-label="跳到最新"
        >
          <ArrowDown />
        </button>
      </header>
      <Transcript jump={jump} visible={visible} />
      <div className={styles.seat}>
        <div className={styles.seatInner}>
          <Note />
          <DecisionDock />
          <Composer connection={connection} />
        </div>
      </div>
    </>
  );
}

export function Note() {
  const n = useStore(notice);
  if (!n) {
    return null;
  }
  return (
    <div className={styles.note} data-tone={n.tone} role="status">
      <span>{n.text}</span>
      <button type="button" className={controls.link} onClick={dismissNotice}>
        知道了
      </button>
    </div>
  );
}
