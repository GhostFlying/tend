import { expect, test } from "vitest";
import { issueHandoffCommand } from "./actions.ts";

test("pending category changes replace responsibility and owner takeback cancels its task", () => {
  const pending = {
    issue_id: "issue",
    revision: 7,
    assignment_epoch: 3,
    assignee: { kind: "owner" as const, ref: "owner" },
    manager: { task_id: "manager-task", category_id: "issue-manager", state: "queued" as const },
  };
  expect(issueHandoffCommand(pending, "category:custom", "Change category", "owner")).toEqual({
    operation: "issues.request_manager",
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
