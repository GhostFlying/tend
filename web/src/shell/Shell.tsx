// The app's frame, after the design mock: sidebar, conversation and task
// details side by side on a desktop; the details float over the
// conversation at medium widths; one column with a tab bar on a phone.
import { useEffect, useMemo, useRef, useState } from "react";
import { splitTaskIds } from "../conversation/model.ts";
import { ComposerDraft } from "../conversation/draft.ts";
import type { ApiClient, Connection } from "../api/client.ts";
import { ServicesContext, type Services } from "../app/services.tsx";
import { attentionCount } from "../decisions/Attention.tsx";
import type { LiveData } from "../live/data.ts";
import type { SendQueue } from "../queue/sendQueue.ts";
import { useStore } from "../state/store.ts";
import { Handoff } from "../conversation/Handoff.tsx";
import { UpgradeDetails } from "../upgrades/Upgrade.tsx";
import { Chat } from "./Chat.tsx";
import { Details } from "./Details.tsx";
import styles from "./Shell.module.css";
import { TaskBoard } from "../tasks/Board.tsx";
import { Repositories } from "../repos/Repositories.tsx";
import { WorkItemBoard } from "../workitems/Board.tsx";
import { WorkItemDetails } from "../workitems/Details.tsx";
import { splitWorkItemIds } from "../workitems/model.ts";
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
  const workItemSelected = useStore(live.workItems.selected);
  const workItemFocus = useRef<HTMLElement | null>(null);
  const [upgradeId, setUpgradeId] = useState<string | null>(null);
  const upgradeFocus = useRef<HTMLElement | null>(null);
  const [reposOpen, setReposOpen] = useState(false);
  const reposFocus = useRef<HTMLElement | null>(null);
  const showRepos = () => {
    reposFocus.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setReposOpen(true);
  };
  const closeRepos = () => {
    setReposOpen(false);
    queueMicrotask(() => reposFocus.current?.isConnected && reposFocus.current.focus());
  };
  useEffect(() => {
    live.repos.setConnected(connection.status === "online");
  }, [live, connection.status]);
  useEffect(() => {
    const refresh = () => {
      if (document.visibilityState === "visible") void live.repos.refresh();
    };
    document.addEventListener("visibilitychange", refresh);
    return () => document.removeEventListener("visibilitychange", refresh);
  }, [live]);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const settingsFocus = useRef<HTMLElement | null>(null);
  const showSettings = () => {
    settingsFocus.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    setSettingsOpen(true);
  };
  const closeSettings = () => {
    setSettingsOpen(false);
    queueMicrotask(() => settingsFocus.current?.focus());
  };
  const [draft] = useState(() => new ComposerDraft());
  const returnFocus = useRef<HTMLElement | null>(null);
  const [view, setView] = useState<View>("chat");
  const snapshot = useStore(live.snapshot);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const [boardOpen, setBoardOpen] = useState(false);
  const [recordsOpen, setRecordsOpen] = useState(false);
  const [recordsVisited, setRecordsVisited] = useState(false);
  const [boardVisited, setBoardVisited] = useState(false);
  const showRecords = () => {
    setRecordsOpen(true);
    setRecordsVisited(true);
    setBoardOpen(true);
    setView("tasks");
  };
  const showBoard = () => {
    setRecordsOpen(false);
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
      draft,
      followUp(task) {
        live.close();
        setDetailsOpen(false);
        setBoardOpen(false);
        setView("chat");
        draft.followUp(task);
      },
      openUpgrade(id) {
        upgradeFocus.current =
          document.activeElement instanceof HTMLElement ? document.activeElement : null;
        setUpgradeId(id);
      },
      openWorkItem(id) {
        workItemFocus.current =
          document.activeElement instanceof HTMLElement ? document.activeElement : null;
        void live.workItems.open(id);
      },
      discussWorkItem(item, intention) {
        live.workItems.close();
        live.close();
        setDetailsOpen(false);
        setBoardOpen(false);
        setView("chat");
        draft.workItem(item, intention);
      },
      openTask(taskId) {
        live.workItems.close();
        const active = document.activeElement;
        if (active instanceof HTMLElement && !active.closest('[aria-label="任务详情"]'))
          returnFocus.current = active;
        void live.open(taskId);
        setDetailsOpen(true);
      },
    }),
    [client, live, queue, draft],
  );

  const openedInitialTask = useRef(false);
  useEffect(() => {
    if (openedInitialTask.current) return;
    openedInitialTask.current = true;
    const taskId = new URLSearchParams(window.location.search).get("task");
    const workItemId = new URLSearchParams(window.location.search).get("work_item");
    if (workItemId) {
      const parts = splitWorkItemIds(workItemId);
      if (parts.length === 1 && typeof parts[0] !== "string")
        services.openWorkItem(parts[0]!.workItemId);
    }
    if (!taskId) return;
    const parts = splitTaskIds(taskId);
    if (parts.length === 1 && typeof parts[0] !== "string") services.openTask(parts[0]!.taskId);
  }, [services]);

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
          <Sidebar
            view={view}
            connection={connection}
            onBoard={showBoard}
            onRecords={showRecords}
            onSettings={showSettings}
            onRepos={showRepos}
          />
        </aside>
        <main className={styles.chat}>
          <div className={styles.content} hidden={boardOpen}>
            <Chat connection={connection} visible={!boardOpen} />
          </div>
          {boardVisited && (
            <div className={styles.content} hidden={!boardOpen || recordsOpen}>
              <WorkItemBoard
                active={boardOpen && !recordsOpen}
                connection={connection}
                onChat={showChat}
                onRepos={showRepos}
                onRecords={showRecords}
              />
            </div>
          )}
          {recordsVisited && (
            <div className={styles.content} hidden={!boardOpen || !recordsOpen}>
              <TaskBoard
                active={boardOpen && recordsOpen}
                connection={connection}
                onChat={showBoard}
                backLabel="返回工作项"
                onRepos={showRepos}
              />
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
      {workItemSelected && (
        <WorkItemDetails
          key={workItemSelected}
          connection={connection}
          onClose={() => {
            live.workItems.close();
            queueMicrotask(
              () => workItemFocus.current?.isConnected && workItemFocus.current.focus(),
            );
          }}
        />
      )}
      {upgradeId && (
        <UpgradeDetails
          id={upgradeId}
          connection={connection}
          onClose={() => {
            setUpgradeId(null);
            queueMicrotask(() => upgradeFocus.current?.isConnected && upgradeFocus.current.focus());
          }}
        />
      )}
      {reposOpen && <Repositories connection={connection} onClose={closeRepos} />}
      {settingsOpen && <Handoff connection={connection} onClose={closeSettings} />}
    </ServicesContext>
  );
}
