import type { PutAgentCategoryPayload } from "../api/generated/api.ts";
import type { OwnerCommand } from "../app/ownerCommand.ts";

/** Capture the category revision; zero explicitly creates a new category. */
export function putCategoryCommand(
  id: string,
  revision: number,
  payload: PutAgentCategoryPayload,
): OwnerCommand {
  return {
    operation: "categories.put",
    target: { type: "category", id },
    expected_control_revision: revision,
    payload,
  };
}
