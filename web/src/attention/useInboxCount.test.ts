import {
  attentionContextTitle,
  attentionHeading,
  attentionFailure,
  attentionOptionHelp,
  attentionTaskStatus,
  inboxSummary,
  relativeTime,
} from "./Inbox.tsx";
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

it("identifies the Issue before a generic failure without rewriting custom questions", () => {
  const item: AttentionView = {
    attention_id: "attention-pwa",
    kind: "decision",
    revision: 1,
    created_at: 0,
    updated_at: 0,
    latest_action_reminders: false,
    raised_by: { kind: "owner", subject: "owner" },
    issue_id: "issue-pwa",
    issue_title: "移动端 PWA 安全区、附件间距与输入光标修复",
    status: "open",
    title: "实现失败（执行 Agent 异常退出）。这个任务接下来怎么办？",
    decision: {
      decision_id: "decision-pwa",
      task_id: "task-pwa",
      version: 1,
      status: "open",
      options: ["retry the implementation"],
      question: "实现失败（执行 Agent 异常退出）。这个任务接下来怎么办？",
      effects: { "retry the implementation": "implement" },
    },
  };
  expect(attentionContextTitle(item)).toBe(item.issue_title);
  expect(attentionHeading(item)).toBe("实现失败，等待你决定下一步");
  expect(attentionFailure(item)).toEqual({ stage: "实现", reason: "执行 Agent 异常退出" });
  expect(
    attentionFailure({ ...item, decision: { ...item.decision!, effects: {} } }),
  ).toBeUndefined();
  expect(attentionContextTitle({} as AttentionView)).toBe("全局事项");
  expect(attentionContextTitle({ issue_id: "unavailable" } as AttentionView)).toBe(
    "所属 Issue 名称暂不可用",
  );
  expect(
    attentionHeading({
      ...item,
      decision: { ...item.decision!, question: "先检查哪张截图？", effects: {} },
    }),
  ).toBe("先检查哪张截图？");
  expect(attentionHeading({ ...item, status: "resolved" })).toBe(item.title);
  expect(attentionOptionHelp(item, "retry the implementation")).toContain("当前任务要求");
  expect(attentionOptionHelp(item, "revise the spec")).toContain("不会直接重新执行");
  expect(
    attentionOptionHelp(
      { ...item, decision: { ...item.decision!, effects: {} } },
      "revise the spec",
    ),
  ).toBe("");
  expect(
    attentionTaskStatus({
      ...item,
      task_context: {
        goal: "Repair mobile layout",
        question_current: true,
        lifecycle: "queued",
        control_mode: "managed",
        execution: { state: "waiting_owner" },
        block_reason: "waiting_decision",
        has_current_artifact: false,
        current_artifact_accepted: false,
      },
    }),
  ).toBe("等待你的决定");
  expect(
    attentionTaskStatus({
      ...item,
      task_context: {
        goal: "Repair mobile layout",
        question_current: true,
        lifecycle: "running",
        control_mode: "managed",
        block_reason: "waiting_decision",
        execution: { state: "unknown" },
        has_current_artifact: false,
        current_artifact_accepted: false,
      },
    }),
  ).toBe("执行待核对");
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
