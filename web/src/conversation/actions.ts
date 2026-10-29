import type {
  CreateConsultationPayload,
  PromoteConsultationPayload,
  InjectSourcePayload,
  SendRunMessagePayload,
} from "../api/generated/api.ts";
import type { OwnerCommand } from "../app/ownerCommand.ts";

export function consultationCommand(payload: CreateConsultationPayload): OwnerCommand {
  return { operation: "conversation.consult", target: { type: "conversation" }, payload };
}

// Sending an execution request in a consultation still reaches its existing
// agent delegation tools. The client does not classify text or require a button.
export function promoteConsultationCommand(
  conversationID: string,
  payload: PromoteConsultationPayload,
): OwnerCommand {
  return {
    operation: "conversation.promote",
    target: { type: "conversation", id: conversationID },
    payload,
  };
}

export function injectSourceCommand(payload: InjectSourcePayload): OwnerCommand {
  return { operation: "conversation.inject_source", target: { type: "message" }, payload };
}

/** This exact run and frozen spec/generation never follow a newer task run. */
export function runSupplementCommand(
  runID: string,
  controlRevision: number,
  payload: SendRunMessagePayload,
): OwnerCommand {
  return {
    operation: "runs.send_message",
    target: { type: "run", id: runID },
    expected_control_revision: controlRevision,
    payload,
  };
}
