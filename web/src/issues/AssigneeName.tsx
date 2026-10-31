import type { IssueAssignee } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";

import { assigneeDisplay } from "./assigneeDisplay.ts";

export function AssigneeName({
  assignee,
  initial = false,
}: {
  assignee: IssueAssignee;
  initial?: boolean;
}) {
  const { live } = useServices();
  const catalog = useStore(live.agents.hosted.state);
  if (assignee.kind === "owner") return <>你</>;
  const definition = catalog.value?.projection.agents?.find(
    (agent) =>
      `${agent.identity.execution_environment_id}/${agent.identity.local_id}` === assignee.ref,
  );
  const name = assigneeDisplay(assignee, definition?.display_name);
  return <span>{initial ? Array.from(name)[0] : name}</span>;
}
