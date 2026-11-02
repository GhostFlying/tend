import type {
  AgentExecutor,
  AgentInventoryDefinition,
  RunnerAgentCatalog,
} from "../api/generated/api.ts";

export function agentKey(agent: AgentInventoryDefinition): string {
  return `${agent.identity.execution_environment_id}/${agent.identity.local_id}`;
}

/** A previous heartbeat must not describe a newly registered definition. */
export function currentObservation(
  catalog: RunnerAgentCatalog | undefined,
  agent: AgentInventoryDefinition,
  purpose: "" | "issue_manager",
): AgentExecutor | undefined {
  const current = catalog?.current;
  const binding =
    purpose === "issue_manager"
      ? (agent.owner_runtime_binding_ref ?? agent.runtime_binding_ref)
      : agent.runtime_binding_ref;
  return catalog?.executors?.find(
    ({ pin }) =>
      current &&
      (pin.purpose ?? "") === purpose &&
      pin.identity.execution_environment_id === agent.identity.execution_environment_id &&
      pin.identity.local_id === agent.identity.local_id &&
      pin.agent_revision === agent.revision &&
      pin.runtime.runtime_id === binding.runtime_id &&
      pin.runtime.revision === binding.revision &&
      pin.snapshot_id === current.snapshot_id &&
      pin.inventory_revision === current.local_revision &&
      pin.public_sha256 === current.public_sha256 &&
      pin.profile.model === agent.requested_model &&
      (pin.profile.effort ?? "") === (agent.requested_effort ?? ""),
  );
}
