import type { IssueAssignee } from "../api/generated/api.ts";
import { useServices } from "../app/services.tsx";
import { useStore } from "../state/store.ts";

export function AssigneeName({ assignee }: { assignee: IssueAssignee }) {
  const { live } = useServices();
  const catalog = useStore(live.agents.hosted.state);
  if (assignee.kind === "owner") return <>你</>;
  const definition = catalog.value?.projection.agents?.find(
    (agent) =>
      `${agent.identity.execution_environment_id}/${agent.identity.local_id}` === assignee.ref,
  );
  return <span title={assignee.ref}>{definition?.display_name ?? assignee.ref}</span>;
}
