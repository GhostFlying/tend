import { expect, test } from "vitest";
import {
  activityOrder,
  activeEntries,
  directoryEntry,
  taskActivity,
  type Group,
} from "./entries.ts";
import { currentObservation } from "./facts.ts";
import { runtimeLabel, runtimeMethod } from "./runtimeLabel.ts";
import type {
  AgentExecutor,
  AgentInventoryDefinition,
  RunnerAgentCatalog,
} from "../api/generated/api.ts";

const agent: AgentInventoryDefinition = {
  identity: { execution_environment_id: "host", local_id: "cedar" },
  revision: 2,
  display_name: "Cedar",
  runtime_binding_ref: { runtime_id: "terminal", revision: 1 },
  owner_runtime_binding_ref: { runtime_id: "native", revision: 1 },
  categories: ["implementation"],
  purposes: ["worker_run"],
  requested_model: "model",
  requested_effort: "high",
  enabled: true,
  registration: "staged",
  availability: { available: false, reason: "runtime_not_verified" },
};
const worker: AgentExecutor = {
  available: true,
  pin: {
    identity: agent.identity,
    agent_revision: 2,
    runtime: agent.runtime_binding_ref,
    inventory_revision: 2,
    snapshot_id: "new",
    public_sha256: "hash",
    profile: { model: "model", effort: "high" },
    worker: "orca",
    backend_id: "orca",
  },
};
const catalog: RunnerAgentCatalog = {
  host_id: "host",
  online: true,
  last_heartbeat_at_ms: 1,
  current: {
    host_id: "host",
    server_revision: 2,
    local_revision: 2,
    snapshot_id: "new",
    public_sha256: "hash",
    request_id: "request",
    state: "registered_staged",
    categories: [],
  },
  executors: [worker],
};

test("observations match snapshot, role and complete definition rather than identity alone", () => {
  expect(currentObservation(catalog, agent, "")).toBe(worker);
  expect(currentObservation(catalog, agent, "issue_manager")).toBeUndefined();
  for (const pin of [
    { ...worker.pin, snapshot_id: "old" },
    { ...worker.pin, inventory_revision: 1 },
    { ...worker.pin, agent_revision: 1 },
    { ...worker.pin, public_sha256: "old-hash" },
    { ...worker.pin, profile: { model: "other", effort: "high" } },
  ])
    expect(
      currentObservation({ ...catalog, executors: [{ ...worker, pin }] }, agent, ""),
    ).toBeUndefined();
  const manager: AgentExecutor = {
    ...worker,
    pin: {
      ...worker.pin,
      purpose: "issue_manager",
      runtime: agent.owner_runtime_binding_ref!,
      worker: "appserver",
      backend_id: "",
    },
  };
  expect(currentObservation({ ...catalog, executors: [manager, worker] }, agent, "")).toBe(worker);
  expect(
    currentObservation({ ...catalog, executors: [worker, manager] }, agent, "issue_manager"),
  ).toBe(manager);
});

test("environment labels retain adapter and CLI and never invent missing CLI identity", () => {
  expect(runtimeLabel({ kind: "orca-worker", cli: "codex" })).toBe("orca/codex");
  expect(runtimeLabel({ kind: "orca-worker", cli: "traex" })).toBe("orca/traex");
  expect(runtimeLabel({ kind: "appserver-worker", cli: "codex" })).toBe("app-server/codex");
  expect(runtimeLabel({ kind: "appserver-worker" })).toBe("app-server/CLI 未记录");
  expect(runtimeLabel({ kind: "central-traex-worker", cli: "traex" })).toBe("central/traex");
  expect(runtimeMethod({ kind: "hosted-manager", cli: "codex" })).toBe("Codex · app-server");
  expect(runtimeMethod({ kind: "hosted-manager" })).toBe("app-server");
  expect(runtimeMethod({ kind: "orca-worker", cli: "traex" })).toBe("TraeX · Orca");
});

test("activity uses the frozen identity aggregate and preserves order within load groups", () => {
  const group: Group = {
    id: "host",
    projection: {
      format_version: 1,
      execution_environment_id: "host",
      source_kind: "local_file",
      agents: [],
      runtimes: [],
    },
    taskActivity: { running_tasks: { cedar: 2, maple: 1 }, uncertain_tasks: { iris: 1 } },
  };
  const entries = ["idle", "cedar", "other-idle", "iris", "maple"].map((local_id) =>
    directoryEntry(group, { ...agent, identity: { execution_environment_id: "host", local_id } }),
  );
  expect(
    [...entries]
      .sort((a, b) => activityOrder(a) - activityOrder(b))
      .map((e) => e.agent.identity.local_id),
  ).toEqual(["cedar", "maple", "iris", "idle", "other-idle"]);
  expect(taskActivity(entries[1]!)).toEqual({ running: 2, uncertain: 0 });
  expect(taskActivity(entries[0]!)).toEqual({ running: 0, uncertain: 0 });
  expect(
    taskActivity({ ...entries[1]!, group: { ...group, taskActivity: undefined } }),
  ).toBeUndefined();
  expect(
    taskActivity({ ...entries[1]!, history: { pin: worker.pin, definition: agent, runtimes: [] } }),
  ).toEqual({ running: 2, uncertain: 0 });
  group.taskActivity!.running_tasks = {};
  expect(
    [...entries]
      .sort((a, b) => activityOrder(a) - activityOrder(b))
      .map((e) => e.agent.identity.local_id),
  ).toEqual(["iris", "idle", "cedar", "other-idle", "maple"]);
});

test("working retired identities remain discoverable without repeating their task total", () => {
  const group: Group = {
    id: "host",
    projection: {
      format_version: 1,
      execution_environment_id: "host",
      source_kind: "local_file",
      agents: [],
      runtimes: [],
    },
    taskActivity: { running_tasks: { cedar: 2 }, uncertain_tasks: { maple: 1 } },
  };
  const history = (local_id: string, revision: number) => {
    const definition = { ...agent, identity: { execution_environment_id: "host", local_id } };
    return directoryEntry(group, definition, {
      definition,
      pin: { ...worker.pin, inventory_revision: revision, snapshot_id: String(revision) },
      runtimes: [],
    });
  };
  const old = history("cedar", 1),
    latest = history("cedar", 2),
    uncertain = history("maple", 1),
    idle = history("iris", 1);
  expect(activeEntries([old, latest, uncertain, idle])).toEqual([latest, uncertain]);
  const current = directoryEntry(group, agent);
  expect(activeEntries([current, old, latest, uncertain, idle])).toEqual([current, uncertain]);
  group.taskActivity!.running_tasks = {};
  expect(activeEntries([old, latest, uncertain, idle])).toEqual([uncertain]);
});
