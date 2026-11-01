import { Component, lazy, Suspense, useLayoutEffect, useRef, type ReactNode } from "react";
import type { Connection } from "../api/client.ts";
import { back, href, navigate, restorePage, type Route } from "../app/router.ts";
import { useServices } from "../app/services.tsx";
const ArchiveBrowser = lazy(() =>
  import("../archive/Archive.tsx").then((m) => ({ default: m.ArchiveBrowser })),
);
const AgentCatalogView = lazy(() =>
  import("../agents/Catalog.tsx").then((m) => ({ default: m.AgentCatalogView })),
);
const Inbox = lazy(() => import("../attention/Inbox.tsx").then((m) => ({ default: m.Inbox })));
const IssueBoard = lazy(() =>
  import("../issues/Board.tsx").then((m) => ({ default: m.IssueBoard })),
);
import type { IssueReadingStates } from "../issues/reading.ts";
const IssueDetails = lazy(() =>
  import("../issues/Details.tsx").then((m) => ({ default: m.IssueDetails })),
);
const Handoff = lazy(() =>
  import("../conversation/Handoff.tsx").then((m) => ({ default: m.Handoff })),
);
const UpgradeDetails = lazy(() =>
  import("../upgrades/Upgrade.tsx").then((m) => ({ default: m.UpgradeDetails })),
);
const TaskBoard = lazy(() => import("../tasks/Board.tsx").then((m) => ({ default: m.TaskBoard })));
const Repositories = lazy(() =>
  import("../repos/Repositories.tsx").then((m) => ({ default: m.Repositories })),
);
const UsagePanel = lazy(() =>
  import("../usage/Usage.tsx").then((m) => ({ default: m.UsagePanel })),
);
const Hosts = lazy(() => import("../hosts/Hosts.tsx").then((m) => ({ default: m.Hosts })));
const Details = lazy(() => import("./Details.tsx").then((m) => ({ default: m.Details })));
const MorePage = lazy(() => import("./More.tsx").then((m) => ({ default: m.MorePage })));
import { Button } from "../ui/Button.tsx";
import { PageHeader } from "./PageHeader.tsx";
import styles from "./Shell.module.css";

type RouteBoundaryProps = { identity: string; children: ReactNode };
type RouteBoundaryState = { identity: string; failed: boolean };

// A failed route must not tear down the persistent chat or shell navigation.
export class RouteErrorBoundary extends Component<RouteBoundaryProps, RouteBoundaryState> {
  override state = { identity: this.props.identity, failed: false };

  static getDerivedStateFromProps(
    props: RouteBoundaryProps,
    state: RouteBoundaryState,
  ): RouteBoundaryState | null {
    return props.identity === state.identity ? null : { identity: props.identity, failed: false };
  }

  static getDerivedStateFromError(): Pick<RouteBoundaryState, "failed"> {
    return { failed: true };
  }

  override render() {
    if (!this.state.failed) return this.props.children;
    return (
      <section className={styles.page}>
        <PageHeader title="页面已更新" />
        <div className={styles.routeError} role="alert">
          <p>页面已更新，需要重新加载。</p>
          <Button variant="primary" onClick={() => window.location.reload()}>
            重新加载
          </Button>
        </div>
      </section>
    );
  }
}

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
    case "im":
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
            backLabel={route.from ? "返回 Issue" : "返回任务执行"}
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
      <RouteErrorBoundary identity={identity}>
        <Suspense
          fallback={
            <section className={styles.page} aria-busy="true">
              <PageHeader title="正在读取…" />
              <div className={styles.skeleton} aria-hidden="true" />
            </section>
          }
        >
          {content}
        </Suspense>
      </RouteErrorBoundary>
    </div>
  );
}
