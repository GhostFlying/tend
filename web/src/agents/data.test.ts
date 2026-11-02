import { expect, test, vi } from "vitest";
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

test("runner names are shared across consumers and current names override retained labels", async () => {
  const api: Reader = {
    async get<T>() {
      return {
        host_id: "host",
        online: false,
        last_heartbeat_at_ms: 1,
        retained_definitions: [
          {
            identity: { execution_environment_id: "host", local_id: "cedar" },
            display_name: "Old Cedar",
          },
        ],
        projection: {
          agents: [
            {
              identity: { execution_environment_id: "host", local_id: "cedar" },
              display_name: "青杉",
            },
          ],
        },
      } as T;
    },
  };
  const data = new AgentCatalog(api);
  expect(data.runnerFor("host")).toBe(data.runnerFor("host"));
  data.ensureRunner("host");
  await data.runnerFor("host").refresh();
  expect(data.names.get().get("host/cedar")).toBe("青杉");
  expect(data.catalogs.get().get("host")?.value?.online).toBe(false);
  data.stop();
  expect(data.names.get().size).toBe(0);
});

test("category pagination accumulates earlier pages without duplicating identities", async () => {
  const api: Reader = {
    async get<T>(path: string) {
      return {
        items: [
          {
            category_id: path.includes("after") ? "b" : "a",
            title: "Category",
            description: "",
            state: "active",
            revision: 1,
          },
        ],
        more: !path.includes("after"),
        next: "a",
      } as T;
    },
  };
  const data = new AgentCatalog(api);
  await data.openCategories("all");
  await data.openCategories("all", "a");
  expect(data.categories.state.get().value?.items?.map((c) => c.category_id)).toEqual(["a", "b"]);
  await data.categories.refresh();
  expect(data.categories.state.get().value?.items).toHaveLength(2);
  data.stop();
});

test("refresh follows fresh category boundaries after insertion", async () => {
  let ids = ["a", "b"];
  const api: Reader = {
    async get<T>(path: string) {
      const after = new URL(path, "https://fixture.invalid").searchParams.get("after") ?? "";
      const remaining = ids.filter((id) => id > after);
      const id = remaining[0];
      return {
        items: id
          ? [{ category_id: id, title: id, description: "", state: "active", revision: 1 }]
          : [],
        more: remaining.length > 1,
        next: id,
      } as T;
    },
  };
  const data = new AgentCatalog(api);
  await data.openCategories("all");
  await data.openCategories("all", "a");
  ids = ["0", "a", "b"];
  data.refresh();
  await vi.waitFor(() =>
    expect(data.categories.state.get().value?.items?.map((c) => c.category_id)).toEqual(["0", "a"]),
  );
  expect(data.categories.state.get().value?.next).toBe("a");
  expect(data.categories.state.get().value?.more).toBe(true);
  data.stop();
});
