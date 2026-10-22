import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { ApiError, NetworkError } from "../api/client.ts";
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
    return new Response(body, { headers: { "Content-Type": "text/event-stream" } });
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
let lost: string[];
let stream: EventStream;

const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

beforeEach(() => {
  vi.useFakeTimers();
  tend = new FakeTend();
  starts = [];
  events = [];
  lost = [];
  stream = new EventStream({
    open: tend.open,
    onStart: (seq, fresh) => starts.push([seq, fresh]),
    onEvent: (ev) => events.push(ev.event_seq),
    onLost: (reason) => lost.push(reason),
    silenceMs: 45_000,
  });
});

afterEach(async () => {
  await stream.stop();
  vi.useRealTimers();
});

describe("EventStream", () => {
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
    expect(lost).toEqual(["事件流太久没有消息"]);
    await tick(1_000);
    expect(tend.opens.map((o) => o.after)).toEqual([undefined, 3]);
  });

  test("a stream that never answers counts as lost too", async () => {
    stream.start();
    await tick(45_000);
    expect(lost).toHaveLength(1);
  });

  test("a refused cursor drops it and reconnects at once without one", async () => {
    stream.start();
    await tick();
    tend.last.push(start(9));
    await tick();
    tend.refuse = (after) =>
      after === undefined
        ? undefined
        : new ApiError(400, { code: "invalid_request", message: "after 9 is past the ledger" });
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
