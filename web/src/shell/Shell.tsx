// Keep the board and conversation mounted behind a shared ledger detail modal.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
import { DetailOverlay, DetailPane } from "./DetailOverlay.tsx";
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
  const [taskSelected, setTaskSelected] = useState<string | null>(null);
  const [detailSession] = useState(() => `${performance.timeOrigin}:${performance.now()}`);
  const closingDetails = useRef(false);
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

  const [view, setView] = useState<View>("chat");
  const snapshot = useStore(live.snapshot);

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

  const applyDetail = useCallback(
    (item: string | null, task: string | null) => {
      if (item) void live.workItems.open(item);
      else live.workItems.close();
      setTaskSelected(task);
      if (task) void live.open(task);
      else live.close();
    },
    [live],
  );
  const navigateDetail = useCallback(
    (item: string | null, task: string | null) => {
      const url = new URL(window.location.href);
      if (url.searchParams.get("work_item") === item && url.searchParams.get("task") === task)
        return;
      if (item) url.searchParams.set("work_item", item);
      else url.searchParams.delete("work_item");
      if (task) url.searchParams.set("task", task);
      else url.searchParams.delete("task");
      window.history.pushState(
        {
          ...window.history.state,
          tendDetailSession: detailSession,
          tendDetailDepth:
            (window.history.state?.tendDetailSession === detailSession
              ? window.history.state.tendDetailDepth
              : 0) + 1,
        },
        "",
        url,
      );
      applyDetail(item, task);
    },
    [applyDetail, detailSession],
  );

  const services = useMemo<Services>(
    () => ({
      client,
      live,
      queue,
      draft,
      followUp(task) {
        live.close();
        setTaskSelected(null);
        live.workItems.close();
        const url = new URL(window.location.href);
        url.searchParams.delete("task");
        url.searchParams.delete("work_item");
        window.history.replaceState({ ...window.history.state, tendDetailDepth: 0 }, "", url);
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
        navigateDetail(id, null);
      },
      discussWorkItem(item, intention) {
        live.workItems.close();
        live.close();
        setTaskSelected(null);
        const url = new URL(window.location.href);
        url.searchParams.delete("task");
        url.searchParams.delete("work_item");
        window.history.replaceState({ ...window.history.state, tendDetailDepth: 0 }, "", url);
        setBoardOpen(false);
        setView("chat");
        draft.workItem(item, intention);
      },
      openTask(taskId) {
        navigateDetail(live.workItems.selected.get(), taskId);
      },
    }),
    [client, live, queue, draft, navigateDetail],
  );

  useEffect(() => {
    const read = () => {
      if (closingDetails.current) {
        closingDetails.current = false;
        const url = new URL(window.location.href);
        url.searchParams.delete("work_item");
        url.searchParams.delete("task");
        window.history.replaceState(
          { ...window.history.state, tendDetailSession: detailSession, tendDetailDepth: 0 },
          "",
          url,
        );
      }
      const params = new URLSearchParams(window.location.search);
      const item = params.get("work_item");
      const task = params.get("task");
      const validItem =
        item && splitWorkItemIds(item).length === 1 && typeof splitWorkItemIds(item)[0] !== "string"
          ? item
          : null;
      const validTask =
        task && splitTaskIds(task).length === 1 && typeof splitTaskIds(task)[0] !== "string"
          ? task
          : null;
      applyDetail(validItem, validTask);
    };
    // A reload starts a new UI session; do not traverse another document's stack.
    window.history.replaceState(
      { ...window.history.state, tendDetailSession: detailSession, tendDetailDepth: 0 },
      "",
    );
    read();
    window.addEventListener("popstate", read);
    return () => window.removeEventListener("popstate", read);
  }, [applyDetail, detailSession]);
  const closeDetails = () => {
    if (closingDetails.current) return;
    const depth =
      window.history.state?.tendDetailSession === detailSession
        ? window.history.state.tendDetailDepth
        : 0;
    if (depth > 0) {
      closingDetails.current = true;
      window.history.go(-depth);
      return;
    }
    const url = new URL(window.location.href);
    url.searchParams.delete("work_item");
    url.searchParams.delete("task");
    window.history.replaceState(window.history.state, "", url);
    applyDetail(null, null);
  };

  return (
    <ServicesContext value={services}>
      <div
        className={styles.app}
        data-view={view}
        data-board={boardOpen ? "open" : "closed"}
        data-details={taskSelected ? "open" : "closed"}
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
      {(workItemSelected || taskSelected) && (
        <DetailOverlay
          task={!!taskSelected}
          onClose={closeDetails}
          onBack={
            taskSelected && workItemSelected
              ? () => navigateDetail(workItemSelected, null)
              : undefined
          }
        >
          {workItemSelected && (
            <DetailPane active={!taskSelected}>
              <WorkItemDetails key={workItemSelected} connection={connection} />
            </DetailPane>
          )}
          {taskSelected && (
            <DetailPane key={taskSelected} active>
              <Details />
            </DetailPane>
          )}
        </DetailOverlay>
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
