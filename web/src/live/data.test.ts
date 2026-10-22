import { describe, expect, test, vi } from "vitest";
import { ApiError } from "../api/client.ts";
import type { ConversationItem, LedgerEvent } from "../api/generated/api.ts";
import { processing } from "../conversation/model.ts";
import { LiveData, type Reader } from "./data.ts";

/** A fake owner API whose answers the test releases one by one. */
class FakeApi implements Reader {
  calls: string[] = [];
  private waiting: { path: string; resolve: (v: unknown) => void; reject: (e: unknown) => void }[] =
    [];

  get<T>(path: string, signal?: AbortSignal): Promise<T> {
    this.calls.push(path);
    return new Promise<T>((resolve, reject) => {
      const clean = () => signal?.removeEventListener("abort", abort);
      const item = {
        path,
        resolve: (v: unknown) => {
          clean();
          resolve(v as T);
        },
        reject: (e: unknown) => {
          clean();
          reject(e);
        },
      };
      const abort = () => {
        const index = this.waiting.indexOf(item);
        if (index >= 0) this.waiting.splice(index, 1);
        item.reject(new DOMException("aborted", "AbortError"));
      };
      this.waiting.push(item);
      if (signal?.aborted) abort();
      else signal?.addEventListener("abort", abort, { once: true });
    });
  }

  /** Answers the oldest pending read of a path starting with prefix. */
  async answer(prefix: string, value: unknown): Promise<void> {
    const i = this.waiting.findIndex((w) => w.path.startsWith(prefix));
    if (i < 0) {
      throw new Error(`no pending read of ${prefix}; pending: ${this.pending().join(", ")}`);
    }
    const [w] = this.waiting.splice(i, 1);
    if (value instanceof Error) {
      w!.reject(value);
    } else {
      w!.resolve(value);
    }
    await settle();
  }

  pending(): string[] {
    return this.waiting.map((w) => w.path);
  }
}

const settle = () => new Promise((r) => setTimeout(r, 0));

function owner(seq: number, state: ConversationItem["state"] = "queued"): ConversationItem {
  return {
    seq,
    kind: "message",
    message_id: `msg_${seq}`,
    author: "owner",
    body: `hello ${seq}`,
    created_at: "2026-10-22T10:00:00Z",
    state,
  };
}

function messageEvent(
  seq: number,
  item: ConversationItem,
  kind = "conversation.message",
): LedgerEvent {
  return {
    event_seq: seq,
    object_type: "message",
    object_id: item.message_id,
    kind,
    created_at: "2026-10-22T10:00:05Z",
    item,
  };
}

const snapshot = { event_seq: 1, tasks: [], decisions: [], hosts: [], projects: [], queue: [] };

