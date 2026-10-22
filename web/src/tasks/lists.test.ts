import { describe, expect, test, vi } from "vitest";
import type { TaskPage, TaskSummary } from "../api/generated/api.ts";
import { TaskFeed, TaskLists } from "./lists.ts";

function task(id: string, project = "demo"): TaskSummary {
  return {
    task_id: id,
    title: id,
    lifecycle: "running",
    project,
    rating: "",
    control_mode: "managed",
    control_revision: 1,
    spec_version: 1,
    next_implement_generation: 1,
    block_reason: "none",
    workspace: "",
  };
}
class Api {
  calls: { path: string; signal?: AbortSignal | undefined; resolve(value: TaskPage): void }[] = [];
  get<T>(path: string, signal?: AbortSignal): Promise<T> {
    return new Promise((resolve) => {
      this.calls.push({ path, signal, resolve: (v) => resolve(v as T) });
    });
  }
}
const flush = async () => {
  for (let i = 0; i < 15; i++) await Promise.resolve();
};

describe("independent task pages", () => {
  test("pages deduplicate boundary rows and stop at an absent cursor", async () => {
    const api = new Api();
    const feed = new TaskFeed(api, ["running"]);
    feed.start();
    expect(api.calls[0]?.path).toContain("all=1&lifecycle=running&limit=50");
    api.calls[0]!.resolve({ tasks: [task("one")], next: "cursor-one" });
    await flush();
    const more = feed.more();
    expect(api.calls[1]?.path).toContain("before=cursor-one");
    api.calls[1]!.resolve({ tasks: [task("one"), task("two")], next: "" });
    await more;
    expect(feed.state.get()).toMatchObject({
      items: [task("one"), task("two")],
      pages: 2,
      next: undefined,
    });
    await feed.more();
    expect(api.calls).toHaveLength(2);
    await feed.stop();
  });
  test("filter changes cancel previous pages and reject their late responses", async () => {
    const api = new Api();
    const feed = new TaskFeed(api, ["running"]);
    feed.start();
    api.calls[0]!.resolve({ tasks: [task("one")], next: "old-cursor" });
    await flush();
    const old = feed.more();
    feed.setProject("other");
    expect(api.calls[1]?.signal?.aborted).toBe(true);
    expect(api.calls[2]?.path).toContain("project=other");
    expect(api.calls[2]?.path).not.toContain("before=");
    api.calls[2]!.resolve({ tasks: [task("new", "other")] });
    await flush();
    api.calls[1]!.resolve({ tasks: [task("old")], next: "stale" });
    await old;
    expect(feed.state.get().items).toEqual([task("new", "other")]);
    expect(feed.state.get().next).toBeUndefined();
    await feed.stop();
  });
  test("refresh rebuilds the loaded range using fresh cursors and drops moved rows", async () => {
    const api = new Api();
    const feed = new TaskFeed(api, ["running"]);
    feed.start();
    api.calls[0]!.resolve({ tasks: [task("old")], next: "old-page" });
    await flush();
    const page = feed.more();
    api.calls[1]!.resolve({ tasks: [task("two")] });
    await page;
    const refresh = feed.refresh();
    expect(api.calls[2]!.path).not.toContain("before=");
    api.calls[2]!.resolve({ tasks: [task("three")], next: "fresh-page" });
    await flush();
    expect(api.calls[3]!.path).toContain("before=fresh-page");
    api.calls[3]!.resolve({ tasks: [task("four")] });
    await refresh;
    expect(feed.state.get().items.map((t) => t.task_id)).toEqual(["three", "four"]);
    await feed.stop();
  });
  test("a failed refresh blocks old cursors until a successful fresh read", async () => {
    let fail = false;
    const api = {
      get: async <T>() => {
        if (fail) throw new Error("temporary");
        return { tasks: [task("one")], next: "next" } as T;
      },
    };
    const feed = new TaskFeed(api, ["running"]);
    feed.start();
    await flush();
    fail = true;
    await feed.refresh();
    expect(feed.state.get().error).toBe("temporary");
    await feed.more();
    expect(feed.state.get().pages).toBe(1);
    fail = false;
    await feed.refresh();
    expect(feed.state.get().error).toBeUndefined();
    await feed.stop();
  });
  test("stopping an older request cannot clear the loading state of a reopened feed", async () => {
    const api = new Api();
    const feed = new TaskFeed(api, ["running"]);
    feed.start();
    const stopped = feed.stop();
    feed.start();
    api.calls[0]!.resolve({ tasks: [task("old")] });
    await stopped;
    expect(feed.state.get().loading).toBe(true);
    api.calls[1]!.resolve({ tasks: [task("new")] });
    await flush();
    expect(feed.state.get().items).toEqual([task("new")]);
    await feed.stop();
  });
});

test.each([25, 150])("continuous %ims events cannot starve slow task reads", async (interval) => {
  vi.useFakeTimers();
  const calls: AbortSignal[] = [];
  let completed = 0;
  const api = {
    get: async <T>(_path: string, signal?: AbortSignal) => {
      calls.push(signal!);
      await new Promise((resolve) => setTimeout(resolve, 500));
      completed++;
      return { tasks: [task(`visible-${completed}`)] } as T;
    },
  };
  const lists = new TaskLists(api);
  lists.open();
  try {
    for (let elapsed = 0; elapsed < 1500; elapsed += interval) {
      lists.changed();
      await vi.advanceTimersByTimeAsync(interval);
    }
    expect(lists.columns[1]!.state.get().items.length).toBe(1);
    expect(completed).toBeGreaterThanOrEqual(12);
    expect(calls.length).toBeLessThanOrEqual(24);
    expect(calls.every((s) => !s.aborted)).toBe(true);
    const stopped = lists.stop();
    await vi.advanceTimersByTimeAsync(500);
    await stopped;
  } finally {
    vi.useRealTimers();
  }
});
