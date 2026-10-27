import { IDBFactory } from "fake-indexeddb";
import { expect, test } from "vitest";
import { ApiError, NetworkError } from "../api/client.ts";
import type { APIResponse, Envelope, RequestView } from "../api/generated/api.ts";
import type { OpSender } from "./ops.ts";
import { DurableOp } from "./durableOp.ts";

const envelope: Envelope = {
  request_id: "req_original",
  operation: "conversation.set_associations",
  target: { type: "message", id: "message-a" },
  expected_control_revision: 7,
  payload: { work_item_ids: ["work-a"] },
};
class Sender implements OpSender {
  submitted: Envelope[] = [];
  receipt: RequestView | null = null;
  offline = true;
  lookupOffline = false;
  async submit(e: Envelope): Promise<APIResponse> {
    this.submitted.push(e);
    if (this.offline) throw new NetworkError("lost response");
    return { operation: { request_id: e.request_id, status: "succeeded", result: {} } };
  }
  async lookup(): Promise<RequestView | null> {
    if (this.lookupOffline) throw new NetworkError("no lookup");
    return this.receipt;
  }
}

test("reload reconciles a committed request without another write", async () => {
  const factory = new IDBFactory(),
    sender = new Sender();
  const first = new DurableOp("association:message-a", factory);
  expect((await first.submit(sender, envelope)).kind).toBe("unknown");
  const recovered = new DurableOp("association:message-a", factory);
  expect(await recovered.pending()).toEqual(envelope);
  sender.receipt = {
    request_id: envelope.request_id,
    operation: envelope.operation,
    status: "succeeded",
    result: {},
  };
  expect((await recovered.reconcile(sender)).kind).toBe("done");
  expect(sender.submitted).toHaveLength(1);
  expect(await recovered.pending()).toBeUndefined();
});

test("unknown receipt does not replay, missing receipt only retries immutable original", async () => {
  const journal = new DurableOp("compact:background-a", new IDBFactory()),
    sender = new Sender();
  await journal.submit(sender, envelope);
  sender.receipt = {
    request_id: envelope.request_id,
    operation: envelope.operation,
    status: "unknown",
    result: {},
  };
  await journal.reconcile(sender);
  expect(sender.submitted).toHaveLength(1);
  sender.receipt = null;
  sender.lookupOffline = true;
  await journal.reconcile(sender);
  expect(sender.submitted).toHaveLength(1);
  sender.lookupOffline = false;
  sender.offline = false;
  expect((await journal.reconcile(sender)).kind).toBe("done");
  expect(sender.submitted).toEqual([envelope, envelope]);
});

test("concurrent tabs cannot replace the target, revision or payload of a pending operation", async () => {
  const factory = new IDBFactory(),
    sender = new Sender();
  const a = new DurableOp("associations:message-a", factory);
  const b = new DurableOp("associations:message-a", factory);
  const other: Envelope = { ...envelope, request_id: "req_other", payload: { work_item_ids: [] } };
  await Promise.all([a.submit(sender, envelope), b.submit(sender, other)]);
  expect(sender.submitted).toHaveLength(1);
  expect(await a.pending()).toEqual(sender.submitted[0]);
  expect(await b.pending()).toEqual(sender.submitted[0]);
  const unrelated = new DurableOp("associations:message-b", factory);
  expect(await unrelated.pending()).toBeUndefined();
});

test.each(["missing", "offline", "committed"])(
  "HTTP 503 reconciles the original request with a %s receipt",
  async (receipt) => {
    const journal = new DurableOp("compact:server-error", new IDBFactory());
    let lookups = 0;
    const sender: OpSender = {
      submit: async () => {
        throw new ApiError(503, undefined);
      },
      lookup: async (id) => {
        expect(id).toBe(envelope.request_id);
        lookups++;
        if (receipt === "offline") throw new NetworkError("lookup failed");
        return receipt === "committed"
          ? { request_id: id, operation: envelope.operation, status: "succeeded", result: {} }
          : null;
      },
    };
    const result = await journal.submit(sender, envelope);
    expect(lookups).toBe(1);
    expect(result.kind).toBe(receipt === "committed" ? "done" : "unknown");
    expect(await journal.pending()).toEqual(receipt === "committed" ? undefined : envelope);
  },
);
