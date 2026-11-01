import type {
  ManagerMigrationPayload,
  RequestIssueManagerPayload,
  CreateIssuePayload,
  CreateIssueTaskPayload,
  FinishIssueAssignmentPayload,
  IssueAssignmentPayload,
  IssueControlPayload,
  IssueEndpointPayload,
  IssueView,
  IssueFollowupPayload,
  IssueRefreshDeliveryPayload,
  ReparentIssuePayload,
  ReviewIssueCompletionsPayload,
  ReviewIssueSourcesPayload,
  SetIssueDependenciesPayload,
  UpdateIssuePayload,
  UpdateIssueProgressPayload,
  UpdateIssueTaskPayload,
} from "../api/generated/api.ts";
import type { OwnerCommand } from "../app/ownerCommand.ts";

interface IssuePayloads {
  manager_migration: ManagerMigrationPayload;
  request_manager: RequestIssueManagerPayload;
  request_owner: RequestIssueManagerPayload;
  update: UpdateIssuePayload;
  update_progress: UpdateIssueProgressPayload;
  pause: IssueControlPayload;
  resume: IssueControlPayload;
  cancel: IssueControlPayload;
  finish_cancel: IssueEndpointPayload;
  close: IssueEndpointPayload;
  reopen: IssueControlPayload;
  reparent: ReparentIssuePayload;
  dependencies: SetIssueDependenciesPayload;
  review_sources: ReviewIssueSourcesPayload;
  review_completions: ReviewIssueCompletionsPayload;
  assign: IssueAssignmentPayload;
  finish_assignment: FinishIssueAssignmentPayload;
  create_task: CreateIssueTaskPayload;
  refresh_delivery: IssueRefreshDeliveryPayload;
}

type IssueSnapshot = Pick<IssueView, "issue_id" | "revision" | "assignment_epoch">;

export function issueHandoffCommand(
  issue: IssueSnapshot & Pick<IssueView, "assignee" | "manager" | "pending_handoff_id">,
  target: string,
  reason: string,
  owner: string,
): OwnerCommand {
  if (issue.pending_handoff_id)
    return issueCommand(issue, "finish_assignment", {
      handoff_id: issue.pending_handoff_id,
      reason,
    });
  const pendingManager =
    !!issue.manager && !issue.manager.host_id && issue.manager.state === "queued";
  if (target.startsWith("category:")) {
    const category_id = target.slice("category:".length);
    return issue.assignee.kind === "agent" && !issue.manager
      ? issueCommand(issue, "manager_migration", { phase: "switch", category_id, reason })
      : issueCommand(issue, "request_owner", {
          category_id,
          replace: pendingManager || issue.assignee.kind === "agent",
          reason,
        });
  }
  if (target === "owner" && pendingManager)
    return issueCommand(issue, "manager_migration", { phase: "cancel", reason });
  return issueCommand(issue, "assign", {
    assignee: target === "owner" ? { kind: "owner", ref: owner } : { kind: "agent", ref: target },
    reason,
  });
}

/** A fresh create keeps restored source facts but uses the current responsibility choice. */
export function createIssueResponsibility(
  payload: CreateIssuePayload,
  target: string,
  owner: string,
): CreateIssuePayload {
  const next = { ...payload };
  delete next.manager;
  delete next.owner;
  delete next.assignee;
  if (target.startsWith("category:"))
    next.owner = {
      category_id: target.slice("category:".length),
      reason: "按类别委派 Issue 持续协调责任",
    };
  else next.assignee = { kind: "owner", ref: owner };
  return next;
}

export function createIssueCommand(payload: CreateIssuePayload): OwnerCommand {
  return { operation: "issues.create", target: { type: "issue" }, payload };
}

/** Capture the revision and epoch the owner is actually acting on. */
export function issueCommand<Action extends keyof IssuePayloads>(
  issue: IssueSnapshot,
  action: Action,
  payload: Omit<IssuePayloads[Action], "expected_assignment_epoch">,
): OwnerCommand {
  return {
    operation: `issues.${action}`,
    target: { type: "issue", id: issue.issue_id },
    expected_control_revision: issue.revision,
    payload: { ...payload, expected_assignment_epoch: issue.assignment_epoch },
  };
}

export function updateIssueTaskCommand(
  taskID: string,
  controlRevision: number,
  payload: UpdateIssueTaskPayload,
): OwnerCommand {
  return {
    operation: "issues.update_task",
    target: { type: "task", id: taskID },
    expected_control_revision: controlRevision,
    payload,
  };
}

export function createIssueFollowupCommand(
  issue: IssueSnapshot,
  payload: Omit<IssueFollowupPayload, "expected_assignment_epoch">,
): OwnerCommand {
  return {
    operation: "issues.followups_create",
    target: { type: "issue", id: issue.issue_id },
    expected_control_revision: issue.revision,
    payload: { ...payload, expected_assignment_epoch: issue.assignment_epoch },
  };
}

/** A rule edit compares the rule revision, independently of its Issue revision. */
export function issueFollowupCommand(
  issue: Pick<IssueView, "assignment_epoch">,
  rule: { rule_id: string; revision: number },
  action: "update" | "pause" | "resume" | "cancel" | "trigger",
  payload: Omit<IssueFollowupPayload, "expected_assignment_epoch"> = {},
): OwnerCommand {
  return {
    operation: `issues.followups_${action}`,
    target: { type: "followup", id: rule.rule_id },
    expected_control_revision: rule.revision,
    payload: { ...payload, expected_assignment_epoch: issue.assignment_epoch },
  };
}

/** Decode the frozen category choice before reconciling a retained handoff. */
export function issueHandoffCategoryTarget(
  original: Pick<OwnerCommand, "operation" | "payload"> | null | undefined,
): string | undefined {
  if (
    original?.operation === "issues.manager_migration" &&
    (original.payload as ManagerMigrationPayload).phase === "cancel"
  )
    return "owner";
  if (
    original?.operation === "issues.request_owner" ||
    original?.operation === "issues.request_manager" ||
    original?.operation === "issues.manager_migration"
  )
    return `category:${(original.payload as RequestIssueManagerPayload | ManagerMigrationPayload).category_id ?? "issue-manager"}`;
  return undefined;
}
