import type { ReactNode } from "react";
import { useStableDisclosures } from "../ui/disclosures.ts";
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
  useStableDisclosures();
  return (
    <div className={styles.app} data-detail={detail}>
      <aside className={styles.sidebar}>{sidebar}</aside>
      <main className={styles.chat}>{children}</main>
      {!detail && tabs}
    </div>
  );
}
