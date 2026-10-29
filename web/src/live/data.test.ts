import { describe, expect, test, vi } from "vitest";
import { ApiError } from "../api/client.ts";
import type {
  WorkItemChoice,
  ConversationItem,
  LedgerEvent,
  TaskDetail,
  TaskSummary,
} from "../api/generated/api.ts";
import { processing } from "../conversation/model.ts";
import { taskLook } from "../tasks/status.ts";
import { queueReasonText } from "../tasks/execution.ts";
import { LiveData, summary, type Reader } from "./data.ts";

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
    expect(api.pending()).toEqual([
      "/api/v1/snapshot",
      "/api/v1/conversation?limit=50&projection=issues&conversation_id=main",
    ]);
    await api.answer("/api/v1/snapshot", snapshot);
    await api.answer("/api/v1/conversation", { items: [owner(1, "replied")], more: true });
    expect(live.snapshot.get()).toEqual(snapshot);
    expect(live.conversation.get()).toMatchObject({
      items: [owner(1, "replied")],
      more: true,
      loaded: true,
    });
  });

  test("choice invalidation retries transient failure without another SSE event", async () => {
    vi.useFakeTimers();
    const choice: WorkItemChoice = {
      choice_id: "choice-retry",
      work_item_id: "wi-retry",
      version: 1,
      status: "open",
      question: "Deploy?",
      options: ["yes"],
      source_id: "event-retry",
      message_id: "message-retry",
      created_at: 1,
      updated_at: 1,
    };
    let calls = 0;
    const api: Reader = {
      async get<T>(): Promise<T> {
        calls++;
        if (calls === 1) throw new ApiError(500, { code: "internal", message: "temporary" });
        return { ...choice, version: 2, status: "answered", answer: "Later" } as T;
      },
    };
    const live = new LiveData(api);
    try {
      live.apply(messageEvent(1, { ...owner(1, "replied"), choices: [choice] }));
      await live.choiceChanged(choice.choice_id);
      expect(live.choiceRead(choice.choice_id).state.get().error).toBe("temporary");
      expect(live.conversation.get().items[0]?.choices?.[0]?.status).toBe("open");
      await vi.advanceTimersByTimeAsync(999);
      expect(calls).toBe(1);
      await vi.advanceTimersByTimeAsync(1);
      expect(calls).toBe(2);
      expect(live.choiceRead(choice.choice_id).state.get().error).toBeUndefined();
      expect(live.conversation.get().items[0]?.choices?.[0]?.status).toBe("answered");
      await vi.advanceTimersByTimeAsync(60_000);
      expect(calls).toBe(2);
    } finally {
      await live.stop();
      vi.useRealTimers();
    }
  });

  test("repeated in-flight choice invalidations and consecutive failures leave one cancellable timer", async () => {
    vi.useFakeTimers();
    const failures: ((error: Error) => void)[] = [];
    let calls = 0;
    const api: Reader = {
      get<T>(): Promise<T> {
        calls++;
        return new Promise((_resolve, reject) => failures.push(reject));
      },
    };
    const live = new LiveData(api);
    try {
      const first = live.choiceChanged("choice-double-failure");
      void live.choiceChanged("choice-double-failure");
      void live.choiceChanged("choice-double-failure");
      expect(calls).toBe(1);
      failures.shift()!(new Error("first failure"));
      await vi.advanceTimersByTimeAsync(0);
      expect(calls).toBe(2);
      // The initial backoff cannot fire while the coalesced second read is pending.
      await vi.advanceTimersByTimeAsync(1000);
      failures.shift()!(new Error("second failure"));
      await first;
      expect(calls).toBe(2);
      expect(live.choiceRead("choice-double-failure").state.get().error).toBe("second failure");
      expect(vi.getTimerCount()).toBe(1);
      await live.stop();
      expect(vi.getTimerCount()).toBe(0);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(calls).toBe(2);
    } finally {
      await live.stop();
      vi.useRealTimers();
    }
  });

  test("choice reads share one retry timer and stop cancels pending retry and fetch", async () => {
    vi.useFakeTimers();
    let calls = 0;
    let signal: AbortSignal | undefined;
    const api: Reader = {
      get<T>(_path: string, current?: AbortSignal): Promise<T> {
        calls++;
        if (calls === 1) return Promise.reject(new Error("temporary"));
        signal = current;
        return new Promise((_resolve, reject) =>
          current?.addEventListener(
            "abort",
            () => reject(new DOMException("aborted", "AbortError")),
            { once: true },
          ),
        );
      },
    };
    const live = new LiveData(api);
    try {
      await live.choiceChanged("choice-stop");
      await vi.advanceTimersByTimeAsync(1000);
      expect(calls).toBe(2);
      void live.choiceChanged("choice-stop");
      void live.choiceChanged("choice-stop");
      expect(calls).toBe(2);
      await live.stop();
      expect(signal?.aborted).toBe(true);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(calls).toBe(2);
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      await live.stop();
      vi.useRealTimers();
    }
    // Stopping before the retry deadline must cancel that timer as well.
    vi.useFakeTimers();
    const failed = new LiveData({
      async get<T>(): Promise<T> {
        throw new Error("offline");
      },
    });
    try {
      await failed.choiceChanged("choice-scheduled");
      await failed.stop();
      expect(vi.getTimerCount()).toBe(0);
    } finally {
      await failed.stop();
      vi.useRealTimers();
    }
  });

  test.each([true, false])(
    "newer card revision survives an older in-flight latest page (already loaded: %s)",
    async (loaded) => {
      const api = new FakeApi();
      const live = new LiveData(api);
      const choice: WorkItemChoice = {
        choice_id: "choice-race",
        work_item_id: "wi-race",
        version: 1,
        status: "open",
        question: "Deploy?",
        options: ["yes"],
        source_id: "event-race",
        message_id: "message-race",
        created_at: 1,
        updated_at: 1,
      };
      const oldMessage = { ...owner(1, "replied"), choices: [choice] };
      if (loaded) live.apply(messageEvent(1, oldMessage));
      const reload = live.reload();
      live.apply({
        event_seq: 2,
        object_type: "work_item_choice",
        object_id: choice.choice_id,
        kind: "work_item_choice.answered",
        created_at: "2026-10-26T00:00:00Z",
      });
      await api.answer("/api/v1/work-item-choices/", {
        ...choice,
        version: 2,
        status: "answered",
        answer: "Later",
      });
      if (loaded) expect(live.conversation.get().items[0]?.choices?.[0]?.status).toBe("answered");
      // This response was issued before the answer, and still contains version 1.
      await api.answer("/api/v1/conversation", { items: [oldMessage], more: false });
      await api.answer("/api/v1/snapshot", snapshot);
      await reload;
      expect(live.conversation.get().items[0]?.choices?.[0]).toMatchObject({
        version: 2,
        status: "answered",
        answer: "Later",
      });
      expect(live.conversation.get().items).toHaveLength(1);
      live.stop();
    },
  );

  test("choice events refresh an older loaded card without replacing chat history", async () => {
    const api = new FakeApi();
    const live = new LiveData(api);
    const choice: WorkItemChoice = {
      choice_id: "choice-one",
      work_item_id: "wi-one",
      version: 1,
      status: "open",
      question: "Continue?",
      options: ["yes"],
      source_id: "event-one",
      message_id: "message-one",
      created_at: 1,
      updated_at: 1,
    };
    const message = { ...owner(1, "replied"), choices: [choice] };
    live.apply(messageEvent(1, message));
    live.apply(messageEvent(2, owner(2, "replied")));
    const changed = live.choiceChanged(choice.choice_id);
    await api.answer("/api/v1/work-item-choices/", {
      ...choice,
      version: 2,
      status: "withdrawn",
      reason: "Already deployed",
    });
    await changed;
    expect(live.conversation.get().items).toHaveLength(2);
    expect(live.conversation.get().items[0]?.choices?.[0]?.status).toBe("withdrawn");
    expect(api.calls.some((path) => path.startsWith("/api/v1/conversation"))).toBe(false);
    live.apply(messageEvent(3, message));
    expect(live.conversation.get().items[0]?.choices?.[0]?.version).toBe(2);
    live.stop();
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

  test("detail refresh keeps formal run status without fetching legacy logs", async () => {
    const api = new FakeApi();
    const live = new LiveData(api);
    void live.open("task_1");
    await api.answer("/api/v1/tasks/task_1", {
      task_id: "task_1",
      runs: [{ run_id: "run_1", observed: "running" }],
    });
    expect(api.pending()).toEqual([]);
    void live.refreshDetail();
    await api.answer("/api/v1/tasks/task_1", {
      task_id: "task_1",
      runs: [{ run_id: "run_1", observed: "failed" }],
    });
    expect(live.detail.get()?.task?.runs?.[0]?.observed).toBe("failed");
    expect(api.pending()).toEqual([]);
    await live.stop();
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
    expect(api.pending()).toContain(
      "/api/v1/conversation?before=51&limit=50&projection=issues&conversation_id=main",
    );
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
    expect(api.pending()).toContain(
      "/api/v1/conversation?before=2&limit=50&projection=issues&conversation_id=main",
    );
    live.apply(messageEvent(103, owner(1, "processing"), "conversation.message_state"));
    await api.answer("/api/v1/conversation", { items: [owner(1)], more: false });
    expect(processing(live.conversation.get().items)?.message_id).toBe("msg_1");
    await live.stop();
  });

  test("switching, closing and stopping details abort their pending read", async () => {
    const api = new FakeApi();
    const live = new LiveData(api);
    void live.open("task_1");
    void live.open("task_2");
    await vi.waitFor(() => expect(api.pending()).toEqual(["/api/v1/tasks/task_2"]));
    await api.answer("/api/v1/tasks/task_2", {
      task_id: "task_2",
      runs: [{ run_id: "run_2", observed: "running" }],
    });
    expect(api.pending()).toEqual([]);
    void live.refreshDetail();
    live.close();
    expect(api.pending()).toEqual([]);
    void live.open("task_3");
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

const referenceTask: TaskDetail = {
  task_id: "task_reference",
  title: "Outside the snapshot",
  project: "sample",
  lifecycle: "running",
  control_mode: "managed",
  control_revision: 2,
  spec_version: 1,
  rating: "p2",
  block_reason: "none",
  workspace: "sample",
  next_implement_generation: 2,
  primary_work_item_id: "wi_primary",
  primary_issue_id: "canonical_primary",
  primary_issue_title: "Current canonical Issue",
  execution: {
    state: "queued",
    queue_reason: { code: "workspace_busy", message: "occupied", task_id: "task_owner" },
  },
  actions: [],
  runs: [],
  artifacts: [],
  acceptances: [],
  decisions: [],
  spec: { goal: "Inspect" },
};
function referenceEvent(object_type: string, object_id = "other"): LedgerEvent {
  return {
    event_seq: 2,
    object_type,
    object_id,
    kind: `${object_type}.changed`,
    created_at: "2026-10-25T00:00:00Z",
  };
}

test("detail summaries retain every summary fact used by cached task references", () => {
  const expected: TaskSummary = {
    task_id: referenceTask.task_id,
    title: referenceTask.title,
    project: referenceTask.project,
    lifecycle: referenceTask.lifecycle,
    control_mode: referenceTask.control_mode,
    control_revision: referenceTask.control_revision,
    spec_version: referenceTask.spec_version,
    rating: referenceTask.rating,
    block_reason: referenceTask.block_reason,
    workspace: referenceTask.workspace,
    next_implement_generation: referenceTask.next_implement_generation,
    primary_work_item_id: "wi_primary",
    primary_issue_id: "canonical_primary",
    primary_issue_title: "Current canonical Issue",
    execution: referenceTask.execution!,
  };
  expect(summary(referenceTask)).toEqual(expected);
  expect(taskLook(summary(referenceTask)).label).toBe("排队中");
  expect(queueReasonText(summary(referenceTask))).toBe("等待工作区释放");
  expect(summary(referenceTask)).not.toHaveProperty("spec");
  expect(summary(referenceTask)).not.toHaveProperty("runs");
});

test("TaskRef's lookup and subsequent detail reads keep authoritative execution facts", async () => {
  const api = new FakeApi();
  const live = new LiveData(api);
  try {
    const looked = live.lookUp(referenceTask.task_id);
    await api.answer("/api/v1/tasks/task_reference", referenceTask);
    await looked;
    expect(live.lookups.get().get(referenceTask.task_id)?.primary_work_item_id).toBe("wi_primary");
    expect(live.lookups.get().get(referenceTask.task_id)?.primary_issue_id).toBe(
      "canonical_primary",
    );
    expect(live.lookups.get().get(referenceTask.task_id)?.primary_issue_title).toBe(
      "Current canonical Issue",
    );
    expect(taskLook(live.lookups.get().get(referenceTask.task_id)!).label).toBe("排队中");
    const opened = live.open(referenceTask.task_id);
    await api.answer("/api/v1/tasks/task_reference", {
      ...referenceTask,
      execution: { state: "accepting", role: "accept" },
    });
    await opened;
    expect(taskLook(live.lookups.get().get(referenceTask.task_id)!).label).toBe("验收中");
    expect(live.lookups.get().get(referenceTask.task_id)?.execution).toEqual({
      state: "accepting",
      role: "accept",
    });
  } finally {
    await live.stop();
  }
});

test("outside-snapshot references refresh on other workspace, host and run facts", async () => {
  const api = new FakeApi();
  const live = new LiveData(api);
  try {
    const first = live.lookUp(referenceTask.task_id);
    // A host event while the original response is pending must schedule a newer read.
    live.apply(referenceEvent("host"));
    live.apply(referenceEvent("workspace"));
    expect(api.calls.filter((p) => p.includes("task_reference"))).toHaveLength(1);
    await api.answer("/api/v1/tasks/task_reference", referenceTask);
    await first;
    expect(api.calls.filter((p) => p.includes("task_reference"))).toHaveLength(2);
    await api.answer("/api/v1/tasks/task_reference", {
      ...referenceTask,
      execution: { state: "running", role: "implement" },
    });
    expect(taskLook(live.lookups.get().get(referenceTask.task_id)!).label).toBe("执行中");
    live.apply(referenceEvent("run"));
    await api.answer("/api/v1/tasks/task_reference", {
      ...referenceTask,
      execution: {
        state: "queued",
        queue_reason: { code: "host_capacity", message: "capacity exhausted" },
      },
    });
    expect(queueReasonText(live.lookups.get().get(referenceTask.task_id)!)).toBe(
      "等待主机空闲槽位",
    );
    live.apply(referenceEvent("task", referenceTask.task_id));
    await api.answer("/api/v1/tasks/task_reference", {
      ...referenceTask,
      lifecycle: "completed",
      execution: { state: "completed" },
    });
    const completedCalls = api.calls.filter((p) => p.includes("task_reference")).length;
    live.apply(referenceEvent("host"));
    expect(api.calls.filter((p) => p.includes("task_reference"))).toHaveLength(completedCalls);
    live.apply(referenceEvent("issue", "canonical_primary"));
    await api.answer("/api/v1/tasks/task_reference", {
      ...referenceTask,
      lifecycle: "completed",
      execution: { state: "completed" },
      primary_issue_title: "Renamed current Issue",
    });
    expect(live.lookups.get().get(referenceTask.task_id)?.primary_issue_title).toBe(
      "Renamed current Issue",
    );
  } finally {
    await live.stop();
  }
});

test("choice updates refresh retained transcript cards and resist older pages", async () => {
  const choice = {
    choice_id: "choice",
    work_item_id: "work",
    version: 1,
    status: "open",
    question: "Continue?",
    source_id: "source",
    message_id: "notice",
    created_at: 1,
    updated_at: 1,
  } as WorkItemChoice;
  const trigger: ConversationItem = {
    ...owner(1),
    message_id: "notice",
    author: "system",
    choices: [choice],
  };
  const api: Reader = {
    async get<T>(path: string): Promise<T> {
      if (path.includes("work-item-choices"))
        return { ...choice, version: 2, status: "withdrawn" } as T;
      if (path.includes("transcript"))
        return {
          blocks: [{ position: 1, notice_only: true, trigger, items: [], more: false }],
          more: false,
          event_seq: 2,
        } as T;
      return { enabled: true, items: [], total: 0, event_seq: 2 } as T;
    },
  };
  const live = new LiveData(api);
  live.queueView.start();
  await live.queueView.refresh();
  await live.choiceChanged("choice");
  expect(live.queueView.state.get().blocks[0]?.trigger.choices?.[0]?.status).toBe("withdrawn");
  await live.queueView.refresh();
  expect(live.queueView.state.get().blocks[0]?.trigger.choices?.[0]?.status).toBe("withdrawn");
  await live.stop();
});

test("turn state events refresh an open followup projection", async () => {
  const api = new FakeApi();
  const live = new LiveData(api);
  const path = "/api/v1/issues/issue-example/followups";
  try {
    const first = live.issues.openFollowups("issue-example");
    await api.answer(path, { enabled: true, items: [], latest: { rule: { status: "starting" } } });
    await first;
    live.apply(
      messageEvent(
        10,
        { ...owner(2, "processing"), author: "system" },
        "conversation.message_state",
      ),
    );
    await vi.waitFor(() => expect(api.pending()).toContain(path));
    await api.answer(path, {
      enabled: true,
      items: [],
      latest: { rule: { status: "processing" } },
    });
    expect(live.issues.followups.state.get().value?.latest?.rule?.status).toBe("processing");
  } finally {
    live.stop();
  }
});

test("canonical global reads invalidate before the selected-chat filter and stop without extra requests", async () => {
  const api = new FakeApi(),
    live = new LiveData(api);
  expect(api.calls).toEqual([]);
  const issue = live.issues.open("issue-one");
  const inbox = live.attention.openInbox();
  const next = live.attention.openNext();
  const chats = live.directChats.openList();
  await api.answer("/api/v1/issues/issue-one", { issue_id: "issue-one", revision: 1 });
  await api.answer("/api/v1/inbox", { items: [], more: false });
  await api.answer("/api/v1/next-actions", { items: [], deadlines: [] });
  await api.answer("/api/v1/agent-conversations", { items: [] });
  await Promise.all([issue, inbox, next, chats]);
  live.apply(
    messageEvent(20, { ...owner(2), conversation_id: "issue:other" }, "conversation.message"),
  );
  expect(api.pending()).toEqual([
    "/api/v1/issues/issue-one",
    "/api/v1/inbox",
    "/api/v1/next-actions",
    "/api/v1/agent-conversations?search=&status=&projection=issues",
  ]);
  expect(live.conversation.get().items).toEqual([]);
  await api.answer("/api/v1/issues/issue-one", { issue_id: "issue-one", revision: 2 });
  await api.answer("/api/v1/inbox", { items: [], more: false });
  await api.answer("/api/v1/next-actions", {
    items: [
      { entry_id: "server-order-first", rank: 9 },
      { entry_id: "server-order-second", rank: 1 },
    ],
    deadlines: [],
  });
  await api.answer("/api/v1/agent-conversations", {
    items: [{ conversation_id: "issue:other", status: "queued" }],
  });
  expect(live.issues.detail.state.get().value?.revision).toBe(2);
  expect(live.attention.next.state.get().value?.items?.map((entry) => entry.entry_id)).toEqual([
    "server-order-first",
    "server-order-second",
  ]);
  // Conversation status depends on native delivery as well as transcript items.
  live.apply({
    event_seq: 21,
    object_type: "outbox",
    object_id: "delivery",
    kind: "delivery.unknown",
    created_at: "2026-10-29T00:00:00Z",
  });
  expect(api.pending()).toEqual(["/api/v1/agent-conversations?search=&status=&projection=issues"]);
  await live.stop();
  expect(api.pending()).toEqual([]);
  const count = api.calls.length;
  live.apply(messageEvent(22, { ...owner(3), conversation_id: "issue:other" }));
  expect(api.calls).toHaveLength(count);
});

test("opened Issue follows run recovery and resource facts without opening hidden Issue or attention reads", async () => {
  const api = new FakeApi(),
    live = new LiveData(api);
  try {
    // ExpireLeases emits only this run event, not a companion task update.
    live.apply({
      event_seq: 30,
      object_type: "run",
      object_id: "run-one",
      kind: "run.unknown",
      created_at: "2026-10-29T00:00:00Z",
    });
    expect(api.calls.filter((path) => path.startsWith("/api/v1/issues"))).toEqual([]);
    await api.answer("/api/v1/snapshot", snapshot);
    const opened = live.issues.open("issue-one");
    await api.answer("/api/v1/issues/issue-one", {
      issue_id: "issue-one",
      tasks: [{ task_id: "task-one", execution: { state: "running" } }],
    });
    await opened;
    const changes = [
      { type: "run", kind: "run.unknown", execution: { state: "unknown" } },
      {
        type: "run",
        kind: "run.recovery",
        execution: { state: "recovering", recovery: { phase: "restarting" } },
      },
      {
        type: "host",
        kind: "host.maintenance_changed",
        execution: { state: "queued", queue_reason: { code: "host_unavailable" } },
      },
      {
        type: "workspace",
        kind: "workspace.released",
        execution: { state: "queued", queue_reason: { code: "ready" } },
      },
    ];
    for (const [index, changed] of changes.entries()) {
      live.apply({
        event_seq: 31 + index,
        object_type: changed.type,
        object_id: "resource-one",
        kind: changed.kind,
        created_at: "2026-10-29T00:00:00Z",
      });
      expect(api.pending()).toContain("/api/v1/issues/issue-one");
      await api.answer("/api/v1/issues/issue-one", {
        issue_id: "issue-one",
        tasks: [{ task_id: "task-one", execution: changed.execution }],
      });
      await api.answer("/api/v1/snapshot", snapshot);
      expect(live.issues.detail.state.get().value?.tasks?.[0]?.execution).toEqual(
        changed.execution,
      );
    }
    expect(
      api.calls.some(
        (path) => path.startsWith("/api/v1/inbox") || path.startsWith("/api/v1/next-actions"),
      ),
    ).toBe(false);
  } finally {
    await live.stop();
  }
});

test("public agent catalogs stay lazy and follow existing fact and reload lifecycle", async () => {
  const api = new FakeApi(),
    live = new LiveData(api);
  try {
    expect(api.calls).toEqual([]);
    const categories = live.agents.openCategories("all", "last category");
    const runner = live.agents.openRunner("runner-one");
    const hosted = live.agents.openHosted();
    await api.answer("/api/v1/categories", { items: [], more: true, next: "next-page" });
    await api.answer("/api/v1/hosts/runner-one/agents", { host_id: "runner-one", online: false });
    await api.answer("/api/v1/hosted-agents", { configuration_revision: 1 });
    await Promise.all([categories, runner, hosted]);
    expect(live.agents.categories.state.get().value?.next).toBe("next-page");
    expect(new URL(api.calls[0]!, "https://tend.invalid").searchParams.get("after")).toBe(
      "last category",
    );
    live.apply({
      event_seq: 40,
      object_type: "host",
      object_id: "runner-one",
      kind: "host.agent_inventory_registered",
      created_at: "2026-10-29T00:00:00Z",
    });
    await api.answer("/api/v1/categories", { items: [], more: false });
    await api.answer("/api/v1/hosts/runner-one/agents", { host_id: "runner-one", online: true });
    await api.answer("/api/v1/hosted-agents", { configuration_revision: 2 });
    await api.answer("/api/v1/snapshot", snapshot);
    expect(live.agents.runner.state.get().value?.online).toBe(true);
    const reloaded = live.reload();
    await api.answer("/api/v1/categories", { items: [], more: false });
    await api.answer("/api/v1/hosts/runner-one/agents", { host_id: "runner-one", online: false });
    await api.answer("/api/v1/hosted-agents", { configuration_revision: 2 });
    await api.answer("/api/v1/snapshot", snapshot);
    await api.answer("/api/v1/conversation", { items: [], more: false });
    await reloaded;
    expect(live.agents.runner.state.get().value?.online).toBe(false);
    live.agents.refresh();
    expect(api.pending().some((path) => path.includes("/agents"))).toBe(true);
    await live.stop();
    expect(api.pending()).toEqual([]);
  } finally {
    await live.stop();
  }
});

test("raw stream content preserves canonical links until the scoped HTTP association refresh", async () => {
  const api = new FakeApi(),
    live = new LiveData(api, "issue:one");
  const association = {
    message_id: "msg_1",
    revision: 3,
    content_revision: 1,
    disposition: "linked",
    facts_reviewed: true,
    owner_locked: true,
    items: [{ issue_id: "issue-one", title: "Current Issue", source: "owner", owner_locked: true }],
  };
  const reload = live.reload();
  await api.answer("/api/v1/snapshot", snapshot);
  await api.answer("/api/v1/conversation", {
    items: [{ ...owner(1, "processing"), conversation_id: "issue:one", associations: association }],
    more: false,
  });
  await reload;
  const raw = {
    ...owner(1, "replied"),
    conversation_id: "issue:one",
    body: "actual streamed body",
    associations: {
      ...association,
      items: [
        { work_item_id: "legacy-only", title: "Historical", source: "owner", owner_locked: true },
      ],
    },
  };
  live.apply(messageEvent(2, raw, "conversation.message_state"));
  expect(live.conversation.get().items[0]?.body).toBe("actual streamed body");
  expect(live.conversation.get().items[0]?.associations).toEqual(association);
  live.apply(messageEvent(3, { ...raw, conversation_id: "issue:other", body: "wrong chat" }));
  expect(live.conversation.get().items[0]?.body).toBe("actual streamed body");
  live.apply({
    event_seq: 4,
    object_type: "message",
    object_id: "msg_1",
    kind: "conversation.associations_changed",
    created_at: "2026-10-29T00:00:00Z",
  });
  expect(
    api.calls.some((path) => {
      const url = new URL(path, "https://test.invalid");
      return (
        url.pathname === "/api/v1/conversation" &&
        url.searchParams.get("projection") === "issues" &&
        url.searchParams.get("conversation_id") === "issue:one"
      );
    }),
  ).toBe(true);
  await api.answer("/api/v1/conversation", {
    items: [
      {
        ...owner(1, "replied"),
        conversation_id: "issue:one",
        associations: { ...association, revision: 4, disposition: "unassigned", items: [] },
      },
    ],
    more: false,
  });
  expect(live.conversation.get().items[0]?.associations).toMatchObject({
    revision: 4,
    disposition: "unassigned",
    items: [],
  });
  await live.stop();
});

test("followup changes refresh opened canonical rule and history reads without opening hidden pages", async () => {
  const api = new FakeApi(),
    live = new LiveData(api);
  try {
    const opened = live.issues.openFollowups("issue-one");
    await api.answer("/api/v1/issues/issue-one/followups", {
      enabled: true,
      issue_id: "issue-one",
      items: [{ rule_id: "rule-one", revision: 1 }],
      event_seq: 1,
    });
    await opened;
    const history = live.issues.openFollowupHistory("issue-one", "rule-one");
    await api.answer("/api/v1/issues/issue-one/followups/rule-one/history", {
      rule: { rule_id: "rule-one", issue_id: "issue-one", revision: 1 },
      occurrences: [],
      revisions: [],
      more: false,
    });
    await history;
    api.calls.length = 0;
    live.apply({
      event_seq: 2,
      object_type: "followup",
      object_id: "rule-one",
      kind: "followup.changed",
      created_at: "2026-10-29T00:00:00Z",
    });
    expect(api.pending()).toContain("/api/v1/issues/issue-one/followups");
    expect(api.pending()).toContain("/api/v1/issues/issue-one/followups/rule-one/history");
    expect(
      api.calls.some(
        (path) => path.startsWith("/api/v1/inbox") || path.startsWith("/api/v1/next-actions"),
      ),
    ).toBe(false);
    await api.answer("/api/v1/issues/issue-one/followups", {
      enabled: true,
      issue_id: "issue-one",
      items: [{ rule_id: "rule-one", revision: 2 }],
      event_seq: 2,
    });
    await api.answer("/api/v1/issues/issue-one/followups/rule-one/history", {
      rule: { rule_id: "rule-one", issue_id: "issue-one", revision: 2 },
      occurrences: [{ occurrence_id: "original-occurrence" }],
      revisions: [],
      more: false,
    });
    expect(live.issues.followups.state.get().value?.items?.[0]?.revision).toBe(2);
    expect(live.issues.followupHistory.state.get().value?.occurrences?.[0]?.occurrence_id).toBe(
      "original-occurrence",
    );
  } finally {
    await live.stop();
  }
});
