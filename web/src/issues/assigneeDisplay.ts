import type { AgentCategory, IssueAssignee, IssueManagerView } from "../api/generated/api.ts";

export function pendingCategoryDisplay(
  categoryID?: string,
  categories: AgentCategory[] = [],
): string | undefined {
  if (!categoryID) return undefined;
  const title = categories.find((category) => category.category_id === categoryID)?.title;
  return `待领取 · ${title || "Issue 负责人 Agent"}`;
}

export function pendingManagerDisplay(
  manager?: IssueManagerView,
  categories: AgentCategory[] = [],
): string | undefined {
  if (!manager || manager.host_id || ["completed", "cancelled"].includes(manager.state))
    return undefined;
  return pendingCategoryDisplay(manager.category_id, categories);
}

export function assigneeDisplay(
  assignee: IssueAssignee,
  displayName?: string,
  pendingName?: string,
): string {
  return pendingName ?? (assignee.kind === "owner" ? "你" : displayName || "负责人 Agent");
}
