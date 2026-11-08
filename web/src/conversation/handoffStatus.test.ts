import { expect, test } from "vitest";
import type { ConversationHandoffView } from "../api/generated/api.ts";
import type { HandoffState } from "./handoff.ts";
import { handoffStatus } from "./handoffStatus.ts";

const view: ConversationHandoffView = {
  enabled: true,
  idle_minutes: 50,
  revision: 1,
  generation: 1,
  pending: false,
  preparing: false,
  last_activity_millis: 1,
};
const state = (patch: Partial<ConversationHandoffView> = {}): HandoffState => ({
  view: { ...view, ...patch },
  reading: false,
  saving: false,
});

test("main work is not evidence that a summary is generating or queued", () => {
  expect(handoffStatus(state({ pending: true }))).toBe("主会话仍有未结工作");
  for (const status of ["pending", "sending"])
    expect(
      handoffStatus(
        state({
          pending: true,
          maintenance: {
            request_id: "original",
            operation: "conversation.handoff",
            status,
          },
        }),
      ),
    ).toBe("交接请求尚未完成");
});

test("unrelated pending work cannot hide the recorded failed or unknown request", () => {
  for (const [status, label] of [
    ["failed", "上次请求未完成"],
    ["unknown", "上次请求结果待确认"],
  ] as const) {
    const s = state({
      pending: true,
      maintenance: {
        request_id: "original",
        operation: "conversation.handoff",
        status,
        reason: "unavailable",
      },
    });
    const before = structuredClone(s);
    expect(handoffStatus(s)).toBe(label);
    expect(s).toEqual(before);
  }
});

test("an unavailable observation differs from a known request failure", () => {
  expect(handoffStatus({ ...state(), readError: "observation_unavailable" })).toBe(
    "交接状态暂不可读取",
  );
  expect(handoffStatus({ ...state(), pendingId: "unknown-write" })).toBe("操作结果待核对");
  expect(handoffStatus({ ...state(), error: "request failed" })).toBe("操作未完成");
});
