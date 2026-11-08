import { expect, test, vi } from "vitest";
import { ConversationHandoff } from "./handoff.ts";
import type { ConversationHandoffView } from "../api/generated/api.ts";
import type { Reader } from "../live/data.ts";

const view: ConversationHandoffView = {
  enabled: true,
  mode: "index",
  idle_minutes: 0,
  revision: 0,
  generation: 1,
  pending: false,
  preparing: false,
  last_activity_millis: 0,
};
test("indexed handoff reads status without producing maintenance writes", async () => {
  const get = vi.fn(async () => view);
  const h = new ConversationHandoff({ get } as unknown as Reader);
  h.start();
  await h.refresh();
  expect(h.state.get().view?.mode).toBe("index");
  expect(get.mock.calls.length).toBeGreaterThan(0);
  expect("prepare" in h).toBe(false);
  expect("save" in h).toBe(false);
  await h.stop();
});
test("status changes coalesce, idle state does not keep polling", async () => {
  vi.useFakeTimers();
  const get = vi.fn(async () => view);
  const h = new ConversationHandoff({ get } as unknown as Reader);
  h.start();
  await h.refresh();
  get.mockClear();
  h.invalidate(1);
  h.invalidate(2);
  h.invalidate(2);
  await vi.advanceTimersByTimeAsync(1000);
  expect(get).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(60000);
  expect(get).toHaveBeenCalledTimes(1);
  await h.stop();
  vi.useRealTimers();
});
