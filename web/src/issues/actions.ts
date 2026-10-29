import type {
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
  UpdateIssueTaskPayload,
} from "../api/generated/api.ts";
import type { OwnerCommand } from "../app/ownerCommand.ts";

interface IssuePayloads {
  update: UpdateIssuePayload;
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
