import type { ReactNode } from "react";
import styles from "./Shell.module.css";

export function AppLayout({
  sidebar,
  children,
  tabs,
  detail,
}: {
  sidebar: ReactNode;
  children: ReactNode;
  tabs: ReactNode;
  detail: boolean;
}) {
  return (
    <div className={styles.app} data-detail={detail}>
      <aside className={styles.sidebar}>{sidebar}</aside>
      <main className={styles.chat}>{children}</main>
      {!detail && tabs}
    </div>
  );
}
