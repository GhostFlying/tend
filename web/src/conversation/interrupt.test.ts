import { describe, expect, test } from "vitest";
import { NetworkError, type KeyValueStorage } from "../api/client.ts";
import type { APIResponse, Envelope, OpStatus, RequestView } from "../api/generated/api.ts";
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
  record: RequestView | null = null;
  async submit(envelope: Envelope): Promise<APIResponse> {
    this.submissions.push(structuredClone(envelope));
    if (this.unreachable) throw new NetworkError(new Error("offline"));
    return { operation: { request_id: envelope.request_id, status: this.status } };
  }
  async lookup(id: string): Promise<RequestView | null> {
    this.lookups.push(id);
    return this.record;
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
  test("an undelivered request resumes after reconnect under its original ID", async () => {
    const sender = new Sender();
    const interrupt = new Interrupt(sender, { storage: storage(), newId: () => "req_original" });
    await interrupt.start("msg_1");
    expect(interrupt.state.get()?.phase).toBe("unconfirmed");
    sender.unreachable = false;
    await Promise.all([interrupt.resume(), interrupt.resume()]);
    expect(sender.lookups).toEqual(["req_original"]);
    expect(sender.submissions).toHaveLength(2);
    expect(sender.submissions[1]).toEqual(sender.submissions[0]);
    expect(interrupt.state.get()?.phase).toBe("recorded");
    interrupt.observe([terminal]);
    expect(interrupt.state.get()).toBeNull();
  });

  test("page reload restores the full envelope without allocating an ID", async () => {
    const sender = new Sender();
    const saved = storage();
    await new Interrupt(sender, { storage: saved, newId: () => "req_original" }).start("msg_1");
    const restored = new Interrupt(sender, {
      storage: saved,
      newId: () => {
        throw new Error("must retain request ID");
      },
    });
    sender.unreachable = false;
    await restored.resume();
    expect(sender.lookups).toEqual(["req_original"]);
    expect(sender.submissions[1]).toEqual(sender.submissions[0]);
    restored.observe([terminal]);
    expect(new Interrupt(sender, { storage: saved }).state.get()).toBeNull();
  });

  test.each(["accepted", "succeeded", "unknown"] as const)(
    "a recorded %s outcome never resubmits its effect",
    async (status) => {
      const sender = new Sender();
      const saved = storage();
      await new Interrupt(sender, { storage: saved, newId: () => "req_original" }).start("msg_1");
      sender.record = {
        request_id: "req_original",
        operation: "conversation.interrupt_reply",
        status,
      };
      const restored = new Interrupt(sender, { storage: saved });
      await restored.resume();
      expect(sender.submissions).toHaveLength(1);
      expect(restored.state.get()?.phase).toBe(status === "unknown" ? "unknown" : "recorded");
      // Even a later missing record cannot authorize replay of a known effect.
      sender.record = null;
      await new Interrupt(sender, { storage: saved }).resume();
      expect(sender.submissions).toHaveLength(1);
    },
  );

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
      lookup: async () => null,
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
      lookup: async () => null,
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
