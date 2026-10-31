import type { IssueAssignee } from "../api/generated/api.ts";

export function assigneeDisplay(assignee: IssueAssignee, displayName?: string): string {
  return assignee.kind === "owner" ? "你" : displayName || "负责人 Agent";
}
