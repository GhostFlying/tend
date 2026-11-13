import type {
  ConcurrencyBinding,
  AgentTaskActivity,
  AgentInventoryDefinition,
  AgentInventoryProjection,
  AgentInventoryRuntime,
  RetainedAgentDefinition,
  RunnerAgentCatalog,
} from "../api/generated/api.ts";
import { agentKey, currentObservation } from "./facts.ts";

export type Group = {
  id: string;
  taskActivity?: AgentTaskActivity | undefined;
  projection: AgentInventoryProjection;
  catalog?: RunnerAgentCatalog | undefined;
  configurationRevision?: number | undefined;
  concurrencyBindings?: ConcurrencyBinding[] | null | undefined;
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
  if (!group.catalog) return { label: "未接入运行观测", available: false };
  if (group.catalog && !group.catalog.online) return { label: "离线", available: false };
  const value =
    currentObservation(group.catalog, agent, "") ??
    currentObservation(group.catalog, agent, "issue_manager");
  return value
    ? { label: value.available ? "可用" : "不可用", available: value.available }
    : { label: "未上报", available: false };
}

/** Current identities include their older frozen versions. Missing observations
 * must never turn into an idle claim. */
export function taskActivity(entry: Entry) {
  const activity = entry.group.taskActivity ?? entry.group.catalog?.task_activity;
  if (!activity) return undefined;
  return {
    running: ownTaskCount(activity.running_tasks, entry.agent.identity.local_id),
    uncertain: ownTaskCount(activity.uncertain_tasks, entry.agent.identity.local_id),
  };
}

function ownTaskCount(counts: AgentTaskActivity["running_tasks"], id: string): number {
  return counts && Object.hasOwn(counts, id) ? (counts[id] ?? 0) : 0;
}

export function activityOrder(entry: Entry): number {
  const activity = taskActivity(entry);
  return activity?.running ? 0 : activity?.uncertain ? 1 : 2;
}

/** Keep working retired identities discoverable, once each, using their latest
 * retained definition. Other historical versions stay in the history section. */
export function activeEntries(entries: Entry[]): Entry[] {
  const current = new Set(
    entries.filter((entry) => !entry.history).map((entry) => agentKey(entry.agent)),
  );
  const retained = new Map<string, Entry>();
  for (const entry of entries) {
    const key = agentKey(entry.agent),
      activity = taskActivity(entry);
    if (!entry.history || current.has(key) || (!activity?.running && !activity?.uncertain))
      continue;
    const previous = retained.get(key);
    if (
      !previous ||
      entry.history.pin.inventory_revision > previous.history!.pin.inventory_revision
    )
      retained.set(key, entry);
  }
  return entries.filter((entry) => !entry.history || retained.get(agentKey(entry.agent)) === entry);
}
