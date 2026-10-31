import { useLayoutEffect, useRef } from "react";
import type { Connection } from "../api/client.ts";
import { back, href, navigate, restorePage, type Route } from "../app/router.ts";
import { useServices } from "../app/services.tsx";
import { ArchiveBrowser } from "../archive/Archive.tsx";
import { AgentCatalogView } from "../agents/Catalog.tsx";
import { Inbox } from "../attention/Inbox.tsx";
import { IssueBoard } from "../issues/Board.tsx";
import { IssueDetails, type IssueReadingStates } from "../issues/Details.tsx";
import { Handoff } from "../conversation/Handoff.tsx";
import { UpgradeDetails } from "../upgrades/Upgrade.tsx";
import { TaskBoard } from "../tasks/Board.tsx";
import { Repositories } from "../repos/Repositories.tsx";
import { UsagePanel } from "../usage/Usage.tsx";
import { Hosts } from "../hosts/Hosts.tsx";
import { Details } from "./Details.tsx";
import { MorePage } from "./More.tsx";
import { PageHeader } from "./PageHeader.tsx";
import styles from "./Shell.module.css";

export function RoutePage({
  route,
  connection,
  reading,
}: {
  route: Route;
  connection: Connection;
  reading: IssueReadingStates;
}) {
  const { openIssue, openTask } = useServices();
  const outlet = useRef<HTMLDivElement>(null);
  const identity = href(route);
  useLayoutEffect(() => {
    const root = outlet.current?.closest("main");
    if (root) return restorePage(root);
  }, [identity]);
  const chat = () => navigate({ page: "chat" });
  const inbox = (id?: string) => navigate(id ? { page: "attention", id } : { page: "inbox" });
  let content;
  switch (route.page) {
    case "chat":
    case "conversation":
      return <div ref={outlet} hidden />;
    case "issues":
      content = <IssueBoard active onSelect={openIssue} onInbox={inbox} />;
      break;
    case "issue":
      content = (
        <IssueDetails
          key={route.id}
          id={route.id}
          connection={connection}
          reading={reading.get(route.id)}
          onBack={() => back({ page: "issues" })}
          onSelect={openIssue}
          onTask={openTask}
        />
      );
      break;
    case "tasks":
      content = <TaskBoard active connection={connection} />;
      break;
    case "task":
      content = (
        <section className={styles.page} aria-label="任务详情" data-task-scroll>
          <PageHeader
            title="任务详情"
            backLabel={route.from ? "返回Issue" : "返回任务执行"}
            onBack={() => back(route.from ? { page: "issue", id: route.from } : { page: "tasks" })}
          />
          <Details key={route.id} />
        </section>
      );
      break;
    case "inbox":
    case "attention":
      content = (
        <Inbox initial={route.page === "attention" ? route.id : undefined} onIssue={openIssue} />
      );
      break;
    case "more":
      content = <MorePage />;
      break;
    case "settings":
      content = <Handoff connection={connection} onCatalog={() => navigate({ page: "agents" })} />;
      break;
    case "agents":
      content = (
        <section className={styles.page} aria-label="Agent 与类别">
          <PageHeader title="Agent 与类别" onBack={() => back({ page: "settings" })} />
          <AgentCatalogView />
        </section>
      );
      break;
    case "archive":
      content = (
        <section className={styles.page}>
          <ArchiveBrowser onIssue={openIssue} />
        </section>
      );
      break;
    case "repos":
      content = <Repositories connection={connection} />;
      break;
    case "usage":
      content = (
        <section className={styles.page}>
          <PageHeader title="Token 用量" />
          <UsagePanel />
        </section>
      );
      break;
    case "hosts":
      content = (
        <section className={styles.page} aria-label="执行主机">
          <PageHeader title="执行主机" />
          <Hosts connection={connection} expanded />
        </section>
      );
      break;
    case "upgrade":
      content = (
        <UpgradeDetails
          key={route.id}
          id={route.id}
          connection={connection}
          onClose={() => back({ page: "hosts" })}
        />
      );
      break;
    case "missing":
      content = (
        <section className={styles.page}>
          <PageHeader title="页面不存在" onBack={chat} />
        </section>
      );
  }
  return (
    <div ref={outlet} className={styles.content}>
      {content}
    </div>
  );
}
