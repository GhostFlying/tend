// Keep the board and conversation mounted behind a shared ledger detail modal.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { splitTaskIds } from "../conversation/model.ts";
import { ComposerDraft } from "../conversation/draft.ts";
import type { ApiClient, Connection } from "../api/client.ts";
import { ServicesContext, type Services } from "../app/services.tsx";
import { ArchiveBrowser } from "../archive/Archive.tsx";
import { useMainAttention } from "../attention/useMainAttention.ts";
import { AgentCatalogView } from "../agents/Catalog.tsx";
import { Inbox } from "../attention/Inbox.tsx";
import { IssueBoard } from "../issues/Board.tsx";
import { IssueDetails } from "../issues/Details.tsx";
import type { LiveData } from "../live/data.ts";
import type { SendQueue } from "../queue/sendQueue.ts";
import { useStore } from "../state/store.ts";
import { Handoff } from "../conversation/Handoff.tsx";
import { UpgradeDetails } from "../upgrades/Upgrade.tsx";
import { AgentWorkspace as Chat } from "../conversation/AgentWorkspace.tsx";
import { Details } from "./Details.tsx";
import { DetailOverlay, DetailPane } from "./DetailOverlay.tsx";
import styles from "./Shell.module.css";
import { TaskBoard } from "../tasks/Board.tsx";
import { Repositories } from "../repos/Repositories.tsx";
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
  const [issueSelected, setIssueSelected] = useState<string | null>(null);
  const [inboxOpen, setInboxOpen] = useState(false);
  const [inboxSelected, setInboxSelected] = useState<string | undefined>(undefined);
  const nextActions = useStore(live.attention.next.state);
  const mainAttention = useMainAttention(client, live);
  useEffect(() => {
    void live.agents.openHosted();
  }, [live]);
  const openInbox = (id?: string) => {
    setInboxSelected(id);
    setInboxOpen(true);
  };
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
  const [archiveOpen, setArchiveOpen] = useState(false);
  useEffect(() => {
    const open = () => setArchiveOpen(true);
    window.addEventListener("tend:open-archive", open);
    return () => window.removeEventListener("tend:open-archive", open);
  }, []);
  const [catalogOpen, setCatalogOpen] = useState(false);
  const [catalogFromSettings, setCatalogFromSettings] = useState(false);
  useEffect(() => {
    const open = () => {
      setCatalogFromSettings(false);
      setCatalogOpen(true);
    };
    window.addEventListener("tend:open-catalog", open);
    return () => window.removeEventListener("tend:open-catalog", open);
  }, []);
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
  useEffect(
    () => () => {
      void draft.close();
    },
    [draft],
  );

  const [view, setView] = useState<View>("chat");

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
      setIssueSelected(item);
      if (item) void live.issues.open(item);
      setTaskSelected(task);
      if (task) void live.open(task);
      else live.close();
    },
    [live],
  );
  const navigateDetail = useCallback(
    (item: string | null, task: string | null) => {
      const url = new URL(window.location.href);
      if (url.searchParams.get("issue") === item && url.searchParams.get("task") === task) return;
      url.searchParams.delete("work_item");
      if (item) url.searchParams.set("issue", item);
      else url.searchParams.delete("issue");
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

  useEffect(() => {
    const open = () => {
      setIssueSelected(null);
      setBoardOpen(false);
      live.close();
      setTaskSelected(null);
      const url = new URL(window.location.href);
      url.searchParams.delete("task");
      url.searchParams.delete("work_item");
      url.searchParams.delete("issue");
      window.history.replaceState({ ...window.history.state, tendDetailDepth: 0 }, "", url);
      setView("chat");
    };
    window.addEventListener("tend:open-agent", open);
    return () => window.removeEventListener("tend:open-agent", open);
  }, [live]);

  const services = useMemo<Services>(
    () => ({
      client,
      live,
      queue,
      draft,
      followUp(task) {
        window.dispatchEvent(new Event("tend:main-agent"));
        live.close();
        setTaskSelected(null);
        const url = new URL(window.location.href);
        url.searchParams.delete("task");
        url.searchParams.delete("work_item");
        url.searchParams.delete("issue");
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
      openIssue(id) {
        navigateDetail(id, null);
      },
      openTask(taskId) {
        const task =
          live.snapshot.get()?.tasks?.find((item) => item.task_id === taskId) ??
          live.lookups.get().get(taskId);
        const currentIssue = issueSelected ?? task?.primary_issue_id;
        navigateDetail(currentIssue ?? null, taskId);
      },
    }),
    [client, live, queue, draft, navigateDetail, issueSelected],
  );

  useEffect(() => {
    const read = () => {
      if (closingDetails.current) {
        closingDetails.current = false;
        const url = new URL(window.location.href);
        url.searchParams.delete("work_item");
        url.searchParams.delete("issue");
        url.searchParams.delete("task");
        window.history.replaceState(
          { ...window.history.state, tendDetailSession: detailSession, tendDetailDepth: 0 },
          "",
          url,
        );
      }
      const params = new URLSearchParams(window.location.search);
      const canonical = params.get("issue");
      const task = params.get("task");
      const validTask =
        task && splitTaskIds(task).length === 1 && typeof splitTaskIds(task)[0] !== "string"
          ? task
          : null;
      applyDetail(canonical, validTask);
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
    url.searchParams.delete("issue");
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
            mainAttention={mainAttention}
            onInbox={openInbox}
            onIssue={(id) => navigateDetail(id, null)}
          />
        </aside>
        <main className={styles.chat}>
          <div className={styles.content} hidden={boardOpen}>
            <Chat
              connection={connection}
              visible={!boardOpen}
              mainAttention={mainAttention}
              onAttention={openInbox}
              onIssue={(id) => navigateDetail(id, null)}
            />
          </div>
          {boardVisited && (
            <div className={styles.content} hidden={!boardOpen || recordsOpen}>
              <IssueBoard
                active={boardOpen && !recordsOpen}
                onSelect={(id) => navigateDetail(id, null)}
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
                backLabel="返回Issue"
                onRepos={showRepos}
              />
            </div>
          )}
        </main>
        <TabBar
          pendingMore={!!mainAttention.value?.more}
          view={view}
          onChange={(next) => {
            if (next === "tasks") showBoard();
            else {
              if (next === "decisions") openInbox();
              setBoardOpen(false);
              setView(next);
            }
          }}
          pending={
            (mainAttention.value?.items ?? []).filter(
              (item) => item.status === "open" && item.kind !== "information",
            ).length +
            (nextActions.value?.items ?? []).filter(
              (item) =>
                item.kind === "user_action" &&
                ["open", "blocked"].includes(item.status) &&
                (!item.attention_id ||
                  !(mainAttention.value?.items ?? []).some(
                    (a) => a.attention_id === item.attention_id,
                  )),
            ).length
          }
        />
      </div>
      {(issueSelected || taskSelected) && (
        <DetailOverlay
          task={!!taskSelected}
          backLabel="返回Issue"
          label={issueSelected && !taskSelected ? "Issue详情" : undefined}
          onClose={closeDetails}
          onBack={
            taskSelected && issueSelected ? () => navigateDetail(issueSelected, null) : undefined
          }
        >
          {issueSelected && (
            <DetailPane active={!taskSelected}>
              <IssueDetails
                key={issueSelected}
                id={issueSelected}
                onSelect={(id) => navigateDetail(id, null)}
                onTask={(id) => navigateDetail(issueSelected, id)}
                onInbox={openInbox}
              />
            </DetailPane>
          )}
          {taskSelected && (
            <DetailPane key={taskSelected} active>
              <Details />
            </DetailPane>
          )}
        </DetailOverlay>
      )}
      {inboxOpen && (
        <DetailOverlay
          task={false}
          label="待处理"
          onClose={() => {
            setInboxOpen(false);
          }}
        >
          <Inbox
            initial={inboxSelected}
            onIssue={(id) => {
              setInboxOpen(false);
              navigateDetail(id, null);
            }}
            onClose={() => {
              setInboxOpen(false);
            }}
          />
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
      {settingsOpen && (
        <Handoff
          connection={connection}
          onClose={closeSettings}
          onCatalog={() => {
            setSettingsOpen(false);
            setCatalogFromSettings(true);
            setCatalogOpen(true);
          }}
        />
      )}
      {archiveOpen && (
        <DetailOverlay task={false} label="历史归档" onClose={() => setArchiveOpen(false)}>
          <ArchiveBrowser
            onIssue={(id) => {
              setArchiveOpen(false);
              navigateDetail(id, null);
            }}
          />
        </DetailOverlay>
      )}
      {catalogOpen && (
        <DetailOverlay
          task={false}
          label="Agent与类别"
          onClose={() => {
            setCatalogOpen(false);
            if (catalogFromSettings) showSettings();
          }}
        >
          <AgentCatalogView />
        </DetailOverlay>
      )}
    </ServicesContext>
  );
}
