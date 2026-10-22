// The work overview: what needs the owner, tasks by project and the hosts.
import type { Connection } from "../api/client.ts";
import { Attention } from "../decisions/Attention.tsx";
import { useStore } from "../state/store.ts";
import { theme, themeLabels, toggleTheme } from "../theme/theme.ts";
import { Note } from "./Chat.tsx";
import { ConnectionBadge } from "./ConnectionBadge.tsx";
import { Hosts, TaskOverview } from "../tasks/Overview.tsx";
import styles from "./Sidebar.module.css";
import type { View } from "./TabBar.tsx";

export function Sidebar({ view, connection }: { view: View; connection: Connection }) {
  const choice = useStore(theme);
  return (
    <div className={styles.root} data-view={view}>
      <div className={styles.brand}>
        <div className={styles.logo} aria-hidden>
          t
        </div>
        <b>tend</b>
        <span className={styles.conn}>
          <ConnectionBadge connection={connection} />
        </span>
      </div>
      <h2 className={styles.mobileTitle}>{view === "decisions" ? "待处理" : "任务"}</h2>
      <div className={styles.mobileNotice}>
        <Note />
      </div>
      <div className={styles.scroll}>
        <div className={styles.decisions}>
          <Attention />
        </div>
        <section className={styles.tasks} aria-label="任务">
          <TaskOverview />
          <Hosts />
        </section>
      </div>
      <div className={styles.foot}>
        <div className={styles.footActions}>
          <button type="button" className={styles.themeButton} onClick={toggleTheme}>
            主题：{themeLabels[choice]}
          </button>
        </div>
      </div>
    </div>
  );
}
