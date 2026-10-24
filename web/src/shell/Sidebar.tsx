import { RepositoryEntry } from "../repos/Repositories.tsx";
// The work overview: what needs the owner, tasks by project and the hosts.
import type { Connection } from "../api/client.ts";
import { Attention } from "../decisions/Attention.tsx";
import { useStore } from "../state/store.ts";
import { theme, themeLabels, toggleTheme } from "../theme/theme.ts";
import { Note } from "./Chat.tsx";
import { ConnectionBadge } from "./ConnectionBadge.tsx";
import { WorkItemOverview } from "../workitems/Board.tsx";
import { RecentTasks } from "../tasks/Board.tsx";
import controls from "./controls.module.css";
import { TaskOverview } from "../tasks/Overview.tsx";
import { Hosts } from "../hosts/Hosts.tsx";
import styles from "./Sidebar.module.css";
import type { View } from "./TabBar.tsx";

export function Sidebar({
  view,
  connection,
  onBoard,
  onRecords,
  onSettings,
  onRepos,
}: {
  view: View;
  connection: Connection;
  onBoard(): void;
  onRecords(): void;
  onSettings(): void;
  onRepos(): void;
}) {
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
      <h2 className={styles.mobileTitle}>{view === "decisions" ? "待处理" : "工作项"}</h2>
      <div className={styles.mobileNotice}>
        <Note />
      </div>
      <div className={styles.scroll}>
        <div className={styles.decisions}>
          <Attention />
        </div>
        <section className={styles.tasks} aria-label="任务">
          <button
            type="button"
            className={`${controls.chipBtn} ${styles.allTasks}`}
            onClick={onBoard}
          >
            工作项 · 白板 ›
          </button>
          <WorkItemOverview />
          <details>
            <summary>执行任务与记录</summary>
            <button type="button" className={controls.chipBtn} onClick={onRecords}>
              执行记录
            </button>
            <TaskOverview />
            <RecentTasks />
          </details>
          <RepositoryEntry onOpen={onRepos} />
          <Hosts connection={connection} />
        </section>
      </div>
      <div className={styles.foot}>
        <div className={styles.footActions}>
          <button type="button" className={styles.themeButton} onClick={onSettings}>
            设置
          </button>
          <button type="button" className={styles.themeButton} onClick={toggleTheme}>
            主题：{themeLabels[choice]}
          </button>
        </div>
      </div>
    </div>
  );
}
