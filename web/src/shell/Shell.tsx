// The app's frame, after the design mock: sidebar, conversation and task
// details side by side on a desktop; the details float over the
// conversation at medium widths; one column with a tab bar on a phone.
import { useMemo, useRef, useState } from "react";
import type { ApiClient, Connection } from "../api/client.ts";
import { ServicesContext, type Services } from "../app/services.tsx";
import { attentionCount } from "../decisions/Attention.tsx";
import type { LiveData } from "../live/data.ts";
import type { SendQueue } from "../queue/sendQueue.ts";
import { useStore } from "../state/store.ts";
import { Chat } from "./Chat.tsx";
import { Details } from "./Details.tsx";
import styles from "./Shell.module.css";
import { TaskBoard } from "../tasks/Board.tsx";
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
  const returnFocus = useRef<HTMLElement | null>(null);
  const [view, setView] = useState<View>("chat");
  const snapshot = useStore(live.snapshot);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [boardOpen, setBoardOpen] = useState(false);
  const [boardVisited, setBoardVisited] = useState(false);
  const showBoard = () => {
    setBoardOpen(true);
    setBoardVisited(true);
    setView("tasks");
  };
  const showChat = () => {
    setBoardOpen(false);
    setView("chat");
  };

  const services = useMemo<Services>(
    () => ({
      client,
      live,
      queue,
      openTask(taskId) {
        const active = document.activeElement;
        if (active instanceof HTMLElement && !active.closest('[aria-label="任务详情"]'))
          returnFocus.current = active;
        void live.open(taskId);
        setDetailsOpen(true);
      },
    }),
    [client, live, queue],
  );

  const closeDetails = () => {
    setDetailsOpen(false);
    live.close();
    queueMicrotask(() => {
      if (returnFocus.current?.isConnected) returnFocus.current.focus();
    });
  };

  return (
    <ServicesContext value={services}>
      <div
        className={styles.app}
        data-view={view}
        data-board={boardOpen ? "open" : "closed"}
        data-details={detailsOpen ? "open" : "closed"}
      >
        <aside className={styles.sidebar}>
          <Sidebar view={view} connection={connection} onBoard={showBoard} />
        </aside>
        <main className={styles.chat}>
          <div className={styles.content} hidden={boardOpen}>
            <Chat connection={connection} />
          </div>
          {boardVisited && (
            <div className={styles.content} hidden={!boardOpen}>
              <TaskBoard active={boardOpen} onChat={showChat} />
            </div>
          )}
        </main>
        <section className={styles.details} inert={!detailsOpen} aria-label="任务详情">
          <Details onClose={closeDetails} />
        </section>
        <TabBar
          view={view}
          onChange={(next) => {
            if (next === "tasks") showBoard();
            else {
              setBoardOpen(false);
              setView(next);
            }
          }}
          pending={attentionCount(snapshot)}
        />
      </div>
    </ServicesContext>
  );
}
