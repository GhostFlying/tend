import { expect, test, vi } from "vitest";
import { NetworkError } from "../api/client.ts";
import type { APIResponse, Envelope, RequestView, WorkItemChoice } from "../api/generated/api.ts";
import { ChoiceResponse } from "./choice.ts";
const choice: WorkItemChoice = {
  choice_id: "choice-example",
  work_item_id: "wi-example",
  version: 2,
  status: "open",
  question: "继续上线？",
  options: ["继续上线", "稍后决定"],
  source_id: "event-example",
  message_id: "message-example",
  created_at: 1,
  updated_at: 1,
};
test("unknown choice response is reconciled and retried only under its original identity", async () => {
  const envelopes: Envelope[] = [];
  let reachable = false;
  const sender = {
    submit: vi.fn(async (envelope: Envelope): Promise<APIResponse> => {
      envelopes.push(structuredClone(envelope));
      if (!reachable) throw new NetworkError("offline");
      return { operation: { request_id: envelope.request_id, status: "succeeded", result: {} } };
    }),
    lookup: vi.fn(async (): Promise<RequestView | null> => null),
  };
  const response = new ChoiceResponse(sender, vi.fn());
  await response.answer(choice, "稍后决定");
  await response.answer(choice, "继续上线");
  await response.retry();
  expect(envelopes).toHaveLength(1);
  expect(envelopes[0]!.expected_control_revision).toBe(2);
  await response.check();
  reachable = true;
  await response.retry();
  expect(envelopes).toHaveLength(2);
  expect(envelopes[1]).toEqual(envelopes[0]);
  expect(response.state.get().unknown).toBeUndefined();
});
test("closed choices do not submit and stale answers request fresh state", async () => {
  const changed = vi.fn();
  const sender = {
    submit: vi.fn(async (envelope: Envelope): Promise<APIResponse> => ({
      operation: {
        request_id: envelope.request_id,
        status: "failed",
        error: { code: "stale_revision", message: "changed" },
      },
    })),
    lookup: vi.fn(async () => null),
  };
  const response = new ChoiceResponse(sender, changed);
  await response.answer({ ...choice, status: "withdrawn" }, "继续上线");
  expect(sender.submit).not.toHaveBeenCalled();
  await response.answer(choice, "继续上线");
  expect(changed).toHaveBeenCalledOnce();
  expect(response.state.get().message).toContain("选择已变化");
});
