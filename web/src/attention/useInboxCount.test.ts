import { inboxSummary, relativeTime } from "./Inbox.tsx";
import type { AttentionView } from "../api/generated/api.ts";
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

it("inbox summaries preserve original content and relative times are explicit", () => {
  expect(
    inboxSummary({
      choice: { question: "\nOriginal agent question\nsecond line" },
    } as AttentionView),
  ).toBe("Original agent question");
  expect(
    inboxSummary({ choice: { question: "", reason: "Reason line\nsecond" } } as AttentionView),
  ).toBe("Reason line");
  expect(inboxSummary({ title: "Only a title" } as AttentionView)).toBe("");
  expect(relativeTime(60_000, 60_000)).toBe("刚刚");
  expect(relativeTime(0, 60_000 * 15)).toBe("15 分钟前");
  expect(relativeTime(0, 60_000 * 120)).toBe("2 小时前");
  expect(relativeTime(0, 60_000 * 2880)).toBe("2 天前");
});

it("reads the source summary instead of repeating a decision title", () => {
  expect(
    inboxSummary({ title: "确认范围", decision: { question: " 确认范围 " } } as AttentionView),
  ).toBe("");
  expect(
    inboxSummary({ title: "确认范围", choice: { question: "确认范围" } } as AttentionView),
  ).toBe("");
  expect(
    inboxSummary({
      title: "确认范围",
      decision: { question: "请选择需要发布的模块\n补充说明" },
    } as AttentionView),
  ).toBe("请选择需要发布的模块");
});
