// The app's frame, after the design mock: sidebar, conversation and task
// details side by side on a desktop; the details float over the
// conversation at medium widths; one column with a tab bar on a phone.
import { useState } from "react";
import type { Connection } from "../api/client.ts";
import type { SendQueue } from "../queue/sendQueue.ts";
import { Chat } from "./Chat.tsx";
import { Details } from "./Details.tsx";
import styles from "./Shell.module.css";
import { Sidebar } from "./Sidebar.tsx";
import { TabBar, type View } from "./TabBar.tsx";

export function Shell({ connection, queue }: { connection: Connection; queue: SendQueue | null }) {
  const [view, setView] = useState<View>("chat");
  // Task rows and task chips open the details (F7).
  const [detailsOpen, setDetailsOpen] = useState(false);

  return (
    <div className={styles.app} data-view={view} data-details={detailsOpen ? "open" : "closed"}>
      <aside className={styles.sidebar}>
        <Sidebar view={view} connection={connection} />
      </aside>
      <main className={styles.chat}>
        <Chat connection={connection} queue={queue} />
      </main>
      <section className={styles.details} inert={!detailsOpen} aria-label="任务详情">
        <Details onClose={() => setDetailsOpen(false)} />
      </section>
      <TabBar view={view} onChange={setView} pending={0} />
    </div>
  );
}
