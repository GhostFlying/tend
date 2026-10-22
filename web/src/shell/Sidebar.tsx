// The work overview: what needs the owner, tasks by project and the hosts.
// The sections are empty frames until the views are filled in (F7).
import type { Connection } from "../api/client.ts";
import { useStore } from "../state/store.ts";
import { theme, themeLabels, toggleTheme } from "../theme/theme.ts";
import { ConnectionBadge } from "./ConnectionBadge.tsx";
import controls from "./controls.module.css";
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
      <div className={styles.scroll}>
        <section className={styles.decisions} aria-label="需要你处理">
          <div className={styles.sectionTitle}>需要你处理</div>
          <p className={controls.empty}>这里会列出待决事项和被接管的任务。</p>
        </section>
        <section className={styles.tasks} aria-label="任务">
          <div className={styles.sectionTitle}>任务</div>
          <p className={controls.empty}>这里会按 project 列出任务。</p>
        </section>
      </div>
      <div className={styles.foot}>
        <div className={styles.hosts}>host 的状态会显示在这里。</div>
        <div className={styles.footActions}>
          <button type="button" className={controls.chipBtn} onClick={toggleTheme}>
            主题：{themeLabels[choice]}
          </button>
        </div>
      </div>
    </div>
  );
}
