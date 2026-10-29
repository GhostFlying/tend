import { afterEach, expect, test, vi } from "vitest";
import { NetworkError } from "../api/client.ts";
import type {
  APIResponse,
  ConversationHandoffView,
  Envelope,
  RequestView,
} from "../api/generated/api.ts";
import { ConversationHandoff } from "./handoff.ts";
import { LiveData } from "../live/data.ts";

const initial: ConversationHandoffView = {
  enabled: true,
  idle_minutes: 50,
  revision: 3,
  generation: 1,
  pending: false,
  preparing: false,
  last_activity_millis: 1000,
};
const flush = async () => {
  for (let i = 0; i < 20; i++) await Promise.resolve();
};
function fixture() {
  let view = structuredClone(initial);
  let response: APIResponse | Error = {
    operation: { request_id: "returned", status: "succeeded" },
  };
  let record: RequestView | null = null;
  const get = vi.fn(async (path: string) => {
    const url = new URL(path, "https://tend.invalid");
    if (
      url.pathname !== "/api/v1/conversation/handoff" ||
      (url.searchParams.has("conversation_id") &&
        url.searchParams.get("conversation_id") !== "main")
    )
      throw Error("unexpected dependency");
    return structuredClone(view);
  });
  const api = { get: async <T>(path: string) => (await get(path)) as T };
  const sender = {
    submit: vi.fn(async (_envelope: Envelope): Promise<APIResponse> => {
      if (response instanceof Error) throw response;
      return response;
    }),
    lookup: vi.fn(async (_id: string) => record),
  };
  const handoff = new ConversationHandoff(api);
  return {
    handoff,
    get,
    api,
    sender,
    setView: (v: ConversationHandoffView) => {
      view = v;
    },
    setResponse: (r: APIResponse | Error) => {
      response = r;
    },
    setRecord: (r: RequestView | null) => {
      record = r;
    },
  };
}
afterEach(() => vi.useRealTimers());

test("summary settings use their own revision without a model catalog and refresh stale values", async () => {
  const f = fixture();
  f.handoff.start();
  await flush();
  f.setResponse({
    operation: {
      request_id: "stale",
      status: "failed",
      error: { code: "stale_revision", message: "changed elsewhere" },
    },
  });
  f.setView({ ...initial, revision: 4, idle_minutes: 0 });
  await f.handoff.save(f.sender, { idle_minutes: 30 });
  expect(f.sender.submit.mock.calls[0]?.[0]).toMatchObject({
    operation: "conversation.set_handoff_settings",
    expected_control_revision: 3,
    payload: { idle_minutes: 30 },
  });
  expect(f.handoff.state.get().view).toMatchObject({ revision: 4, idle_minutes: 0 });
  expect(f.get.mock.calls.every(([path]) => path === "/api/v1/conversation/handoff")).toBe(true);
  await f.handoff.stop();
});

test("unknown manual preparation is only reconciled and keeps the original request identity", async () => {
  const f = fixture();
  f.handoff.start();
  await flush();
  f.setResponse(new NetworkError("lost"));
  await f.handoff.prepare(f.sender);
  const id = f.sender.submit.mock.calls[0]?.[0].request_id;
  expect(f.sender.submit.mock.calls[0]?.[0]).toMatchObject({
    operation: "conversation.handoff",
    payload: { prepare_only: true },
  });
  expect(f.handoff.state.get().pendingId).toBe(id);
  await f.handoff.save(f.sender, { idle_minutes: 0 });
  f.setRecord({ request_id: id!, operation: "conversation.handoff", status: "unknown" });
  await f.handoff.reconcile(f.sender);
  expect(f.handoff.state.get().pendingId).toBe(id);
  expect(f.sender.submit).toHaveBeenCalledTimes(1);
  f.setRecord({ request_id: id!, operation: "conversation.handoff", status: "succeeded" });
  await f.handoff.reconcile(f.sender);
  expect(f.handoff.state.get().pendingId).toBeUndefined();
  expect(f.sender.submit).toHaveBeenCalledTimes(1);
  await f.handoff.stop();
});

