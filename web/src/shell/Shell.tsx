// The app's frame, after the design mock: sidebar, conversation and task
// details side by side on a desktop; the details float over the
// conversation at medium widths; one column with a tab bar on a phone.
import { useMemo, useState } from "react";
import type { ApiClient, Connection } from "../api/client.ts";
import { ServicesContext, type Services } from "../app/services.tsx";
import type { LiveData } from "../live/data.ts";
import type { SendQueue } from "../queue/sendQueue.ts";
import { Chat } from "./Chat.tsx";
import { Details } from "./Details.tsx";
import styles from "./Shell.module.css";
import { Sidebar } from "./Sidebar.tsx";
import { TabBar, type View } from "./TabBar.tsx";

export function Shell({
  client,
  live,
  connection,
  queue,
}: {
  client: ApiClient;
  live: LiveData;
  connection: Connection;
  queue: SendQueue | null;
}) {
  const [view, setView] = useState<View>("chat");
  const [detailsOpen, setDetailsOpen] = useState(false);

  const services = useMemo<Services>(
    () => ({
      client,
      live,
      queue,
      openTask(taskId) {
        void live.open(taskId);
        setDetailsOpen(true);
      },
    }),
    [client, live, queue],
  );

  const closeDetails = () => {
    setDetailsOpen(false);
    live.close();
  };

  return (
    <ServicesContext value={services}>
      <div className={styles.app} data-view={view} data-details={detailsOpen ? "open" : "closed"}>
        <aside className={styles.sidebar}>
          <Sidebar view={view} connection={connection} />
        </aside>
        <main className={styles.chat}>
          <Chat connection={connection} />
        </main>
        <section className={styles.details} inert={!detailsOpen} aria-label="任务详情">
          <Details onClose={closeDetails} />
        </section>
        <TabBar view={view} onChange={setView} pending={0} />
      </div>
    </ServicesContext>
  );
}
