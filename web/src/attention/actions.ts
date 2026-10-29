import type {
  AttentionView,
  ConfirmAttentionPayload,
  CreateAttentionPayload,
  PresentAttentionPayload,
  RespondAttentionPayload,
  UpdateAttentionPayload,
  PutIssueDeadlinePayload,
  CancelIssueDeadlinePayload,
  PutPlannerEntryPayload,
} from "../api/generated/api.ts";
import type { OwnerCommand } from "../app/ownerCommand.ts";

interface AttentionPayloads {
  respond: RespondAttentionPayload;
  confirm: ConfirmAttentionPayload;
  present: PresentAttentionPayload;
  update: UpdateAttentionPayload;
}

export function createAttentionCommand(payload: CreateAttentionPayload): OwnerCommand {
  return { operation: "attentions.create", target: { type: "attention" }, payload };
}

/** The answer's original question version stays in its generated payload. */
export function attentionCommand<Action extends keyof AttentionPayloads>(
  attention: Pick<AttentionView, "attention_id" | "revision">,
  action: Action,
  payload: AttentionPayloads[Action],
): OwnerCommand {
  return {
    operation: `attentions.${action}`,
    target: { type: "attention", id: attention.attention_id },
    expected_control_revision: attention.revision,
    payload,
  };
}

/** Withdrawal records its reason on the original owner envelope, not an answer. */
export function withdrawAttentionCommand(
  attention: Pick<AttentionView, "attention_id" | "revision">,
  reason: string,
): OwnerCommand {
  return {
    operation: "attentions.withdraw",
    target: { type: "attention", id: attention.attention_id },
    expected_control_revision: attention.revision,
    reason,
    payload: {},
  };
}

export function deadlineCommand(
  id: string,
  revision: number | undefined,
  payload: PutIssueDeadlinePayload,
): OwnerCommand {
  return {
    operation: "issues.deadlines_put",
    target: { type: "deadline", id },
    ...(revision === undefined ? {} : { expected_control_revision: revision }),
    payload,
  };
}

export function cancelDeadlineCommand(
  id: string,
  revision: number,
  payload: CancelIssueDeadlinePayload,
): OwnerCommand {
  return {
    operation: "issues.deadlines_cancel",
    target: { type: "deadline", id },
    expected_control_revision: revision,
    payload,
  };
}

export function plannerCommand(
  id: string,
  revision: number | undefined,
  payload: PutPlannerEntryPayload,
): OwnerCommand {
  return {
    operation: "planner.entries_put",
    target: { type: "planner_entry", id },
    ...(revision === undefined ? {} : { expected_control_revision: revision }),
    payload,
  };
}