test("read failures retain shared values, retry with bounded delay, and stop cancels reads", async () => {
  vi.useFakeTimers();
  let fail = false;
  const get = vi.fn(async <T>(_path: string, signal?: AbortSignal): Promise<T> => {
    if (fail) throw Error("temporary");
    if (signal?.aborted) throw Error("aborted");
    return initial as T;
  });
  const h = new ConversationHandoff({
    get: async <T>(path: string, signal?: AbortSignal): Promise<T> =>
      (await get(path, signal)) as T,
  });
  h.start();
  await flush();
  fail = true;
  await h.refresh();
  expect(h.state.get().view).toEqual(initial);
  expect(h.state.get().readError).toBeDefined();
  fail = false;
  await vi.advanceTimersByTimeAsync(1000);
  expect(h.state.get().readError).toBeUndefined();
  await h.stop();
  const count = get.mock.calls.length;
  await vi.advanceTimersByTimeAsync(60_000);
  expect(get).toHaveBeenCalledTimes(count);
});

test("handoff settings and summary ledger events refresh across devices", async () => {
  const f = fixture();
  const live = new LiveData(f.api);
  live.handoff.start();
  await flush();
  f.setView({ ...initial, revision: 4, idle_minutes: 15 });
  live.apply({
    event_seq: 1,
    object_type: "conversation",
    object_id: "main",
    kind: "conversation.handoff_settings",
    created_at: new Date().toISOString(),
  });
  await flush();
  expect(live.handoff.state.get().view?.idle_minutes).toBe(15);
  await live.stop();
});

test("closing the connector aborts an in-flight handoff read", async () => {
  let aborted = false;
  const h = new ConversationHandoff({
    get: async <T>(_path: string, signal?: AbortSignal): Promise<T> =>
      new Promise((_resolve, reject) => {
        signal?.addEventListener(
          "abort",
          () => {
            aborted = true;
            reject(new Error("aborted"));
          },
          { once: true },
        );
      }),
  });
  h.start();
  await flush();
  await h.stop();
  expect(aborted).toBe(true);
});

const currentSummary: ConversationHandoffView = {
  ...initial,
  summary: {
    id: "summary_1",
    format_version: 3,
    ledger_bytes: 100,
    correction_bytes: 0,
    batches: 1,
    complete: true,
    source_thread_id: "thread_1",
    version: 2,
    max_seq: 10,
    created_at_millis: 1000,
    current: true,
  },
};

function contentEvent(seq: number, kind = "conversation.message") {
  return {
    event_seq: seq,
    object_type: "message",
    object_id: "msg_10",
    kind,
    created_at: "2026-10-23T10:00:00Z",
  };
}

test("new messages and in-place stream/tool/state changes immediately invalidate an open summary", async () => {
  vi.useFakeTimers();
  const f = fixture();
  f.setView(currentSummary);
  const live = new LiveData(f.api);
  live.handoff.start();
  await flush();
  for (const [i, kind] of [
    "conversation.message",
    "conversation.tool_use",
    "conversation.message_state",
    "conversation.message",
  ].entries()) {
    f.setView(currentSummary);
    await live.handoff.refresh();
    live.apply(contentEvent(i + 10, kind));
    expect(live.handoff.state.get().view?.summary?.current).toBe(false);
  }
  const reads = f.get.mock.calls.length;
  for (let seq = 20; seq < 120; seq++) live.apply(contentEvent(seq));
  expect(f.get).toHaveBeenCalledTimes(reads);
  f.setView({ ...currentSummary, summary: { ...currentSummary.summary!, current: false } });
  await vi.advanceTimersByTimeAsync(1000);
  expect(f.get).toHaveBeenCalledTimes(reads + 1);
  await live.stop();
  await vi.advanceTimersByTimeAsync(5000);
  expect(f.get).toHaveBeenCalledTimes(reads + 1);
});

test("an older in-flight GET cannot revive freshness and replayed events cannot invalidate a newer summary", async () => {
  vi.useFakeTimers();
  let delayed: ((view: ConversationHandoffView) => void) | undefined;
  let deferRead = false;
  const get = vi.fn(async () =>
    deferRead
      ? new Promise<ConversationHandoffView>((resolve) => {
          delayed = resolve;
        })
      : structuredClone(currentSummary),
  );
  const live = new LiveData({ get: async <T>() => (await get()) as T });
  live.handoff.start();
  await flush();
  deferRead = true;
  const pending = live.handoff.refresh();
  await flush();
  live.apply(contentEvent(20));
  expect(live.handoff.state.get().view?.summary?.current).toBe(false);
  deferRead = false;
  delayed!(structuredClone(currentSummary));
  await pending;
  expect(live.handoff.state.get().view?.summary?.current).toBe(false);
  // A newly produced summary can legitimately cover the changed content.
  live.apply({
    event_seq: 21,
    object_type: "conversation",
    object_id: "main",
    kind: "conversation.summary",
    created_at: "2026-10-23T10:01:00Z",
  });
  await flush();
  expect(live.handoff.state.get().view?.summary?.current).toBe(true);
  const reads = get.mock.calls.length;
  live.apply(contentEvent(20));
  expect(live.handoff.state.get().view?.summary?.current).toBe(true);
  await vi.advanceTimersByTimeAsync(2000);
  expect(get).toHaveBeenCalledTimes(reads);
  await live.stop();
});

