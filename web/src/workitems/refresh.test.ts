import { expect, test, vi } from "vitest";
import { NetworkError } from "../api/client.ts";
import type {
  APIResponse,
  Envelope,
  RequestView,
  WorkItemDelivery,
  WorkItemView,
} from "../api/generated/api.ts";
import { DeliveryRefresh } from "./refresh.ts";

const item: WorkItemView = {
  work_item_id: "wi_example",
  title: "Track review",
  intent: "Track review",
  project: "sample",
  revision: 4,
  status: "open",
  column: "unplanned",
  stages: [],
  created_at: 1,
  updated_at: 1,
};
const delivery: WorkItemDelivery = {
  delivery_id: "pr",
  provider: "github",
  repository: "example/repo",
  number: 1,
};

test.each(["merged", "closed"])(
  "a lost terminal %s refresh recovers under its original request identity",
  async (state) => {
    const envelopes: Envelope[] = [];
    let reachable = false;
    const sender = {
      submit: vi.fn(async (envelope: Envelope): Promise<APIResponse> => {
        envelopes.push(structuredClone(envelope));
        if (!reachable) throw new NetworkError("lost before server");
        return { operation: { request_id: envelope.request_id, status: "succeeded", result: {} } };
      }),
      lookup: vi.fn(async (): Promise<RequestView | null> => null),
    };
    const action = new DeliveryRefresh(sender, vi.fn());
    await action.start(item, delivery, {
      work_item_id: item.work_item_id,
      delivery_id: "pr",
      generation: 1,
      state,
      checked_at: 1000,
    });
    expect(action.state.get().unknown?.envelope).toEqual(envelopes[0]);
    await action.retry(); // Not authorized by a missing receipt yet.
    expect(envelopes).toHaveLength(1);
    await action.check();
    expect(action.state.get().unknown?.missing).toBe(true);
    reachable = true;
    await action.retry();
    expect(envelopes).toHaveLength(2);
    expect(envelopes[1]).toEqual(envelopes[0]);
    expect(action.state.get().unknown).toBeUndefined();
    expect(action.state.get().message).toContain("已请求重新观察");
  },
);

test.each(["succeeded", "failed"] as const)(
  "a later %s request receipt settles without resubmitting",
  async (status) => {
    let receipt: RequestView | null = null;
    const sender = {
      submit: vi.fn(async (): Promise<APIResponse> => {
        throw new NetworkError("lost reply");
      }),
      lookup: vi.fn(async () => receipt),
    };
    const action = new DeliveryRefresh(sender, vi.fn());
    await action.start(item, delivery);
    const request = action.state.get().unknown!.envelope;
    receipt = {
      request_id: request.request_id,
      operation: "work_items.refresh_delivery",
      status,
      ...(status === "failed" ? { error: { code: "stale_revision", message: "changed" } } : {}),
    };
    await action.check();
    expect(sender.submit).toHaveBeenCalledTimes(1);
    expect(sender.lookup).toHaveBeenLastCalledWith(request.request_id);
    expect(action.state.get().unknown).toBeUndefined();
    expect(action.state.get().message).toContain(
      status === "succeeded" ? "已请求" : "计划刚被修改",
    );
  },
);

test("unknown or unreachable receipt never enables a resend", async () => {
  let reachable = true;
  const sender = {
    submit: vi.fn(async (): Promise<APIResponse> => {
      throw new NetworkError("lost");
    }),
    lookup: vi.fn(async (): Promise<RequestView | null> => {
      if (!reachable) throw new NetworkError("offline");
      return {
        request_id: "original",
        operation: "work_items.refresh_delivery",
        status: "unknown",
      };
    }),
  };
  const action = new DeliveryRefresh(sender, vi.fn());
  await action.start(item, delivery);
  await action.check();
  reachable = false;
  await action.check();
  await action.retry();
  expect(sender.submit).toHaveBeenCalledTimes(1);
  expect(action.state.get().unknown?.missing).toBe(false);
});
