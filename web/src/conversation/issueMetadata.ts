import type {
  IssueMessageAssociations,
  SetIssueMessageAssociationsPayload,
} from "../api/generated/api.ts";
import type { OwnerCommand } from "../app/ownerCommand.ts";
import type { Reader } from "../live/data.ts";
import { conversationReader } from "./api.ts";

/** Reuse the existing reader/abort lifecycle; this adds no cache or queue. */
export function issueConversationReader(api: Reader, conversationID: string): Reader {
  const scoped = conversationReader(api, conversationID);
  return {
    get<T>(path: string, signal?: AbortSignal): Promise<T> {
      const url = new URL(path, "https://tend.invalid");
      if (
        url.pathname === "/api/v1/conversation" ||
        url.pathname === "/api/v1/conversation/queue" ||
        url.pathname === "/api/v1/conversation/transcript" ||
        /^\/api\/v1\/conversation\/blocks\/[^/]+$/.test(url.pathname) ||
        url.pathname === "/api/v1/agent-conversations" ||
        /^\/api\/v1\/agent-conversations\/[^/]+$/.test(url.pathname) ||
        /^\/api\/v1\/messages\/[^/]+\/associations$/.test(url.pathname)
      ) {
        url.searchParams.set("projection", "issues");
        path = url.pathname + url.search;
      }
      return scoped.get<T>(path, signal);
    },
  };
}

/** Keep exact content/association versions in the existing durable Envelope. */
export function issueAssociationCommand(
  source: Pick<IssueMessageAssociations, "message_id" | "revision" | "content_revision">,
  disposition: SetIssueMessageAssociationsPayload["disposition"],
  issueIDs: string[],
): OwnerCommand {
  return {
    operation: "conversation.set_issue_associations",
    target: { type: "message", id: source.message_id },
    expected_control_revision: source.revision,
    payload: {
      disposition,
      content_revision: source.content_revision,
      issue_ids: issueIDs,
    } satisfies SetIssueMessageAssociationsPayload,
  };
}
