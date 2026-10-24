import { expect, test } from "vitest";
import type { RepoRegistry } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";
import { RepositoryRegistry } from "./registry.ts";

function setup() {
  const waiting: {
    resolve(view: RepoRegistry): void;
    reject(err: Error): void;
    signal?: AbortSignal | undefined;
  }[] = [];
  const paths: string[] = [];
  // Deliberately ignores cancellation to exercise the generation guard.
  const api: Reader = {
    get<T>(path: string, signal?: AbortSignal) {
      paths.push(path);
      return new Promise<T>((resolve, reject) =>
        waiting.push({ resolve: (view) => resolve(view as T), reject, signal }),
      );
    },
  };
  return { registry: new RepositoryRegistry(api), waiting, paths };
}
const view = (repo: string): RepoRegistry => ({ repos: [{ repo, hosts: [] }] });
const settle = async () => {
  await Promise.resolve();
  await Promise.resolve();
};

test("first read, failure, retry, empty and disconnect keep unknown distinct from zero", async () => {
  const { registry, waiting, paths } = setup();
  expect(registry.state.get().view).toBeNull();
  registry.setConnected(true);
  expect(registry.state.get().loading).toBe(true);
  waiting[0]!.reject(new Error("failed"));
  await settle();
  expect(registry.state.get()).toMatchObject({ view: null, loading: false, error: true });
  const retry = registry.refresh();
  waiting[1]!.resolve({ repos: [] });
  await retry;
  expect(registry.state.get()).toEqual({
    view: { repos: [] },
    loading: false,
    stale: false,
    error: false,
  });
  registry.setConnected(false);
  expect(registry.state.get()).toMatchObject({ view: { repos: [] }, stale: true });
  expect(paths).toEqual(["/api/v1/repos", "/api/v1/repos"]);
});

test("new reads win over older success and failure, retaining records on failed refresh", async () => {
  const { registry, waiting } = setup();
  registry.setConnected(true);
  const fresh = registry.refresh();
  expect(waiting[0]!.signal?.aborted).toBe(true);
  waiting[1]!.resolve(view("fresh"));
  await fresh;
  waiting[0]!.resolve(view("old"));
  await settle();
  expect(registry.state.get().view).toEqual(view("fresh"));
  const old = registry.refresh();
  const newer = registry.refresh();
  waiting[3]!.resolve(view("newer"));
  await newer;
  waiting[2]!.reject(new Error("old failure"));
  await old;
  expect(registry.state.get()).toMatchObject({ view: view("newer"), error: false, stale: false });
  const failed = registry.refresh();
  waiting[4]!.reject(new Error("latest failure"));
  await failed;
  expect(registry.state.get()).toMatchObject({ view: view("newer"), error: true, stale: true });
});

test("disconnect cancels pending reads, reconnect updates and stop rejects late data", async () => {
  const { registry, waiting } = setup();
  registry.setConnected(true);
  waiting[0]!.resolve(view("before"));
  await settle();
  const pending = registry.refresh();
  registry.setConnected(false);
  waiting[1]!.resolve(view("late"));
  await pending;
  expect(registry.state.get()).toMatchObject({ view: view("before"), stale: true, loading: false });
  await registry.refresh();
  expect(waiting).toHaveLength(2);
  registry.setConnected(true);
  waiting[2]!.resolve(view("reconnected"));
  await settle();
  expect(registry.state.get()).toMatchObject({ view: view("reconnected"), stale: false });
  const last = registry.refresh();
  registry.stop();
  waiting[3]!.resolve(view("stopped"));
  await last;
  expect(registry.state.get().view).toEqual(view("reconnected"));
});
