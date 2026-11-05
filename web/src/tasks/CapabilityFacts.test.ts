import { expect, test } from "vitest";
import type { AgentCapabilityView, RunExecutionPin } from "../api/generated/api.ts";
import { capabilityCandidateKey } from "./CapabilityFacts.tsx";

const identity = { execution_environment_id: "host", local_id: "alpha" };
const runtime = { runtime_id: "worker", revision: 1 };
const pin: RunExecutionPin = {
  identity,
  agent_revision: 1,
  runtime,
  inventory_revision: 1,
  snapshot_id: "snapshot",
  public_sha256: "hash",
  profile: { model: "model", effort: "high" },
  worker: "orca",
  backend_id: "git",
  purpose: "worker_run",
};
const candidate: AgentCapabilityView = {
  identity,
  agent_revision: 1,
  runtime_binding_ref: runtime,
  categories: [],
  purposes: [],
  declared: [],
  evidence: [],
  pin,
  current: {
    state: "eligible",
    required: [],
    reasons: [],
    evidence_ids: [],
    checked_at_ms: 1000,
  },
};

test("candidate keys distinguish every frozen pin field and ignore changing match facts", () => {
  const variations: RunExecutionPin[] = [
    { ...pin, identity: { ...identity, execution_environment_id: "other" } },
    { ...pin, identity: { ...identity, local_id: "other" } },
    { ...pin, agent_revision: 2 },
    { ...pin, runtime: { ...runtime, runtime_id: "other" } },
    { ...pin, runtime: { ...runtime, revision: 2 } },
    { ...pin, inventory_revision: 2 },
    { ...pin, snapshot_id: "other" },
    { ...pin, public_sha256: "other" },
    { ...pin, profile: { ...pin.profile, model: "other" } },
    { ...pin, profile: { ...pin.profile, effort: "medium" } },
    { ...pin, worker: "appserver" },
    { ...pin, backend_id: "other" },
    { ...pin, purpose: "issue_manager" },
  ];
  const keys = [candidate, ...variations.map((pin) => ({ ...candidate, pin }))].map(
    capabilityCandidateKey,
  );
  expect(new Set(keys).size).toBe(keys.length);
  const reordered = Object.fromEntries(Object.entries(pin).reverse()) as unknown as RunExecutionPin;
  expect(capabilityCandidateKey({ ...candidate, pin: reordered })).toBe(keys[0]);
  expect(
    capabilityCandidateKey({
      ...candidate,
      declared: [{ capability_id: "image_gen", definition_revision: 1 }],
      current: { ...candidate.current, checked_at_ms: 2000, state: "unavailable" },
    }),
  ).toBe(keys[0]);
});

test("missing pins use a separate revision-aware fallback without colliding with pinned rows", () => {
  const unpinned = { ...candidate };
  delete unpinned.pin;
  const variants: AgentCapabilityView[] = [
    candidate,
    unpinned,
    { ...unpinned, identity: { ...identity, execution_environment_id: "other" } },
    { ...unpinned, identity: { ...identity, local_id: "other" } },
    { ...unpinned, agent_revision: 2 },
    { ...unpinned, runtime_binding_ref: { ...runtime, runtime_id: "other" } },
    { ...unpinned, runtime_binding_ref: { ...runtime, revision: 2 } },
  ];
  expect(new Set(variants.map(capabilityCandidateKey)).size).toBe(variants.length);
  const minimal = { ...pin, profile: { ...pin.profile } };
  delete minimal.purpose;
  delete minimal.backend_id;
  delete minimal.profile.effort;
  expect(capabilityCandidateKey({ ...candidate, pin: minimal })).toBe(
    capabilityCandidateKey({
      ...candidate,
      pin: { ...minimal, purpose: "", backend_id: "", profile: { model: "model", effort: "" } },
    }),
  );
});
