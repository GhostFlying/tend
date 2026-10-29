import { afterEach, describe, expect, test, vi } from "vitest";
import type { Reader } from "../live/data.ts";
import { WorkItemRead } from "./live.ts";
const flush = async () => {
  for (let i = 0; i < 10; i++) await Promise.resolve();
};
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
describe("shared owner projection reads", () => {
  test("switching paths ignores an old response; closing aborts the current read", async () => {
    const api = new Deferred();
    const read = new WorkItemRead<{ title: string }>(api);
    void read.open("/api/v1/issues/a");
    const first = api.pending.shift()!;
    void read.open("/api/v1/issues/b");
    const second = api.pending[0]!;
    expect(first.signal?.aborted).toBe(true);
    first.resolve({ title: "old" });
    await flush();
    expect(read.state.get().value).toBeNull();
    read.close();
    expect(second.signal?.aborted).toBe(true);
    second.resolve({ title: "late" });
    await flush();
    expect(read.state.get().value).toBeNull();
  });
  test("events arriving during a read cause one follow-up projection read", async () => {
    const api = new Deferred();
    const read = new WorkItemRead<{ title: string }>(api);
    void read.open("/api/v1/issues/a");
    void read.refresh();
    void read.refresh();
    expect(api.calls).toHaveLength(1);
    await api.answer("/api/v1/issues/a", { title: "first" });
    expect(api.calls).toHaveLength(2);
    await api.answer("/api/v1/issues/a", { title: "current" });
    expect(read.state.get().value?.title).toBe("current");
    read.close();
  });
  test("read failures keep last facts, retry with backoff and close owns timers", async () => {
    vi.useFakeTimers();
    const api = new Deferred();
    const read = new WorkItemRead<{ title: string }>(api);
    void read.open("/api/v1/issues/a");
    await api.answer("/api/v1/issues/a", { title: "accepted" });
    void read.refresh();
    api.pending.shift()!.reject(new Error("offline"));
    await flush();
    expect(read.state.get()).toMatchObject({
      value: { title: "accepted" },
      error: "offline",
      loading: false,
    });
    await vi.advanceTimersByTimeAsync(1000);
    expect(api.pending).toHaveLength(1);
    const retry = api.pending[0]!;
    read.close();
    expect(retry.signal?.aborted).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });
});
