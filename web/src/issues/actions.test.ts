import { expect, test } from "vitest";
import { pendingManagerDisplay } from "./assigneeDisplay.ts";
import {
  createIssueResponsibility,
  issueHandoffCategoryTarget,
  issueHandoffCommand,
} from "./actions.ts";

test("pending category changes replace responsibility and owner takeback cancels its task", () => {
  const pending = {
    issue_id: "issue",
    revision: 7,
    assignment_epoch: 3,
    assignee: { kind: "owner" as const, ref: "owner" },
    manager: { task_id: "manager-task", category_id: "issue-manager", state: "queued" as const },
  };
  expect(issueHandoffCommand(pending, "category:custom", "Change category", "owner")).toEqual({
    operation: "issues.request_owner",
    target: { type: "issue", id: "issue" },
    expected_control_revision: 7,
    payload: {
      category_id: "custom",
      replace: true,
      reason: "Change category",
      expected_assignment_epoch: 3,
    },
  });
  expect(issueHandoffCommand(pending, "owner", "Take back before claim", "owner")).toEqual({
    operation: "issues.manager_migration",
    target: { type: "issue", id: "issue" },
    expected_control_revision: 7,
    payload: { phase: "cancel", reason: "Take back before claim", expected_assignment_epoch: 3 },
  });
  const assigned = {
    ...pending,
    assignee: { kind: "agent" as const, ref: "runner/agent" },
    manager: { ...pending.manager, host_id: "runner", state: "waiting" as const },
  };
  expect(issueHandoffCommand(assigned, "owner", "Take back after claim", "owner").operation).toBe(
    "issues.assign",
  );
});

test("pending assignee label uses category title without claiming owner responsibility", () => {
  const manager = { task_id: "task", category_id: "issue-manager", state: "queued" as const };
  const categories = [
    {
      category_id: "issue-manager",
      revision: 1,
      title: "负责协调",
      description: "",
      state: "active",
    },
  ];
  expect(pendingManagerDisplay(manager, categories)).toBe("待领取 · 负责协调");
  expect(pendingManagerDisplay({ ...manager, host_id: "host" }, categories)).toBeUndefined();
  expect(pendingManagerDisplay({ ...manager, state: "cancelled" }, categories)).toBeUndefined();
});

test("a fresh create after rejection replaces restored responsibility but retains source facts", () => {
  const restored = {
    plan: { title: "Original", project: "default", intent: "Intent", stages: [] },
    source_message_id: "source",
    expected_source_revision: 7,
    manager: {
      category_id: "custom-coordinator",
      reason: "Original responsibility",
      expected_assignment_epoch: 1,
    },
  };
  const ownerCreate = createIssueResponsibility(restored, "owner", "owner@example.test");
  expect(ownerCreate.manager).toBeUndefined();
  expect(ownerCreate.owner).toBeUndefined();
  expect(ownerCreate.assignee).toEqual({ kind: "owner", ref: "owner@example.test" });
  expect(ownerCreate.source_message_id).toBe("source");
  expect(ownerCreate.expected_source_revision).toBe(7);
  const categoryCreate = createIssueResponsibility(
    ownerCreate,
    "category:issue-manager",
    "owner@example.test",
  );
  expect(categoryCreate.assignee).toBeUndefined();
  expect(categoryCreate.manager).toBeUndefined();
  expect(categoryCreate.owner?.category_id).toBe("issue-manager");
  expect(restored.manager.category_id).toBe("custom-coordinator");
});

test("retained owner handoff restores its custom category alongside historical requests", () => {
  for (const operation of [
    "issues.request_owner",
    "issues.request_manager",
    "issues.manager_migration",
  ]) {
    expect(
      issueHandoffCategoryTarget({
        operation,
        payload: {
          category_id: "custom-owner",
          reason: "Retained request",
          expected_assignment_epoch: 3,
        },
      }),
    ).toBe("category:custom-owner");
  }
  expect(
    issueHandoffCategoryTarget({
      operation: "issues.manager_migration",
      payload: { phase: "cancel" },
    }),
  ).toBe("owner");
});
