import { expect, test } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type {
  AgentCapabilityView,
  AgentInventoryDefinition,
  RunnerAgentCatalog,
} from "../api/generated/api.ts";
import { CapabilityFacts, capabilityFacts } from "./CapabilityFacts.tsx";

const agent: AgentInventoryDefinition = {
  identity: { execution_environment_id: "host", local_id: "agent" },
  revision: 2,
  display_name: "Agent",
  runtime_binding_ref: { runtime_id: "worker", revision: 1 },
  categories: ["implementation"],
  purposes: ["worker_run"],
  requested_model: "model",
  requested_effort: "",
  enabled: true,
  registration: "staged",
  availability: { available: false, reason: "runtime_not_verified" },
  capabilities: [{ capability_id: "image_gen", definition_revision: 1 }],
};
const view: AgentCapabilityView = {
  identity: agent.identity,
  agent_revision: 2,
  runtime_binding_ref: agent.runtime_binding_ref,
  categories: agent.categories,
  purposes: agent.purposes,
  declared: agent.capabilities!,
  evidence: [],
  pin: {
    identity: agent.identity,
    agent_revision: 2,
    runtime: agent.runtime_binding_ref,
    inventory_revision: 2,
    snapshot_id: "snapshot",
    public_sha256: "hash",
    profile: { model: "model" },
    worker: "orca",
    backend_id: "orca",
  },
  current: {
    state: "eligible",
    required: agent.capabilities!,
    reasons: [
      {
        capability: agent.capabilities![0]!,
        state: "eligible",
        reason: "capability_declared",
        recovery: "recheck",
      },
    ],
    evidence_ids: [],
    checked_at_ms: 1000,
  },
};
const catalog: RunnerAgentCatalog = {
  host_id: "host",
  online: true,
  last_heartbeat_at_ms: 1000,
  current: {
    host_id: "host",
    server_revision: 2,
    local_revision: 2,
    snapshot_id: "snapshot",
    public_sha256: "hash",
    request_id: "request",
    state: "registered_staged",
    categories: [],
  },
  capabilities: [view],
};

test("capability display never transfers an old configuration, offline or historical confirmation", () => {
  expect(capabilityFacts(catalog, agent, false)).toBe(view);
  expect(capabilityFacts(catalog, agent, true)).toBeUndefined();
  expect(capabilityFacts({ ...catalog, online: false }, agent, false)).toBeUndefined();
  expect(capabilityFacts(catalog, { ...agent, revision: 3 }, false)).toBeUndefined();
  expect(capabilityFacts(catalog, { ...agent, requested_model: "other" }, false)).toBeUndefined();
  expect(
    capabilityFacts(
      { ...catalog, current: { ...catalog.current!, snapshot_id: "other" } },
      agent,
      false,
    ),
  ).toBeUndefined();
  expect(capabilityFacts(catalog, agent, false)?.current.state).toBe("eligible");
  expect(view.current.state).toBe("eligible");
});

test("rendered declaration and tool facts do not claim a successful real generation", () => {
  const html = renderToStaticMarkup(
    <dl>
      <CapabilityFacts catalog={catalog} agent={agent} historical={false} />
    </dl>,
  );
  expect(html).toContain("image_gen@1");
  expect(html).toContain("尚无真实调用证据");
  expect(html).toContain("配置声明满足要求；实际执行未验证");
});
