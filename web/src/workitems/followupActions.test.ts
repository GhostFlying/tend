import { expect, test, vi } from "vitest";
import type { Envelope, APIResponse, RequestView } from "../api/generated/api.ts";
import { NetworkError } from "../api/client.ts";
import { FollowupActions } from "./followupActions.ts";
function storage(): Storage {
  const m = new Map<string, string>();
  return {
    get length() {
      return m.size;
    },
    key: (i) => [...m.keys()][i] ?? null,
    getItem: (k) => m.get(k) ?? null,
    setItem: (k, v) => {
      m.set(k, v);
    },
    removeItem: (k) => {
      m.delete(k);
    },
    clear: () => m.clear(),
  };
}
const command = {
  operation: "work_items.followups_pause",
  target: { type: "followup", id: "followup-example" },
  expected_control_revision: 3,
  payload: {},
};
test("unknown control survives reload and retries the original revision and identity", async () => {
  const disk = storage();
  const submitted: Envelope[] = [];
  let reachable = false;
  const sender = {
    submit: vi.fn(async (e: Envelope): Promise<APIResponse> => {
      submitted.push(e);
      if (!reachable) throw new NetworkError("offline");
      return { operation: { request_id: e.request_id, status: "succeeded", result: {} } };
    }),
    lookup: vi.fn(async (): Promise<RequestView | null> => null),
  };
  const first = new FollowupActions(sender, "work-example", disk, vi.fn());
  await first.execute(command);
  const next = new FollowupActions(sender, "work-example", disk, vi.fn());
  expect(next.state.get().pending).toHaveLength(1);
  await next.execute({ ...command, expected_control_revision: 4 });
  expect(submitted).toHaveLength(1);
  await next.check(next.state.get().pending[0]!);
  reachable = true;
  await next.retry(next.state.get().pending[0]!);
  expect(submitted[1]).toEqual(submitted[0]);
  expect(disk.length).toBe(0);
});
test("a terminal receipt removes only its request and preserves another tab's record", async () => {
  const disk = storage();
  let release: (r: APIResponse) => void = () => {};
  const sender = {
    submit: vi.fn(
      (e: Envelope) =>
        new Promise<APIResponse>((resolve) => {
          release = resolve;
          disk.setItem(
            "tend.followup-requests.work-example.other",
            JSON.stringify({ ...e, request_id: "other" }),
          );
        }),
    ),
    lookup: vi.fn(async () => null),
  };
  const action = new FollowupActions(sender, "work-example", disk, vi.fn());
  const sent = action.execute(command);
  release({ operation: { request_id: "receipt", status: "succeeded", result: {} } });
  await sent;
  expect(action.state.get().pending.map((e) => e.request_id)).toEqual(["other"]);
});
test("failed persistent storage prevents sending", async () => {
  const disk = storage();
  disk.setItem = () => {
    throw new Error("storage full");
  };
  const sender = { submit: vi.fn(), lookup: vi.fn() };
  await new FollowupActions(sender, "work-example", disk, vi.fn()).execute(command);
  expect(sender.submit).not.toHaveBeenCalled();
});
