import { expect, it } from "vitest";
import type { AttentionPage, NextActionPage } from "../api/generated/api.ts";
import { inboxCount } from "./useInboxCount.ts";

it("counts open owner actions once and excludes information and resolved entries", () => {
  const summary = {
    items: [
      { attention_id: "a", status: "open", kind: "decision" },
      { attention_id: "info", status: "open", kind: "information" },
      { attention_id: "done", status: "resolved", kind: "decision" },
    ],
  } as AttentionPage;
  const next = {
    items: [
      { attention_id: "a", status: "open", kind: "user_action" },
      { status: "open", kind: "user_action" },
      { status: "blocked", kind: "user_action" },
      { status: "closed", kind: "user_action" },
      { status: "open", kind: "agent_action" },
    ],
  } as NextActionPage;
  expect(inboxCount(summary, next)).toBe(3);
  expect(inboxCount(undefined, next)).toBe(3);
  expect(inboxCount()).toBe(0);
});
