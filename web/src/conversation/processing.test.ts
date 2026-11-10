import { expect, test } from "vitest";
import type { IssueConversationItem } from "../api/generated/api.ts";
import { groupedProcessing } from "./processing.ts";

function reply(id: string, status: string, revision: number): IssueConversationItem {
  return {
    message_id: id,
    author: "main_agent",
    kind: "message",
    seq: revision,
    created_at: "2026-01-01T00:00:00Z",
    associations: {
      message_id: id,
      revision: 1,
      content_revision: 1,
      disposition: "linked",
      facts_reviewed: false,
      owner_locked: false,
      items: [],
      processing: { logical_turn_id: "turn", status, revision, reason: "Receipt recovery" },
    },
  };
}

test("one recovery status represents the whole group of replies", () => {
  const states = groupedProcessing([
    reply("first", "recovering", 2),
    reply("last", "recovering", 2),
  ]);
  expect([...states.keys()]).toEqual(["last"]);
  expect(states.get("last")?.status).toBe("recovering");
});

test("a later terminal receipt removes an older recovery state even with stale row ordering", () => {
  const states = groupedProcessing([
    reply("settled", "completed", 3),
    reply("older", "recovering", 2),
  ]);
  expect([...states.keys()]).toEqual(["older"]);
  expect(states.get("older")?.status).toBe("completed");
});
