import { expect, test } from "vitest";
import type { IssueTranscriptBlock } from "../api/generated/api.ts";
import { showTranscriptBlock } from "./background.ts";
test("only an actually completed silent batch leaves the transcript", () => {
  const block: IssueTranscriptBlock = {
    position: 1,
    trigger: {
      seq: 1,
      kind: "message",
      message_id: "m",
      author: "system",
      created_at: "2026-01-01T00:00:00Z",
      background: {
        batch_id: "b",
        message_id: "m",
        status: "completed",
        source_count: 1,
        created_at: 1,
        checkin: { note: "waiting", created_at: 2 },
      },
    },
    items: [],
    more: false,
  };
  expect(showTranscriptBlock(block)).toBe(false);
  for (const status of ["frozen", "unknown", "failed", "interrupted"]) {
    block.trigger.background!.status = status;
    expect(showTranscriptBlock(block)).toBe(true);
  }
  block.trigger.background!.status = "completed";
  delete block.trigger.background!.checkin;
  expect(showTranscriptBlock(block)).toBe(true);
});