describe("LiveData", () => {
  test("a reload reads the snapshot and the latest page", async () => {
    const api = new FakeApi();
    const live = new LiveData(api);
    void live.reload();
    expect(api.pending()).toEqual(["/api/v1/snapshot", "/api/v1/conversation?limit=50"]);
    await api.answer("/api/v1/snapshot", snapshot);
    await api.answer("/api/v1/conversation", { items: [owner(1, "replied")], more: true });
    expect(live.snapshot.get()).toEqual(snapshot);
    expect(live.conversation.get()).toMatchObject({
      items: [owner(1, "replied")],
      more: true,
      loaded: true,
    });
  });

  test("conversation events update their item in place", async () => {
    const api = new FakeApi();
    const live = new LiveData(api);
    void live.reload();
    await api.answer("/api/v1/snapshot", snapshot);
    await api.answer("/api/v1/conversation", { items: [owner(1), owner(2)], more: false });
    live.apply(messageEvent(10, owner(2, "processing"), "conversation.message_state"));
    live.apply(messageEvent(11, owner(3)));
    const c = live.conversation.get();
    expect(c.items.map((i) => [i.seq, i.state])).toEqual([
      [1, "queued"],
      [2, "processing"],
      [3, "queued"],
    ]);
    expect(c.processingSince.get("msg_2")).toBe(Date.parse("2026-10-22T10:00:05Z"));
    // Nothing else was read.
    expect(api.pending()).toEqual([]);
  });

  test("events during a reload are applied after the page, in order", async () => {
    const api = new FakeApi();
    const live = new LiveData(api);
    void live.reload();
    live.apply(messageEvent(5, owner(1, "processing"), "conversation.message_state"));
    live.apply(messageEvent(6, owner(1, "replied"), "conversation.message_state"));
    // The page was read between the two state changes.
    await api.answer("/api/v1/conversation", { items: [owner(1, "processing")], more: false });
    expect(live.conversation.get().items.map((i) => i.state)).toEqual(["replied"]);
  });

  test("other events read the snapshot again, once per burst", async () => {
    const api = new FakeApi();
    const live = new LiveData(api);
    const task: LedgerEvent = {
      event_seq: 2,
      object_type: "task",
      object_id: "task_1",
      kind: "task.created",
      created_at: "2026-10-22T10:00:00Z",
    };
    live.apply(task);
    live.apply({ ...task, event_seq: 3 });
    live.apply({ ...task, event_seq: 4, object_type: "decision" });
    expect(api.pending()).toEqual(["/api/v1/snapshot"]);
    await api.answer("/api/v1/snapshot", snapshot);
    // One more read covers the events that came during the first.
    expect(api.pending()).toEqual(["/api/v1/snapshot"]);
    await api.answer("/api/v1/snapshot", snapshot);
    expect(api.pending()).toEqual([]);
    live.apply({ ...task, event_seq: 5, object_type: "outbox" });
    expect(api.pending()).toEqual([]);
  });

  test("an open task's details are read again on ledger events", async () => {
    const api = new FakeApi();
    const live = new LiveData(api);
    void live.open("task_1");
    await api.answer("/api/v1/tasks/task_1", { task_id: "task_1", title: "t", runs: [] });
    expect(live.detail.get()?.task?.title).toBe("t");
    live.apply({
      event_seq: 9,
      object_type: "run",
      object_id: "run_1",
      kind: "run.assigned",
      created_at: "2026-10-22T10:00:00Z",
    });
    expect(api.pending()).toEqual(["/api/v1/snapshot", "/api/v1/tasks/task_1"]);
  });

  test("run logs resume from their cursor and expose an unfinished read", async () => {
    const api = new FakeApi();
    const live = new LiveData(api);
    const task = { task_id: "task_1", title: "t", runs: [{ run_id: "run_1" }] };
    void live.open("task_1");
    await api.answer("/api/v1/tasks/task_1", task);
    for (let seq = 1; seq <= 20; seq++) {
      expect(api.pending()).toContain(`/api/v1/runs/run_1/logs?after=${seq - 1}`);
      await api.answer("/api/v1/runs/run_1/logs", {
        logs: [{ seq, kind: "output", created_at: "2026-10-22T10:00:00Z", body: `line ${seq}` }],
        next: seq,
      });
    }
    expect(live.detail.get()?.logs).toMatchObject({ more: true });
    void live.refreshDetail();
    await api.answer("/api/v1/tasks/task_1", task);
    expect(api.pending()).toContain("/api/v1/runs/run_1/logs?after=20");
    await api.answer("/api/v1/runs/run_1/logs", { logs: [], next: 20 });
    expect(live.detail.get()?.logs?.more).toBe(false);
    expect(live.detail.get()?.logs?.entries).toHaveLength(20);
  });

  test("failed view reads retry without a stream event and stop owns retries", async () => {
    const api = new FakeApi();
    const live = new LiveData(api);
    void live.reload();
    await api.answer("/api/v1/snapshot", new Error("temporary"));
    await api.answer("/api/v1/conversation", new Error("temporary"));
    await vi.waitFor(() => expect(api.pending()).toHaveLength(2), { timeout: 1800 });
    await api.answer("/api/v1/snapshot", snapshot);
    await api.answer("/api/v1/conversation", { items: [owner(1, "replied")], more: false });
    expect(live.snapshot.get()).toEqual(snapshot);
    expect(live.conversation.get().loaded).toBe(true);
    expect(live.conversation.get().error).toBeUndefined();
    void live.refreshSnapshot();
    await api.answer("/api/v1/snapshot", new Error("temporary again"));
    await live.stop();
    const reads = api.calls.length;
    await new Promise((r) => setTimeout(r, 1100));
    expect(api.calls).toHaveLength(reads);
  });

  test("an old live item never moves the contiguous history cursor", async () => {
    const api = new FakeApi();
    const live = new LiveData(api);
    void live.reload();
    await api.answer("/api/v1/snapshot", snapshot);
    await api.answer("/api/v1/conversation", {
      items: Array.from({ length: 50 }, (_, i) => owner(i + 51, "replied")),
      more: true,
    });
    live.apply(messageEvent(101, owner(1, "interrupted"), "conversation.message_state"));
    expect(live.conversation.get().before).toBe(51);
    void live.loadEarlier();
    expect(api.pending()).toContain("/api/v1/conversation?before=51&limit=50");
    await api.answer("/api/v1/conversation", {
      items: Array.from({ length: 50 }, (_, i) => owner(i + 1, "replied")),
      more: false,
    });
    expect(live.conversation.get().items).toHaveLength(100);
    // A live update must survive an overlapping page read.
    expect(live.conversation.get().items[0]?.state).toBe("interrupted");
    await live.stop();
  });

  test.each(["tools", "queued"])("latest %s page reads back to the active owner", async (kind) => {
    const api = new FakeApi();
    const live = new LiveData(api);
    void live.reload();
    await api.answer("/api/v1/snapshot", snapshot);
    const recent = Array.from({ length: 50 }, (_, i) =>
      kind === "queued"
        ? owner(i + 2)
        : {
            ...owner(i + 2),
            author: "main_agent",
            kind: "tool_use",
            reply_to: "msg_1",
            state: undefined,
          },
    );
    await api.answer("/api/v1/conversation", { items: recent, more: true });
    expect(api.pending()).toContain("/api/v1/conversation?before=2&limit=50");
    live.apply(messageEvent(103, owner(1, "processing"), "conversation.message_state"));
    await api.answer("/api/v1/conversation", { items: [owner(1)], more: false });
    expect(processing(live.conversation.get().items)?.message_id).toBe("msg_1");
    await live.stop();
  });

  test("active logs update without ledger events and cancel on run, task, close and stop", async () => {
    const api = new FakeApi();
    const live = new LiveData(api);
    const detail = (runId: string) => ({
      task_id: "task_1",
      runs: [{ run_id: runId, observed: "running" }],
    });
    void live.open("task_1");
    await api.answer("/api/v1/tasks/task_1", detail("run_1"));
    await api.answer("/api/v1/runs/run_1/logs", { logs: [], next: 0 });
    expect(api.pending()).toContain("/api/v1/runs/run_1/logs?after=0&wait=15");
    await api.answer("/api/v1/runs/run_1/logs", {
      logs: [{ seq: 1, body: "new log" }],
      next: 1,
      observed: "running",
    });
    expect(live.detail.get()?.logs?.entries).toMatchObject([{ seq: 1, body: "new log" }]);
    expect(api.pending()).toContain("/api/v1/runs/run_1/logs?after=1&wait=0");
    void live.refreshDetail();
    await api.answer("/api/v1/tasks/task_1", detail("run_2"));
    expect(api.pending().some((p) => p.includes("run_1"))).toBe(false);
    await api.answer("/api/v1/runs/run_2/logs", { logs: [], next: 0 });
    expect(api.pending()).toContain("/api/v1/runs/run_2/logs?after=0&wait=15");
    void live.open("task_2");
    expect(api.pending().some((p) => p.includes("run_2"))).toBe(false);
    await api.answer("/api/v1/tasks/task_2", { ...detail("run_3"), task_id: "task_2" });
    await api.answer("/api/v1/runs/run_3/logs", { logs: [], next: 0 });
    live.close();
    expect(api.pending()).toEqual([]);
    void live.open("task_1");
    await api.answer("/api/v1/tasks/task_1", detail("run_4"));
    await api.answer("/api/v1/runs/run_4/logs", { logs: [], next: 0 });
    await live.stop();
    expect(api.pending()).toEqual([]);
  });

  test("tasks outside the snapshot are looked up once; unknown ones are remembered", async () => {
    const api = new FakeApi();
    const live = new LiveData(api);
    void live.lookUp("task_A");
    void live.lookUp("task_A");
    void live.lookUp("task_B");
    expect(api.pending()).toEqual(["/api/v1/tasks/task_A", "/api/v1/tasks/task_B"]);
    await api.answer("/api/v1/tasks/task_A", { task_id: "task_A", title: "done", runs: [] });
    await api.answer(
      "/api/v1/tasks/task_B",
      new ApiError(404, { code: "not_found", message: "no" }),
    );
    expect(live.lookups.get().get("task_A")?.title).toBe("done");
    expect(live.lookups.get().get("task_B")).toBeNull();
    void live.lookUp("task_B");
    expect(api.pending()).toEqual([]);
  });
});

