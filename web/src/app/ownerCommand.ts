// Non-React callers use the same persisted Envelope and recovery as controls.
import type { Envelope } from "../api/generated/api.ts";
import { newRequestId } from "../api/ids.ts";
import type { DurableOp } from "./durableOp.ts";
import type { OpOutcome, OpSender } from "./ops.ts";

export type OwnerCommand = Omit<Envelope, "request_id">;

export async function performOwnerCommand(
  sender: OpSender,
  journal: DurableOp,
  command?: OwnerCommand,
  changed: () => void = () => {},
): Promise<OpOutcome> {
  const outcome = command
    ? await journal.submit(sender, { ...command, request_id: newRequestId(Date.now()) })
    : await journal.reconcile(sender);
  // Refresh projections, but never rebase or replace the persisted command.
  if (outcome.kind === "done" || outcome.kind === "stale") changed();
  return outcome;
}
