import { expect, test } from "vitest";
import type { RunnerAgentCatalog } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { AgentCatalog } from "./data.ts";

test("host switch cancels the old inventory and preserves registration, last availability and presence separately", async () => {
  const calls: { path: string; signal?: AbortSignal; answer: (v: unknown) => void }[] = [];
  const api: Reader = {
    get<T>(path: string, signal?: AbortSignal): Promise<T> {
      return new Promise<T>((resolve) =>
        calls.push({ path, ...(signal ? { signal } : {}), answer: (v) => resolve(v as T) }),
      );
    },
  };
  const data = new AgentCatalog(api);
  expect(calls).toHaveLength(0);
  const old = data.openRunner("old/host");
  const current = data.openRunner("current-host");
  expect(calls[0]!.signal?.aborted).toBe(true);
  expect(calls[1]!.path).toBe("/api/v1/hosts/current-host/agents");
  const identity = { execution_environment_id: "current-host", local_id: "investigator" };
  const runtime = { runtime_id: "traex", revision: 1 };
  const hash = `sha256:${"a".repeat(64)}`;
  const observation: RunnerAgentCatalog = {
    host_id: "current-host",
    online: false,
    last_heartbeat_at_ms: 123,
    current: {
      host_id: "current-host",
      state: "accepted",
      server_revision: 2,
      local_revision: 1,
      public_sha256: hash,
      request_id: "registration-request",
      snapshot_id: "snapshot-one",
      categories: [],
    },
    projection: {
      format_version: 1,
      execution_environment_id: "current-host",
      source_kind: "local_file",
      runtimes: [
        {
          ...runtime,
          kind: "appserver-worker",
          declared_purposes: ["worker_run"],
          enabled: true,
          registration: "staged",
          availability: { available: false, reason: "runtime_not_verified" },
        },
      ],
      agents: [
        {
          identity,
          revision: 1,
          display_name: "Investigator",
          runtime_binding_ref: runtime,
          categories: ["investigation"],
          purposes: ["worker_run"],
          requested_model: "named-model",
          requested_effort: "high",
          enabled: true,
          registration: "staged",
          availability: { available: false, reason: "runtime_not_verified" },
        },
      ],
    },
    executors: [
      {
        pin: {
          identity,
          runtime,
          agent_revision: 1,
          inventory_revision: 2,
          snapshot_id: "snapshot-one",
          public_sha256: hash,
          profile: { model: "named-model", effort: "high" },
          worker: "appserver",
          backend_id: "git",
        },
        available: true,
        reason: "last observation",
      },
    ],
  };
  calls[1]!.answer(observation);
  await current;
  calls[0]!.answer({ host_id: "old/host", online: true });
  await old;
  expect(data.runner.state.get().value).toEqual(observation);
  data.stop();
});
