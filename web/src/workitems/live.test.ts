import { afterEach, describe, expect, test, vi } from "vitest";
import type { WorkItemView } from "../api/generated/api.ts";
import { LiveData, type Reader } from "../live/data.ts";
import { WorkItems } from "./live.ts";
const flush = async () => {
  for (let i = 0; i < 10; i++) await Promise.resolve();
};
const item = (id = "a", title = "原计划"): WorkItemView => ({
  work_item_id: id,
  title,
  revision: 1,
  project: "sample",
  intent: "调查",
  status: "open",
  column: "unplanned",
  stages: [],
  created_at: 1,
  updated_at: 1,
});
class Deferred implements Reader {
  pending: {
    path: string;
    signal?: AbortSignal | undefined;
    resolve(value: unknown): void;
    reject(error: Error): void;
  }[] = [];
  calls: string[] = [];
  get<T>(path: string, signal?: AbortSignal): Promise<T> {
    this.calls.push(path);
    // Deliberately ignore abort: stale servers/transports must not overwrite new state.
    return new Promise((resolve, reject) =>
      this.pending.push({ path, signal, resolve: (value) => resolve(value as T), reject }),
    );
  }
  async answer(prefix: string, value: unknown) {
    const index = this.pending.findIndex((read) => read.path.startsWith(prefix));
    expect(index).toBeGreaterThanOrEqual(0);
    this.pending.splice(index, 1)[0]!.resolve(value);
    await flush();
  }
}
afterEach(() => vi.useRealTimers());
describe("live work-item projections", () => {
  test("a slow project response cannot overwrite a new project", async () => {
    const api = new Deferred();
    const live = new WorkItems(api);
    live.openBoard();
    const old = api.pending.shift()!;
    live.setProject("second");
    expect(old.signal?.aborted).toBe(true);
    await api.answer("/api/v1/work-items?include_closed=true&project=second", {
      items: [item("new")],
    });
    old.resolve({ items: [item("stale")] });
    await flush();
    expect(live.board.state.get().value?.items?.map((item) => item.work_item_id)).toEqual(["new"]);
    live.stop();
  });
  test("switching details ignores an old response; closing aborts the current read", async () => {
    const api = new Deferred();
    const live = new WorkItems(api);
    void live.open("a");
    const first = api.pending.shift()!;
    void live.open("b");
    const second = api.pending[0]!;
    first.resolve(item("a"));
    await flush();
    expect(live.detail.state.get().value).toBeNull();
    live.close();
    expect(second.signal?.aborted).toBe(true);
    second.resolve(item("b"));
    await flush();
    expect(live.detail.state.get().value).toBeNull();
    live.stop();
  });
  test("events arriving during a read cause one follow-up projection read", async () => {
    vi.useFakeTimers();
    const api = new Deferred();
    const live = new WorkItems(api);
    live.openBoard();
    live.changed();
    live.changed();
    await vi.advanceTimersByTimeAsync(100);
    expect(api.calls).toHaveLength(1);
    await api.answer("/api/v1/work-items", { items: [item()] });
    expect(api.calls).toHaveLength(2);
    await api.answer("/api/v1/work-items", { items: [item("a", "最新计划")] });
    expect(live.board.state.get().value?.items?.[0]?.title).toBe("最新计划");
    live.stop();
    expect(vi.getTimerCount()).toBe(0);
  });
  test("read failures keep last facts, retry with backoff and stop owns timers", async () => {
    vi.useFakeTimers();
    const api = new Deferred();
    const live = new WorkItems(api);
    live.startOverview();
    await api.answer("/api/v1/work-items", { items: [item()] });
    void live.overview.refresh();
    api.pending.shift()!.reject(new Error("offline"));
    await flush();
    expect(live.overview.state.get()).toMatchObject({
      value: { items: [item()] },
      error: "offline",
      loading: false,
    });
    await vi.advanceTimersByTimeAsync(1000);
    expect(api.pending).toHaveLength(1);
    const retry = api.pending[0]!;
    live.stop();
    expect(retry.signal?.aborted).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });
  test("task completions, delivery events, and reconnect refresh active lists/details", async () => {
    vi.useFakeTimers();
    const paths: string[] = [];
    const api: Reader = {
      async get<T>(path: string) {
        paths.push(path);
        if (path === "/api/v1/work-items/a") return item() as T;
        if (path.startsWith("/api/v1/work-items")) return { items: [item()] } as T;
        if (path.startsWith("/api/v1/conversation?")) return { items: [], more: false } as T;
        return { event_seq: 1, tasks: [], decisions: [], hosts: [], projects: [], queue: [] } as T;
      },
    };
    const live = new LiveData(api);
    live.workItems.openBoard();
    await live.workItems.open("a");
    await flush();
    paths.length = 0;
    live.apply({
      event_seq: 2,
      object_type: "task",
      object_id: "t",
      kind: "task.completed",
      created_at: "2026-10-25T00:00:00Z",
    });
    await vi.advanceTimersByTimeAsync(100);
    expect(paths).toContain("/api/v1/work-items?include_closed=true");
    expect(paths).toContain("/api/v1/work-items/a");
    paths.length = 0;
    live.apply({
      event_seq: 3,
      object_type: "work_item",
      object_id: "a",
      kind: "work_item.delivery_checked",
      created_at: "2026-10-25T00:00:00Z",
    });
    await vi.advanceTimersByTimeAsync(100);
    expect(paths).toContain("/api/v1/work-items/a");
    paths.length = 0;
    await live.reload();
    await flush();
    expect(paths).toContain("/api/v1/work-items?include_closed=true");
    expect(paths).toContain("/api/v1/work-items/a");
    await live.stop();
    expect(vi.getTimerCount()).toBe(0);
  });
});