test("queued preparation remains busy, polls through running, and stops at a failed maintenance result", async () => {
  vi.useFakeTimers();
  const f = fixture();
  f.handoff.start();
  await flush();
  await f.handoff.prepare(f.sender);
  const request = f.sender.submit.mock.calls[0]![0].request_id;
  expect(f.handoff.state.get().preparationRequestId).toBe(request);
  await f.handoff.prepare(f.sender);
  expect(f.sender.submit).toHaveBeenCalledTimes(1);
  f.setView({
    ...initial,
    pending: true,
    maintenance: {
      request_id: request,
      operation: "conversation.handoff",
      status: "pending",
      reason: "",
    },
  });
  await vi.advanceTimersByTimeAsync(1000);
  expect(f.handoff.state.get().preparationRequestId).toBeUndefined();
  expect(f.handoff.state.get().view?.pending).toBe(true);
  await f.handoff.prepare(f.sender);
  expect(f.sender.submit).toHaveBeenCalledTimes(1);
  f.setView({
    ...initial,
    pending: true,
    preparing: true,
    maintenance: {
      request_id: request,
      operation: "conversation.handoff",
      status: "sending",
      reason: "",
    },
  });
  await vi.advanceTimersByTimeAsync(1000);
  expect(f.handoff.state.get().view?.preparing).toBe(true);
  f.setView({
    ...initial,
    maintenance: {
      request_id: request,
      operation: "conversation.handoff",
      status: "failed",
      reason: "Summary timed out",
    },
  });
  await vi.advanceTimersByTimeAsync(1000);
  expect(f.handoff.state.get().view?.maintenance).toMatchObject({
    status: "failed",
    reason: "Summary timed out",
  });
  const reads = f.get.mock.calls.length;
  await vi.advanceTimersByTimeAsync(60_000);
  expect(f.get).toHaveBeenCalledTimes(reads);
  await f.handoff.stop();
});

test("a status read begun before acceptance cannot remove the local queued guard", async () => {
  vi.useFakeTimers();
  let delayed: ((v: ConversationHandoffView) => void) | undefined;
  let delay = false;
  let view = structuredClone(initial);
  const get = async <T>(): Promise<T> =>
    (delay
      ? await new Promise<ConversationHandoffView>((resolve) => {
          delayed = resolve;
        })
      : structuredClone(view)) as T;
  const h = new ConversationHandoff({ get });
  h.start();
  await flush();
  delay = true;
  const reading = h.refresh();
  await flush();
  let request = "";
  const sender = {
    submit: vi.fn(async (e: Envelope): Promise<APIResponse> => {
      request = e.request_id;
      return { operation: { request_id: request, status: "accepted" } };
    }),
    lookup: vi.fn(async () => null),
  };
  const preparing = h.prepare(sender);
  await flush();
  expect(h.state.get().preparationRequestId).toBe(request);
  const guarded: boolean[] = [];
  const off = h.state.subscribe(() => {
    const s = h.state.get();
    guarded.push(!!s.preparationRequestId || !!s.view?.pending);
  });
  view = {
    ...initial,
    pending: true,
    maintenance: {
      request_id: request,
      operation: "conversation.handoff",
      status: "pending",
      reason: "",
    },
  };
  delay = false;
  delayed!(initial);
  await Promise.all([reading, preparing]);
  off();
  expect(guarded.every(Boolean)).toBe(true);
  await h.prepare(sender);
  expect(sender.submit).toHaveBeenCalledTimes(1);
  await h.stop();
});

test("an accepted preparation can settle through a later device's completed maintenance record", async () => {
  const f = fixture();
  f.handoff.start();
  await flush();
  await f.handoff.prepare(f.sender);
  expect(f.handoff.state.get().preparationRequestId).toBeDefined();
  f.setView({
    ...initial,
    maintenance: {
      request_id: "another_device",
      operation: "conversation.handoff",
      status: "delivered",
      reason: "",
    },
  });
  await f.handoff.refresh();
  expect(f.handoff.state.get().preparationRequestId).toBeUndefined();
  await f.handoff.stop();
});
