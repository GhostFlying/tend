import { expect, test } from "vitest";
import { currentObservation } from "./facts.ts";
import { runtimeLabel } from "./runtimeLabel.ts";
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
});
