import type {
  AgentInventoryDefinition,
  AgentInventoryProjection,
  AgentInventoryRuntime,
  RetainedAgentDefinition,
  RunnerAgentCatalog,
} from "../api/generated/api.ts";
import { agentKey, currentObservation } from "./facts.ts";

export type Group = {
  id: string;
  projection: AgentInventoryProjection;
  catalog?: RunnerAgentCatalog | undefined;
  configurationRevision?: number | undefined;
};
export type Entry = {
  group: Group;
  agent: AgentInventoryDefinition;
  runtime: AgentInventoryRuntime | undefined;
  history?: RetainedAgentDefinition | undefined;
};

export function entryKey(entry: Entry): string {
  const pin = entry.history?.pin;
  return pin
    ? `${agentKey(entry.agent)}@${pin.snapshot_id}:${pin.public_sha256}`
    : agentKey(entry.agent);
}

export function directoryEntry(
  group: Group,
  agent: AgentInventoryDefinition,
  history?: RetainedAgentDefinition,
): Entry {
  return {
    group,
    agent,
    history,
    runtime: (history?.runtimes ?? group.projection.runtimes ?? []).find(
      (runtime) =>
        runtime.runtime_id === agent.runtime_binding_ref.runtime_id &&
        runtime.revision === agent.runtime_binding_ref.revision,
    ),
  };
}

export function purposeLabel(purpose: string): string {
  return purpose === "main"
    ? "主对话"
    : purpose === "issue_manager"
      ? "协调"
      : purpose === "consultation"
        ? "咨询"
        : purpose === "worker_run"
          ? "执行"
          : purpose;
}

export function observation(entry: Entry): { label: string; available: boolean } {
  const { agent, group } = entry;
  if (entry.history) return { label: "仅续接", available: false };
  if (!agent.enabled) return { label: "已停用", available: false };
  if (group.catalog && !group.catalog.online) return { label: "离线", available: false };
  const value =
    currentObservation(group.catalog, agent, "") ??
    currentObservation(group.catalog, agent, "issue_manager");
  return value
    ? { label: value.available ? "可用" : "不可用", available: value.available }
    : { label: "未上报", available: false };
}
