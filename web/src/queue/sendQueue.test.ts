import { IDBFactory } from "fake-indexeddb";
import { beforeEach, describe, expect, test } from "vitest";
import { ApiError, NetworkError } from "../api/client.ts";
import type { APIResponse, Envelope, RequestView } from "../api/generated/api.ts";
import { openOutbox, type OutgoingMessage } from "./outbox.ts";
import { choiceOf, SendQueue, type QueueOptions, type Sender } from "./sendQueue.ts";

type Answer = APIResponse | Error | Promise<APIResponse>;

/** A scripted tend: each submit takes the next answer, or "accepted". */
class FakeSender implements Sender {
  readonly submitted: Envelope[] = [];
  readonly looked: string[] = [];
  answers: Answer[] = [];
  records = new Map<string, RequestView>();
  private seq = 0;

  async submit(envelope: Envelope): Promise<APIResponse> {
    // What a browser would put on the wire.
    this.submitted.push(JSON.parse(JSON.stringify(envelope)) as Envelope);
    const next = this.answers.shift();
    if (next instanceof Error) {
      throw next;
    }
    if (next) {
      return next;
    }
    this.seq++;
    return accepted(envelope.request_id, `msg_${this.seq}`, this.seq);
  }

  async lookup(requestId: string): Promise<RequestView | null> {
    this.looked.push(requestId);
    return this.records.get(requestId) ?? null;
  }
}

function accepted(requestId: string, messageId: string, seq: number): APIResponse {
  return {
    operation: {
      request_id: requestId,
      status: "accepted",
      result: { message_id: messageId, seq },
    },
  };
}