test("cumulative assistant text keeps its identity through replay, pagination and a terminal state", async () => {
  const api = new FakeApi();
  const live = new LiveData(api);
  void live.reload();
  await api.answer("/api/v1/snapshot", snapshot);
  const partial: ConversationItem = {
    ...owner(60),
    author: "main_agent",
    reply_to: "msg_59",
    body: "正在写",
    assistant_state: "streaming",
  };
  delete partial.state;
  await api.answer("/api/v1/conversation", {
    items: [owner(59, "processing"), partial],
    more: true,
  });
  void live.loadEarlier();
  live.apply(messageEvent(61, { ...partial, body: "正在写一段完整的正文" }));
  await api.answer("/api/v1/conversation?before", { items: [owner(58, "replied")], more: false });
  live.apply(
    messageEvent(62, { ...partial, body: "正在写一段完整的正文", assistant_state: "interrupted" }),
  );
  live.apply(messageEvent(63, { ...owner(59, "interrupted") }, "conversation.message_state"));
  const state = live.conversation.get();
  expect(state.items.filter((it) => it.message_id === partial.message_id)).toEqual([
    { ...partial, body: "正在写一段完整的正文", assistant_state: "interrupted" },
  ]);
  expect(state.before).toBe(58);
  expect(processing(state.items)).toBeUndefined();
  await live.stop();
});

