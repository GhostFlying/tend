import { describe, expect, test } from "vitest";
import { ApiError } from "../api/client.ts";
import type { ConversationItem, LedgerEvent } from "../api/generated/api.ts";
import { LiveData, type Reader } from "./data.ts";

/** A fake owner API whose answers the test releases one by one. */
class FakeApi implements Reader {
  calls: string[] = [];
  private waiting: { path: string; resolve: (v: unknown) => void; reject: (e: unknown) => void }[] =
    [];

  get<T>(path: string): Promise<T> {
    this.calls.push(path);
    return new Promise<T>((resolve, reject) => {
      this.waiting.push({ path, resolve: resolve as (v: unknown) => void, reject });
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
    await api.answer("/api/v1/conversation", { items: [owner(1)], more: true });
    expect(live.snapshot.get()).toEqual(snapshot);
    expect(live.conversation.get()).toMatchObject({ items: [owner(1)], more: true, loaded: true });
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
