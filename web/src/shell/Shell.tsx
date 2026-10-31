import { useEffect, useMemo, useRef, useState } from "react";
import { ComposerDraft } from "../conversation/draft.ts";
import type { ApiClient, Connection } from "../api/client.ts";
import { ServicesContext, type Services } from "../app/services.tsx";
import { navigate, useRoute } from "../app/router.ts";
import { useMainAttention } from "../attention/useMainAttention.ts";
import { IssueReadingStates } from "../issues/Details.tsx";
import { ConversationSessions, ConversationSessionsContext } from "../conversation/session.tsx";
import type { LiveData } from "../live/data.ts";
import type { SendQueue } from "../queue/sendQueue.ts";
import { useStore } from "../state/store.ts";
import { AgentWorkspace } from "../conversation/AgentWorkspace.tsx";
import { Sidebar } from "./Sidebar.tsx";
import { TabBar, type View } from "./TabBar.tsx";
import { AppLayout } from "./AppLayout.tsx";
import { RoutePage } from "./RoutePage.tsx";
import styles from "./Shell.module.css";

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
  const route = useRoute();
  const [reading] = useState(() => new IssueReadingStates());
  const sessions = useMemo(() => new ConversationSessions(client), [client]);
  const sessionCleanup = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => {
    clearTimeout(sessionCleanup.current);
    const stop = sessions.connect(live);
    return () => {
      stop();
      sessionCleanup.current = setTimeout(() => void sessions.close(), 0);
    };
  }, [sessions, live]);
  const [draft] = useState(() => new ComposerDraft());
  useEffect(
    () => () => {
      void draft.close();
    },
    [draft],
  );
  const nextActions = useStore(live.attention.next.state);
  const mainAttention = useMainAttention(client, live);
  useEffect(() => {
    void live.agents.openHosted();
  }, [live]);
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
  const taskId = route.page === "task" ? route.id : null;
  useEffect(() => {
    if (taskId) void live.open(taskId);
    else live.close();
  }, [live, taskId]);
  const services = useMemo<Services>(
    () => ({
      client,
      live,
      queue,
      draft,
      followUp(task) {
        draft.followUp(task);
        navigate({ page: "chat" });
      },
      openUpgrade(id) {
        navigate({ page: "upgrade", id });
      },
      openIssue(id) {
        navigate({ page: "issue", id });
      },
      openTask(id) {
        const from =
          route.page === "issue" ? route.id : route.page === "task" ? route.from : undefined;
        navigate(
          { page: "task", id, ...(from ? { from } : {}) },
          { replace: route.page === "task" },
        );
      },
    }),
    [client, live, queue, draft, route],
  );
  const chat = route.page === "chat" || route.page === "conversation";
  const view: View = chat
    ? "chat"
    : route.page === "inbox" || route.page === "attention"
      ? "decisions"
      : "tasks";
  const detail = ["issue", "task", "attention", "upgrade"].includes(route.page);
  const openInbox = (id?: string) => navigate(id ? { page: "attention", id } : { page: "inbox" });
  const showChat = () => navigate({ page: "chat" });
  const showBoard = () => navigate({ page: "issues" });
  const showRecords = () => navigate({ page: "tasks" });
  const showRepos = () => navigate({ page: "repos" });
  return (
    <ServicesContext value={services}>
      <ConversationSessionsContext value={sessions}>
        <AppLayout
          detail={detail}
          sidebar={
            <Sidebar
              records={route.page === "tasks"}
              view={view}
              connection={connection}
              onChat={showChat}
              onBoard={showBoard}
              onRecords={showRecords}
              onSettings={() => navigate({ page: "settings" })}
              onRepos={showRepos}
              mainAttention={mainAttention}
              onInbox={openInbox}
              onIssue={services.openIssue}
            />
          }
          tabs={
            <TabBar
              pendingMore={!!mainAttention.value?.more}
              view={view}
              onChange={(next) =>
                next === "chat" ? showChat() : next === "tasks" ? showBoard() : openInbox()
              }
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
          }
        >
          <div className={styles.content} hidden={!chat}>
            <AgentWorkspace
              connection={connection}
              visible={chat}
              mainAttention={mainAttention}
              onAttention={openInbox}
              onIssue={services.openIssue}
            />
          </div>
          <RoutePage route={route} connection={connection} reading={reading} />
        </AppLayout>
      </ConversationSessionsContext>
    </ServicesContext>
  );
}