function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void } {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

let factory: IDBFactory;
let sender: FakeSender;
let clock: number;
let ids: number;
let pauses: number[];
let online: boolean;

beforeEach(() => {
  factory = new IDBFactory();
  sender = new FakeSender();
  clock = 1_000_000;
  ids = 0;
  pauses = [];
  online = true;
});

async function open(extra: Partial<QueueOptions> = {}): Promise<SendQueue> {
  return SendQueue.open({
    outbox: await openOutbox(factory),
    sender,
    now: () => clock,
    newId: () => `req_${String(++ids).padStart(26, "0")}`,
    sleep: async (ms) => {
      pauses.push(ms);
    },
    online: () => online,
    backoffMs: [10, 20],
    ...extra,
  });
}

/** What a reloaded page would find in IndexedDB. */
async function stored(): Promise<OutgoingMessage[]> {
  return (await openOutbox(factory)).all();
}

describe("SendQueue", () => {
  test("image-only messages keep exact bytes and request identity through offline reload and retry", async () => {
    online = false;
    sender.answers = [new NetworkError(new TypeError("offline"))];
    const first = await open();
    const image = { name: "capture.png", media_type: "image/png", data_base64: "AQIDBA==" };
    const queued = await first.enqueue({ body: "", images: [image] });
    await first.idle();
    image.data_base64 = "changed after enqueue";
    expect((await stored())[0]?.envelope.payload).toEqual({
      body: "",
      images: [{ ...image, data_base64: "AQIDBA==" }],
    });
    online = true;
    sender.answers = [new NetworkError(new TypeError("lost receipt"))];
    const reloaded = await open();
    await reloaded.resume();
    expect(sender.submitted).toHaveLength(3);
    for (const envelope of sender.submitted) expect(envelope).toEqual(queued.envelope);
    expect((await stored())[0]?.state).toBe("sent");
  });

  test("writes a message down before sending it", async () => {
    const answer = deferred<APIResponse>();
    sender.answers = [answer.promise];
    const q = await open();

    const m = await q.enqueue({ body: "hello", replyTo: "msg_9", lastSeenSeq: 7 });

    expect(await stored()).toEqual([m]);
    expect(m.state).toBe("sending");
    expect(m.envelope).toEqual({
      request_id: m.requestId,
      operation: "conversation.submit_message",
      target: { type: "conversation" },
      payload: { body: "hello", reply_to: "msg_9", last_seen_seq: 7 },
    });
    answer.resolve(accepted(m.requestId, "msg_10", 10));
    await q.idle();
    expect(sender.submitted).toEqual([m.envelope]);
  });

  test("a message can choose a decision option", async () => {
    const q = await open();
    const decision = { decision_id: "dec_1", version: 2, option: "revise the spec" };
    const m = await q.enqueue({ body: "关于『x』：修改 spec", decision });
    await q.idle();
    expect(sender.submitted[0]?.payload).toEqual({ body: "关于『x』：修改 spec", decision });
    expect(choiceOf(m)).toEqual(decision);
  });

  test("a receipt marks the message sent", async () => {
    const q = await open();
    const m = await q.enqueue({ body: "hello" });
    await q.idle();

    const [sent] = await stored();
    expect(sent).toMatchObject({
      requestId: m.requestId,
      state: "sent",
      messageId: "msg_1",
      seq: 1,
    });
    expect(q.list.get()).toEqual([sent]);
  });

  test("a reload sends what is pending with the same request ID", async () => {
    online = false;
    sender.answers = [new NetworkError(new TypeError("offline"))];
    const first = await open();
    const m = await first.enqueue({ body: "written offline" });
    await first.idle();
    expect((await stored())[0]?.state).toBe("sending");

    online = true;
    const reloaded = await open();
    await reloaded.resume();

    expect(sender.submitted.map((e) => e.request_id)).toEqual([m.requestId, m.requestId]);
    expect(sender.submitted[1]).toEqual(m.envelope);
    expect((await stored())[0]?.state).toBe("sent");
  });

  test("without a network it waits instead of using up its tries", async () => {
    online = false;
    sender.answers = [new NetworkError(new TypeError("offline"))];
    const q = await open();
    await q.enqueue({ body: "hi" });
    await q.idle();

    expect(sender.submitted).toHaveLength(1);
    expect(pauses).toEqual([]);
    expect(q.list.get()[0]?.state).toBe("sending");

    online = true;
    await q.resume();
    expect(q.list.get()[0]?.state).toBe("sent");
  });

  test("no answer after the retries makes it unknown", async () => {
    const lost = () => new NetworkError(new TypeError("reset"));
    sender.answers = [lost(), new ApiError(502, undefined), lost()];
    const q = await open();
    const m = await q.enqueue({ body: "hi" });
    await q.idle();

    expect(pauses).toEqual([10, 20]);
    expect(sender.submitted.map((e) => e.request_id)).toEqual([
      m.requestId,
      m.requestId,
      m.requestId,
    ]);
    const [unknown] = await stored();
    expect(unknown?.state).toBe("unknown");
    expect(unknown?.error).toBeTruthy();
  });

  test("a retry that gets an answer is sent", async () => {
    sender.answers = [new ApiError(503, undefined)];
    const q = await open();
    await q.enqueue({ body: "hi" });
    await q.idle();
    expect(sender.submitted).toHaveLength(2);
    expect(q.list.get()[0]?.state).toBe("sent");
  });

  test("resend reuses the original request ID and envelope", async () => {
    const lost = () => new NetworkError(new TypeError("reset"));
    sender.answers = [lost(), lost(), lost()];
    const q = await open();
    const m = await q.enqueue({ body: "hi" });
    await q.idle();
    expect(q.list.get()[0]?.state).toBe("unknown");

    await q.resend(m.requestId);

    expect(sender.submitted).toHaveLength(4);
    expect(sender.submitted[3]).toEqual(m.envelope);
    expect(q.list.get()[0]).toMatchObject({ state: "sent", messageId: "msg_1" });
    expect((await stored())[0]?.error).toBeUndefined();
  });

  test("unknown messages are not sent again by themselves", async () => {
    sender.answers = [
      {
        operation: {
          request_id: "x",
          status: "unknown",
          error: { code: "unknown_outcome", message: "?" },
        },
      },
    ];
    const q = await open();
    await q.enqueue({ body: "hi" });
    await q.idle();

    expect(sender.submitted).toHaveLength(1);
    expect(pauses).toEqual([]);
    expect(q.list.get()[0]?.state).toBe("unknown");
  });

  test("reconnecting settles an unknown message tend recorded", async () => {
    const lost = () => new NetworkError(new TypeError("reset"));
    sender.answers = [lost(), lost(), lost()];
    const q = await open();
    const m = await q.enqueue({ body: "hi" });
    await q.idle();
    sender.records.set(m.requestId, {
      request_id: m.requestId,
      operation: "conversation.submit_message",
      status: "accepted",
      result: { message_id: "msg_7", seq: 7 },
    });

    await q.resume();

    expect(sender.looked).toEqual([m.requestId]);
    expect(sender.submitted).toHaveLength(3);
    expect(q.list.get()[0]).toMatchObject({ state: "sent", messageId: "msg_7", seq: 7 });
  });

  test("reconnecting resends an unknown message tend never recorded", async () => {
    const lost = () => new NetworkError(new TypeError("reset"));
    sender.answers = [lost(), lost(), lost()];
    const q = await open();
    const m = await q.enqueue({ body: "hi" });
    await q.idle();

    await q.resume();

    expect(sender.submitted).toHaveLength(4);
    expect(sender.submitted[3]).toEqual(m.envelope);
    expect(q.list.get()[0]?.state).toBe("sent");
  });

  test("a refused message fails and is not retried", async () => {
    sender.answers = [
      new ApiError(400, { code: "invalid_request", message: "body must be non-empty" }),
    ];
    const q = await open();
    const m = await q.enqueue({ body: "x" });
    await q.idle();

    expect(sender.submitted).toHaveLength(1);
    expect(q.list.get()[0]).toMatchObject({ state: "failed", error: "body must be non-empty" });
    await q.resend(m.requestId);
    expect(sender.submitted).toHaveLength(1);
  });

  test("a device that is not an owner's stops sending", async () => {
    sender.answers = [new ApiError(403, { code: "forbidden", message: "only owners" })];
    const q = await open();
    await q.enqueue({ body: "hi" });
    await q.idle();

    expect(sender.submitted).toHaveLength(1);
    expect(pauses).toEqual([]);
    expect(q.list.get()[0]?.state).toBe("sending");
  });

  test("messages go out one at a time in the order they were written", async () => {
    const first = deferred<APIResponse>();
    sender.answers = [first.promise];
    const q = await open();
    const a = await q.enqueue({ body: "one" });
    clock++;
    const b = await q.enqueue({ body: "two" });
    await Promise.resolve();
    expect(sender.submitted.map((e) => e.request_id)).toEqual([a.requestId]);

    first.resolve(accepted(a.requestId, "msg_1", 1));
    await q.idle();

    expect(sender.submitted.map((e) => e.request_id)).toEqual([a.requestId, b.requestId]);
    expect(q.list.get().map((m) => m.state)).toEqual(["sent", "sent"]);
  });

  describe("an unknown message holds back the ones after it", () => {
    async function heldBack() {
      const lost = () => new NetworkError(new TypeError("reset"));
      sender.answers = [lost(), lost(), lost()];
      const q = await open();
      const a = await q.enqueue({ body: "one" });
      await q.idle();
      clock++;
      const b = await q.enqueue({ body: "two" });
      await q.idle();
      expect(q.list.get().map((m) => m.state)).toEqual(["unknown", "sending"]);
      expect(sender.submitted.map((e) => e.request_id)).toEqual([
        a.requestId,
        a.requestId,
        a.requestId,
      ]);
      return { q, a, b };
    }

    test("until a reconnect finds tend never got it and resends it first", async () => {
      const { q, a, b } = await heldBack();
      await q.resume();
      expect(sender.submitted.slice(3).map((e) => e.request_id)).toEqual([
        a.requestId,
        b.requestId,
      ]);
      expect(q.list.get().map((m) => m.state)).toEqual(["sent", "sent"]);
    });

    test("until a reconnect finds tend has it", async () => {
      const { q, a, b } = await heldBack();
      sender.records.set(a.requestId, {
        request_id: a.requestId,
        operation: "conversation.submit_message",
        status: "accepted",
        result: { message_id: "msg_7", seq: 7 },
      });
      await q.resume();
      await q.idle();
      expect(sender.submitted.slice(3).map((e) => e.request_id)).toEqual([b.requestId]);
      expect(q.list.get().map((m) => m.state)).toEqual(["sent", "sent"]);
    });

    test("until the owner deletes it", async () => {
      const { q, a, b } = await heldBack();
      await q.discard(a.requestId);
      await q.idle();
      expect(sender.submitted.slice(3).map((e) => e.request_id)).toEqual([b.requestId]);
      expect(q.list.get()).toMatchObject([{ requestId: b.requestId, state: "sent" }]);
    });
  });

  test("messages written in the same millisecond keep their order across a reload", async () => {
    // Request IDs whose random part sorts against the order of writing.
    let n = 100;
    const newId = () => `req_${String(--n).padStart(26, "0")}`;
    online = false;
    const offline = () => new NetworkError(new TypeError("offline"));
    sender.answers = [offline(), offline()];
    const first = await open({ newId });
    const a = await first.enqueue({ body: "one" });
    const b = await first.enqueue({ body: "two" });
    await first.idle();
    expect(a.requestId > b.requestId).toBe(true);

    online = true;
    const reloaded = await open({ newId });
    expect(reloaded.list.get().map((m) => m.body)).toEqual(["one", "two"]);
    await reloaded.resume();
    // Offline, each enqueue tried the oldest message once.
    expect(sender.submitted.slice(2).map((e) => e.request_id)).toEqual([a.requestId, b.requestId]);

    // New messages come after the stored ones even if the clock went back.
    clock -= 60_000;
    const c = await reloaded.enqueue({ body: "three" });
    await reloaded.idle();
    expect(c.order).toBeGreaterThan(b.order);
    expect(reloaded.list.get().map((m) => m.body)).toEqual(["one", "two", "three"]);
  });

  test("discarding removes a message", async () => {
    sender.answers = [new ApiError(400, { code: "invalid_request", message: "no" })];
    const q = await open();
    const m = await q.enqueue({ body: "x" });
    await q.idle();
    await q.discard(m.requestId);
    expect(q.list.get()).toEqual([]);
    expect(await stored()).toEqual([]);
  });

  test("opening drops messages sent long ago", async () => {
    const q = await open({ keepSentMs: 1_000 });
    await q.enqueue({ body: "old" });
    await q.idle();
    clock += 500;
    await q.enqueue({ body: "recent" });
    await q.idle();

    clock += 600;
    const reopened = await open({ keepSentMs: 1_000 });
    expect(reopened.list.get().map((m) => m.body)).toEqual(["recent"]);
    expect((await stored()).map((m) => m.body)).toEqual(["recent"]);
  });
});
