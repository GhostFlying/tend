import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { ApiClient, ApiError, NetworkError } from "../api/client.ts";
import type { LedgerEvent } from "../api/generated/api.ts";
import { EventStream } from "./eventStream.ts";

interface Open {
  after: number | undefined;
  signal: AbortSignal;
  push(text: string): void;
  end(): void;
}

/** A fake tend whose streams the test writes to. */
class FakeTend {
  opens: Open[] = [];
  traceID: string | undefined = "trc_00000000000000000000000001";
  /** When each open was tried, refused ones included. */
  attempts: { at: number; after: number | undefined }[] = [];
  /** Answers an open instead of a stream when it returns an error. */
  refuse: (after: number | undefined) => Error | undefined = () => undefined;

  open = async (after: number | undefined, signal: AbortSignal): Promise<Response> => {
    this.attempts.push({ at: Date.now(), after });
    const refusal = this.refuse(after);
    if (refusal) {
      throw refusal;
    }
    let ctrl!: ReadableStreamDefaultController<Uint8Array>;
    let closed = false;
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        ctrl = c;
      },
    });
    signal.addEventListener("abort", () => {
      if (!closed) {
        closed = true;
        ctrl.error(new DOMException("aborted", "AbortError"));
      }
    });
    const encoder = new TextEncoder();
    this.opens.push({
      after,
      signal,
      push: (text) => ctrl.enqueue(encoder.encode(text)),
      end: () => {
        closed = true;
        ctrl.close();
      },
    });
    const headers = new Headers({ "Content-Type": "text/event-stream" });
    if (this.traceID) headers.set("X-Tend-Trace-ID", this.traceID);
    return new Response(body, { headers });
  };

  get last(): Open {
    return this.opens[this.opens.length - 1]!;
  }
}

const start = (seq: number) => `event: start\ndata: {"event_seq":${seq}}\n\n`;

function ledger(seq: number, objectType = "task"): string {
  const ev: LedgerEvent = {
    event_seq: seq,
    object_type: objectType,
    object_id: "task_1",
    kind: "task.created",
    created_at: "2026-10-22T00:00:00Z",
  };
  return `id: ${seq}\nevent: ledger\ndata: ${JSON.stringify(ev)}\n\n`;
}

let tend: FakeTend;
let starts: [number, boolean][];
let events: number[];
let lost: [string, string | undefined][];
let resets: [string | undefined, string | undefined][];
let stream: EventStream;

const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

beforeEach(() => {
  vi.useFakeTimers();
  tend = new FakeTend();
  starts = [];
  events = [];
  lost = [];
  resets = [];
  stream = new EventStream({
    open: tend.open,
    onStart: (seq, fresh) => starts.push([seq, fresh]),
    onEvent: (ev) => events.push(ev.event_seq),
    onLost: (reason, traceID) => lost.push([reason, traceID]),
    onReset: (reason, traceID) => resets.push([reason, traceID]),
    silenceMs: 45_000,
  });
});

afterEach(async () => {
  await stream.stop();
  vi.useRealTimers();
});