function assistant(
  seq: number,
  revision: number,
  body: string,
  state: ConversationItem["assistant_state"] = "streaming",
): ConversationItem {
  return {
    seq,
    message_id: `msg_${seq}`,
    kind: "message",
    author: "main_agent",
    reply_to: `msg_${seq - 1}`,
    body,
    assistant_state: state,
    assistant_revision: revision,
    created_at: "2026-10-22T10:00:01Z",
  };
}

test("a delayed held stream item cannot roll back a newer conversation snapshot", async () => {
  const api = new FakeApi();
  const live = new LiveData(api);
  void live.reload();
  await api.answer("/api/v1/snapshot", snapshot);
  live.apply(messageEvent(2, assistant(2, 2, "older draft")));
  const current = assistant(2, 3, "newer cumulative text");
  await api.answer("/api/v1/conversation", {
    items: [owner(1, "processing"), current],
    more: false,
  });
  expect(live.conversation.get().items).toEqual([owner(1, "processing"), current]);
  await live.stop();
});

test("a newer earlier-page item beats an older stream item already shown", async () => {
  const api = new FakeApi();
  const live = new LiveData(api);
  void live.reload();
  await api.answer("/api/v1/snapshot", snapshot);
  await api.answer("/api/v1/conversation", { items: [owner(59, "processing")], more: true });
  void live.loadEarlier();
  live.apply(messageEvent(10, assistant(58, 2, "older live text")));
  const current = assistant(58, 3, "newer saved text");
  await api.answer("/api/v1/conversation?before", {
    items: [owner(57, "replied"), current],
    more: false,
  });
  expect(live.conversation.get().items.find((it) => it.message_id === current.message_id)).toEqual(
    current,
  );
  expect(live.conversation.get().before).toBe(57);
  await live.stop();
});

test("a shorter final reply stays terminal after late streaming or legacy items", async () => {
  const api = new FakeApi();
  const live = new LiveData(api);
  void live.reload();
  await api.answer("/api/v1/snapshot", snapshot);
  await api.answer("/api/v1/conversation", {
    items: [owner(1, "processing"), assistant(2, 3, "a longer streamed draft")],
    more: false,
  });
  const final = assistant(2, 4, "done", "completed");
  live.apply(messageEvent(4, final));
  live.apply(messageEvent(5, assistant(2, 3, "a longer late draft")));
  const legacy = assistant(2, 0, "unversioned stale text");
  delete legacy.assistant_revision;
  live.apply(messageEvent(6, legacy));
  expect(live.conversation.get().items.filter((it) => it.message_id === final.message_id)).toEqual([
    final,
  ]);
  await live.stop();
});
