import { describe, expect, test } from "vitest";
import {
  ApiError,
  NetworkError,
  type BusinessMessage,
  type KeyValueStorage,
} from "../api/client.ts";
import type { APIResponse, Envelope, OpStatus } from "../api/generated/api.ts";
import type { OpSender } from "../app/ops.ts";
import { Interrupt } from "./interrupt.ts";

function storage(): KeyValueStorage {
  const values = new Map<string, string>();
  return {
    getItem: (k) => values.get(k) ?? null,
    setItem: (k, v) => {
      values.set(k, v);
    },
    removeItem: (k) => {
      values.delete(k);
    },
  };
}
class Sender implements OpSender {
  submissions: Envelope[] = [];
  lookups: string[] = [];
  unreachable = true;
  status: OpStatus = "accepted";
  message: BusinessMessage | null = null;
  async submit(envelope: Envelope): Promise<APIResponse> {
    this.submissions.push(structuredClone(envelope));
    if (this.unreachable) throw new NetworkError(new Error("offline"));
    return { operation: { request_id: envelope.request_id, status: this.status } };
  }
  async get<T>(path: string): Promise<T> {
    this.lookups.push(path);
    if (!this.message) throw new ApiError(404, { code: "not_found", message: "message absent" });
    return this.message as T;
  }
}
const terminal = {
  message_id: "msg_1",
  seq: 1,
  kind: "message",
  author: "owner",
  created_at: "2026-10-22T10:00:00Z",
  state: "interrupted" as const,
};

describe("durable interrupt", () => {
  test("a missing business message never authorizes another interrupt", async () => {
    const sender = new Sender(),
      interrupt = new Interrupt(sender, { storage: storage(), newId: () => "req_original" });
    await interrupt.start("msg_1");
    sender.unreachable = false;
    await Promise.all([interrupt.resume(), interrupt.resume()]);
    expect(sender.lookups).toEqual(["/api/v1/messages/msg_1"]);
    expect(sender.submissions).toHaveLength(1);
    expect(interrupt.state.get()?.phase).toBe("unknown");
    interrupt.observe([terminal]);
    expect(interrupt.state.get()).toBeNull();
  });

  test("reload keeps the original envelope and clears only on an observed terminal message", async () => {
    const sender = new Sender(),
      saved = storage();
    await new Interrupt(sender, { storage: saved, newId: () => "req_original" }).start("msg_1");
    const restored = new Interrupt(sender, {
      storage: saved,
      newId: () => {
        throw new Error("must retain identity");
      },
    });
    const original = restored.state.get()?.envelope;
    await restored.resume();
    expect(restored.state.get()?.envelope).toEqual(original);
    expect(sender.submissions).toHaveLength(1);
    sender.message = terminal;
    await restored.resume();
    expect(restored.state.get()).toBeNull();
    expect(new Interrupt(sender, { storage: saved }).state.get()).toBeNull();
  });

  test("a business unknown response stays fenced through reload", async () => {
    const sender = new Sender();
    sender.unreachable = false;
    sender.status = "unknown";
    const saved = storage();
    await new Interrupt(sender, { storage: saved, newId: () => "req_original" }).start("msg_1");
    const restored = new Interrupt(sender, { storage: saved });
    await restored.start("msg_1");
    expect(restored.state.get()?.phase).toBe("unknown");
    expect(sender.submissions).toHaveLength(1);
  });

  test("an old receipt never delays a new turn's stop or overwrites its state", async () => {
    const submissions: Envelope[] = [];
    const answers = new Map<string, (value: APIResponse) => void>();
    const sender: OpSender = {
      submit: (envelope) => {
        submissions.push(envelope);
        return new Promise((resolve) => {
          answers.set(envelope.request_id, resolve);
        });
      },
    };
    let nextId = 0;
    const interrupt = new Interrupt(sender, { storage: storage(), newId: () => `req_${++nextId}` });
    const first = interrupt.start("msg_1");
    await Promise.resolve();
    expect(submissions).toHaveLength(1);
    interrupt.observe([terminal]);
    const second = interrupt.start("msg_2");
    await Promise.resolve();
    // The first HTTP call is deliberately unresolved: the second must
    // already have reached the sender rather than waiting for its timeout.
    expect(submissions).toHaveLength(2);
    expect(submissions[1]?.target?.id).toBe("msg_2");
    expect(interrupt.state.get()).toMatchObject({ messageId: "msg_2", phase: "unconfirmed" });
    answers.get("req_1")!({ operation: { request_id: "req_1", status: "unknown" } });
    await first;
    expect(interrupt.state.get()).toMatchObject({ messageId: "msg_2", phase: "unconfirmed" });
    answers.get("req_2")!({ operation: { request_id: "req_2", status: "accepted" } });
    await second;
    expect(interrupt.state.get()).toMatchObject({ messageId: "msg_2", phase: "recorded" });
  });

  test("a terminal message cannot be resurrected by a late HTTP response", async () => {
    let answer!: (value: APIResponse) => void;
    const sender: OpSender = {
      submit: () =>
        new Promise((resolve) => {
          answer = resolve;
        }),
    };
    const saved = storage();
    const interrupt = new Interrupt(sender, { storage: saved, newId: () => "req_original" });
    const sending = interrupt.start("msg_1");
    await Promise.resolve();
    interrupt.observe([terminal]);
    answer({ operation: { request_id: "req_original", status: "unknown" } });
    await sending;
    expect(interrupt.state.get()).toBeNull();
    expect(new Interrupt(sender, { storage: saved }).state.get()).toBeNull();
  });
});