describe("EventStream", () => {
  test("an expired refresh position rebuilds views from a fresh start", async () => {
    stream.start();
    await tick();
    tend.last.push(start(5) + ledger(6));
    await tick();
    tend.last.push('event: reset\ndata: {"event_seq":10}\n\n');
    await tick();
    expect(tend.opens.map((o) => o.after)).toEqual([undefined, undefined]);
    expect(resets).toEqual([["服务端要求重新同步游标", tend.traceID]]);
    tend.last.push(start(10));
    await tick();
    expect(starts).toEqual([
      [5, true],
      [10, true],
    ]);
    expect(events).toEqual([6]);
    expect(stream.cursor).toBe(10);
  });
  test("a failed event handler recovers from source without skipping the event on resume", async () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => {});
    stream = new EventStream({
      open: tend.open,
      onStart: (seq, fresh) => starts.push([seq, fresh]),
      onEvent: () => {
        throw new Error("view update failed");
      },
    });
    try {
      stream.start();
      await tick();
      tend.last.push(start(5) + ledger(6));
      await tick();
      expect(tend.opens.map((o) => o.after)).toEqual([undefined, undefined]);
      tend.last.push(start(6));
      await tick();
      expect(starts).toEqual([
        [5, true],
        [6, true],
      ]);
    } finally {
      log.mockRestore();
    }
  });

  test("a first connection has no cursor and starts fresh", async () => {
    stream.start();
    await tick();
    expect(tend.opens.map((o) => o.after)).toEqual([undefined]);
    tend.last.push(start(5));
    tend.last.push(ledger(6) + ledger(7));
    await tick();
    expect(starts).toEqual([[5, true]]);
    expect(events).toEqual([6, 7]);
    expect(stream.cursor).toBe(7);
    expect(stream.state.get()).toBe("live");
  });

  test("resumes after the last event once the connection is lost", async () => {
    stream.start();
    await tick();
    tend.last.push(start(5) + ledger(6));
    await tick();
    tend.last.end();
    await tick();
    expect(stream.state.get()).toBe("waiting");
    expect(lost).toHaveLength(1);
    await tick(999);
    expect(tend.opens).toHaveLength(1);
    await tick(1);
    expect(tend.opens.map((o) => o.after)).toEqual([undefined, 6]);
    tend.last.push(start(6) + ledger(6) + ledger(7));
    await tick();
    // Resuming is not a fresh start, and nothing is delivered twice.
    expect(starts).toEqual([
      [5, true],
      [6, false],
    ]);
    expect(events).toEqual([6, 7]);
  });

  test("heartbeats keep a quiet stream alive", async () => {
    stream.start();
    await tick();
    tend.last.push(start(1));
    for (let i = 0; i < 4; i++) {
      await tick(15_000);
      tend.last.push(": heartbeat\n\n");
    }
    await tick(40_000);
    expect(tend.opens).toHaveLength(1);
    expect(stream.state.get()).toBe("live");
  });

  test("a missed heartbeat window counts as a lost connection", async () => {
    stream.start();
    await tick();
    tend.last.push(start(3));
    await tick(44_999);
    expect(tend.last.signal.aborted).toBe(false);
    await tick(1);
    expect(tend.opens[0]!.signal.aborted).toBe(true);
    expect(lost).toEqual([["事件流心跳超时，期间未收到数据", tend.traceID]]);
    await tick(1_000);
    expect(tend.opens.map((o) => o.after)).toEqual([undefined, 3]);
  });

  test("a stalled HTTP error body keeps the trace received with its headers", async () => {
    const traceID = "trc_00000000000000000000000002";
    const client = new ApiClient({
      storage: {
        getItem: (key) => (key === "tend.token" ? "fixture-token" : null),
        setItem: () => {},
        removeItem: () => {},
      },
      fetch: async (_input, options) =>
        new Response(
          new ReadableStream({
            start(controller) {
              options?.signal?.addEventListener("abort", () => {
                controller.error(new DOMException("aborted", "AbortError"));
              });
            },
          }),
          { status: 500, headers: { "X-Tend-Trace-ID": traceID } },
        ),
    });
    stream = new EventStream({
      open: (_after, signal) => client.openStream("/api/v1/events", signal),
      onStart: () => {},
      onEvent: () => {},
      onLost: (reason, id) => lost.push([reason, id]),
      silenceMs: 45_000,
    });
    stream.start();
    await tick(44_999);
    expect(lost).toEqual([]);
    await tick(1);
    expect(lost).toEqual([["读取事件流错误响应超时（HTTP 500）", traceID]]);
  });

  test("a timeout before response headers has no confirmed trace", async () => {
    stream = new EventStream({
      open: (_after, signal) =>
        new Promise((_resolve, reject) => {
          signal.addEventListener("abort", () => reject(new NetworkError(signal.reason)));
        }),
      onStart: () => {},
      onEvent: () => {},
      onLost: (reason, traceID) => lost.push([reason, traceID]),
      silenceMs: 45_000,
    });
    stream.start();
    await tick(45_000);
    expect(lost).toEqual([["等待事件流响应超时", undefined]]);
  });

  test("a failed retry before receiving headers does not reuse the prior trace", async () => {
    stream.start();
    await tick();
    tend.last.push(start(3));
    await tick();
    tend.last.end();
    tend.refuse = () => new NetworkError(new Error("no response"));
    await tick(1_000);
    expect(lost).toEqual([
      ["事件流已结束", tend.traceID],
      ["未能打开事件流", undefined],
    ]);
  });

  test.each([400, 409])("a refused cursor (%s) reconnects at once without one", async (status) => {
    stream.start();
    await tick();
    tend.last.push(start(9));
    await tick();
    tend.refuse = (after) =>
      after === undefined
        ? undefined
        : new ApiError(status, {
            code: status === 409 ? "stale_revision" : "invalid_request",
            message: "refresh cursor refused",
          });
    tend.last.end();
    const t0 = Date.now();
    await tick(1_000);
    await tick();
    // The refusal is answered at once, without a pause.
    expect(tend.attempts.map((a) => [a.at - t0, a.after])).toEqual([
      [0, undefined],
      [1_000, 9],
      [1_000, undefined],
    ]);
    tend.last.push(start(2));
    await tick();
    expect(starts).toEqual([
      [9, true],
      [2, true],
    ]);
  });

  test("a rate-limited reconnect retries the same cursor after backoff", async () => {
    stream.start();
    await tick();
    tend.last.push(start(5) + ledger(6));
    await tick();
    tend.last.end();
    tend.refuse = () => new ApiError(429, { code: "internal", message: "rate limited" });
    await tick(1_000);
    expect(tend.attempts.map((a) => a.after)).toEqual([undefined, 6]);

    tend.refuse = () => undefined;
    await tick(1_999);
    expect(tend.opens).toHaveLength(1);
    await tick(1);
    expect(tend.last.after).toBe(6);
    tend.last.push(start(6) + ledger(6) + ledger(7));
    await tick();
    expect(starts).toEqual([
      [5, true],
      [6, false],
    ]);
    expect(events).toEqual([6, 7]);
  });

  test("the pause grows while tend cannot be reached and resets after a start", async () => {
    tend.refuse = () => new NetworkError(new Error("down"));
    const t0 = Date.now();
    stream.start();
    await tick(16_000);
    // Attempts at 0, 1, 3, 7 and 15 seconds.
    expect(tend.attempts.map((a) => a.at - t0)).toEqual([0, 1_000, 3_000, 7_000, 15_000]);

    tend.refuse = () => undefined;
    await tick(16_000);
    tend.last.push(start(1));
    await tick();
    tend.last.end();
    await tick(999);
    const before = tend.opens.length;
    await tick(1);
    expect(tend.opens.length).toBe(before + 1);
  });

  test("a 401 the client could not fix is retried, not treated as a bad cursor", async () => {
    stream.start();
    await tick();
    tend.last.push(start(4));
    await tick();
    tend.refuse = () => new ApiError(401, { code: "unauthenticated", message: "no" });
    tend.last.end();
    await tick(1_000);
    tend.refuse = () => undefined;
    await tick(2_000);
    expect(tend.last.after).toBe(4);
  });

  test("wake cuts a pause short", async () => {
    stream.start();
    await tick();
    tend.last.push(start(1));
    await tick();
    tend.last.end();
    await tick();
    stream.wake();
    await tick();
    expect(tend.opens).toHaveLength(2);
  });

  test("stop closes the stream", async () => {
    stream.start();
    await tick();
    tend.last.push(start(1));
    await tick();
    await stream.stop();
    expect(tend.last.signal.aborted).toBe(true);
    expect(stream.state.get()).toBe("stopped");
    await tick(60_000);
    expect(tend.opens).toHaveLength(1);
  });
});
