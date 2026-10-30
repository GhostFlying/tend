import { ResourceNavigation, ResourceHosts } from "./Resources.tsx";
// Work items are the primary navigation; execution records have a separate view.
import type { Connection } from "../api/client.ts";
import type { AttentionPage } from "../api/generated/api.ts";
import type { ReadState } from "../workitems/live.ts";
import { NextSummary } from "../attention/NextSummary.tsx";
import { useStore } from "../state/store.ts";
import { theme, themeLabels, toggleTheme } from "../theme/theme.ts";
import { Note } from "./Chat.tsx";
import { ConnectionBadge } from "./ConnectionBadge.tsx";
import { RecentTasks } from "../tasks/Board.tsx";
import { IssueOverview } from "../issues/Overview.tsx";
import controls from "./controls.module.css";
import styles from "./Sidebar.module.css";
import type { View } from "./TabBar.tsx";

export function Sidebar({
  view,
  records,
  connection,
  onChat,
  onBoard,
  onRecords,
  onSettings,
  onRepos,
  mainAttention,
  onInbox,
  onIssue,
}: {
  view: View;
  records: boolean;
  connection: Connection;
  onChat(): void;
  onBoard(): void;
  onRecords(): void;
  onSettings(): void;
  onRepos(): void;
  mainAttention: ReadState<AttentionPage>;
  onInbox(id?: string): void;
  onIssue(id: string): void;
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
      <nav className={styles.primaryNav} aria-label="主导航">
        <button onClick={onChat}>主对话</button>
        <button onClick={() => onInbox()}>收件箱</button>
      </nav>
      <h2 className={styles.mobileTitle}>{view === "decisions" ? "待处理" : "Issue"}</h2>
      <div className={styles.mobileNotice}>
        <Note />
      </div>
      <div className={styles.scroll}>
        <div className={styles.decisions}>
          <NextSummary onOpen={onInbox} onIssue={onIssue} summary={mainAttention} />
        </div>
        <section className={styles.tasks} aria-label="Issue与资源">
          <button
            type="button"
            className={`${controls.chipBtn} ${styles.allTasks}`}
            onClick={onBoard}
          >
            Issue · 白板 ›
          </button>
          <IssueOverview onSelect={onIssue} onAll={onBoard} active={view === "chat"} />
          <div className={styles.recent}>
            <RecentTasks limit={3} detailed />
          </div>
          <div className={styles.sectionTitle}>资源与记录</div>
          <ResourceNavigation onRecords={onRecords} onRepos={onRepos} records={records} />
          <ResourceHosts connection={connection} />
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
