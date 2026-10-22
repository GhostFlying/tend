import { afterEach, describe, expect, test, vi } from "vitest";
import { ApiError, NetworkError } from "../api/client.ts";
import type {
  APIResponse,
  ConversationSettingsView,
  Envelope,
  RequestView,
} from "../api/generated/api.ts";
import { LiveData } from "../live/data.ts";
import { ConversationSettings } from "./settings.ts";

const ready: ConversationSettingsView = {
  enabled: true,
  available: true,
  revision: 3,
  model: "model-a",
  effort: "high",
  models: [
    {
      id: "model-a",
      label: "Model A",
      supported_efforts: ["low", "high", "max"],
      default_effort: "high",
    },
    { id: "model-b", label: "Model B", supported_efforts: [], default_effort: "none" },
  ],
};
function fixture() {
  let view = structuredClone(ready);
  const get = vi.fn(async () => structuredClone(view));
  const submitted: Envelope[] = [];
  let answer: APIResponse | Error = {
    operation: { request_id: "req_returned", status: "succeeded" },
  };
  let record: RequestView | null = null;
  const sender = {
    submit: vi.fn(async (e: Envelope) => {
      submitted.push(e);
      if (answer instanceof Error) throw answer;
      return answer;
    }),
    lookup: vi.fn(async () => record),
  };
  const settings = new ConversationSettings({ get: async <T>() => (await get()) as T });
  return {
    settings,
    get,
    sender,
    submitted,
    setView: (v: ConversationSettingsView) => {
      view = v;
    },
    setAnswer: (a: APIResponse | Error) => {
      answer = a;
    },
    setRecord: (r: RequestView | null) => {
      record = r;
    },
  };
}
const flush = async () => {
  for (let i = 0; i < 15; i++) await Promise.resolve();
};
afterEach(() => vi.useRealTimers());

describe("shared conversation settings", () => {
  test("saves under the observed revision and reads back the server result", async () => {
    const f = fixture();
    f.settings.start();
    await flush();
    f.setView({ ...ready, revision: 4, model: "model-b", effort: "none" });
    await f.settings.save(f.sender, { model: "model-b", effort: "none" });
    expect(f.submitted).toHaveLength(1);
    expect(f.submitted[0]).toMatchObject({
      operation: "conversation.set_settings",
      target: { type: "conversation" },
      expected_control_revision: 3,
      payload: { model: "model-b", effort: "none" },
    });
    expect(f.settings.state.get().view).toMatchObject({ revision: 4, model: "model-b" });
    await f.settings.stop();
  });

  test("a stale revision reads the other device's value and keeps the conflict visible", async () => {
    const f = fixture();
    f.settings.start();
    await flush();
    f.setView({ ...ready, revision: 4, effort: "low" });
    f.setAnswer({
      operation: {
        request_id: "req_returned",
        status: "failed",
        error: { code: "stale_revision", message: "设置已在另一台设备更改" },
      },
    });
    await f.settings.save(f.sender, { model: "model-a", effort: "max" });
    expect(f.settings.state.get()).toMatchObject({
      view: { revision: 4, effort: "low" },
      error: "设置已在另一台设备更改",
      saving: false,
    });
    await f.settings.stop();
  });

  test("unknown receipts keep the original request for lookup and never resend", async () => {
    const f = fixture();
    f.settings.start();
    await flush();
    f.setAnswer(new NetworkError(new Error("lost")));
    f.sender.lookup.mockRejectedValueOnce(new Error("still offline"));
    await f.settings.save(f.sender, { model: "model-a", effort: "low" });
    const id = f.submitted[0]!.request_id;
    expect(f.settings.state.get().pendingId).toBe(id);
    await f.settings.save(f.sender, { model: "model-b", effort: "none" });
    f.setRecord({ request_id: id, operation: "conversation.set_settings", status: "unknown" });
    await f.settings.reconcile(f.sender);
    expect(f.sender.lookup).toHaveBeenLastCalledWith(id);
    expect(f.settings.state.get().pendingId).toBe(id);
    expect(f.submitted).toHaveLength(1);
    f.setRecord({ request_id: id, operation: "conversation.set_settings", status: "succeeded" });
    await f.settings.reconcile(f.sender);
    expect(f.settings.state.get().pendingId).toBeUndefined();
    expect(f.submitted).toHaveLength(1);
    await f.settings.stop();
  });

  test("unavailable catalog retains cached values, retries with capped backoff and stops", async () => {
    vi.useFakeTimers();
    const f = fixture();
    f.settings.start();
    await flush();
    f.setView({ ...ready, available: false, models: null, model: "", effort: "" });
    await f.settings.refresh();
    expect(f.settings.state.get().view).toMatchObject({
      model: "model-a",
      effort: "high",
      available: false,
      models: ready.models,
    });
    await f.settings.save(f.sender, { model: "model-b", effort: "none" });
    expect(f.submitted).toHaveLength(0);
    for (const delay of [1000, 2000, 4000, 8000, 16000, 30000, 30000]) {
      const n = f.get.mock.calls.length;
      await vi.advanceTimersByTimeAsync(delay - 1);
      expect(f.get).toHaveBeenCalledTimes(n);
      await vi.advanceTimersByTimeAsync(1);
      expect(f.get).toHaveBeenCalledTimes(n + 1);
    }
    f.setView(ready);
    await vi.advanceTimersByTimeAsync(30000);
    expect(f.settings.state.get().view?.available).toBe(true);
    const n = f.get.mock.calls.length;
    await vi.advanceTimersByTimeAsync(60000);
    expect(f.get).toHaveBeenCalledTimes(n);
    f.get.mockRejectedValueOnce(new ApiError(503, undefined));
    await f.settings.refresh();
    await f.settings.stop();
    await vi.advanceTimersByTimeAsync(60000);
    expect(f.get).toHaveBeenCalledTimes(n + 1);
  });

  test("stop aborts an in-flight read and prevents its retry", async () => {
    let signal: AbortSignal | undefined;
    const api = {
      get: <T>(_path: string, abort?: AbortSignal) =>
        new Promise<T>((_resolve, reject) => {
          signal = abort;
          abort?.addEventListener(
            "abort",
            () => reject(new DOMException("stopped", "AbortError")),
            { once: true },
          );
        }),
    };
    const settings = new ConversationSettings(api);
    settings.start();
    expect(signal?.aborted).toBe(false);
    await settings.stop();
    expect(signal?.aborted).toBe(true);
    expect(settings.state.get().readError).toBeUndefined();
  });

  test("settings ledger events refresh only settings and ignore APIResponse.current", async () => {
    const f = fixture();
    const live = new LiveData({ get: async <T>() => (await f.get()) as T });
    live.settings.start();
    await flush();
    f.setView({ ...ready, revision: 4, effort: "max" });
    live.apply({
      event_seq: 1,
      object_type: "conversation",
      object_id: "main",
      kind: "conversation.settings",
      created_at: "2026-10-22T00:00:00Z",
    });
    await flush();
    expect(live.settings.state.get().view?.effort).toBe("max");
    expect(f.get.mock.calls).toHaveLength(2);
    await live.stop();
  });
});
