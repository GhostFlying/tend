import { useEffect } from "react";
import type { IssueAssignee, IssueManagerView } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";

import {
  assigneeDisplay,
  pendingManagerDisplay,
  pendingCategoryDisplay,
} from "./assigneeDisplay.ts";

export function AssigneeName({
  assignee,
  initial = false,
  manager,
  pendingCategoryID,
}: {
  assignee: IssueAssignee;
  manager?: IssueManagerView | undefined;
  initial?: boolean;
  pendingCategoryID?: string | undefined;
}) {
  const { live } = useServices();
  const catalog = useStore(live.agents.hosted.state);
  const snapshot = useStore(live.snapshot);
  const hostID =
    manager?.host_id ??
    (assignee.kind === "agent"
      ? snapshot?.hosts?.find((host) => assignee.ref.startsWith(`${host.host_id}/`))?.host_id
      : undefined);
  const runnerRead = hostID ? live.agents.runnerFor(hostID) : live.agents.runner;
  const runnerCatalog = useStore(runnerRead.state);
  useEffect(() => {
    if (hostID && !runnerRead.state.get().value && !runnerRead.state.get().loading)
      void runnerRead.open(`/api/v1/hosts/${encodeURIComponent(hostID)}/agents`);
  }, [runnerRead, hostID]);
  const categories = useStore(live.agents.categories.state);
  const pendingName =
    pendingCategoryDisplay(pendingCategoryID, categories.value?.items ?? []) ??
    pendingManagerDisplay(manager, categories.value?.items ?? []);
  const definition = [
    ...(catalog.value?.projection?.agents ?? []),
    ...(runnerCatalog.value?.projection?.agents ?? []),
  ].find(
    (agent) =>
      `${agent.identity.execution_environment_id}/${agent.identity.local_id}` === assignee.ref,
  );
  const name = assigneeDisplay(assignee, definition?.display_name, pendingName);
  return <span>{initial ? Array.from(name)[0] : name}</span>;
}
