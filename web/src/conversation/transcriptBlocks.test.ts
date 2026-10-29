import { expect, test } from "vitest";
import type {
  IssueConversationItem as ConversationItem,
  IssueTranscriptBlock as TranscriptBlock,
} from "../api/generated/api.ts";
import { buildTranscriptEntries } from "./transcriptBlocks.ts";

function item(seq: number, replyTo?: string, revision = 1): ConversationItem {
  return {
    seq,
    message_id: `message_${seq}`,
    kind: "message",
    author: replyTo ? "main_agent" : "owner",
    body: `text ${seq}`,
    created_at: new Date(Date.UTC(2026, 9, 28, 1, 0, seq)).toISOString(),
    ...(replyTo
      ? { reply_to: replyTo, assistant_revision: revision, assistant_state: "completed" }
      : { state: "replied" }),
  };
}
function block(
  trigger: ConversationItem,
  items: ConversationItem[] = [],
  more = false,
): TranscriptBlock {
  return { position: trigger.seq, trigger, items, more, next_seq: items.at(-1)?.seq ?? 0 };
}
function shown(blocks: TranscriptBlock[]): ConversationItem[] {
  return buildTranscriptEntries(blocks).flatMap((entry) =>
    entry.kind === "agent"
      ? entry.items
      : entry.kind === "owner" || entry.kind === "system"
        ? [entry.item]
        : [],
  );
}

test("a shared native reply appears once after the supplement it actually answers", () => {
  const original = item(1),
    early = item(2, original.message_id),
    supplement = item(3);
  const reply = item(4, supplement.message_id),
    next = item(5),
    final = item(6, next.message_id);
  const blocks = [
    block(original, [early, reply, final]),
    block(supplement, [reply, final]),
    block(next, [final]),
  ];
  expect(shown(blocks).map((row) => row.seq)).toEqual([1, 2, 3, 4, 5, 6]);
  expect(blocks[0]!.items).toHaveLength(3);
  expect(blocks[1]!.items).toHaveLength(2);
});

test("the newer shared streaming revision wins without moving it before its source", () => {
  const original = item(1),
    supplement = item(2);
  const latest = { ...item(3, supplement.message_id, 3), body: "completed answer" };
  const stale = {
    ...item(3, supplement.message_id, 1),
    body: "partial",
    assistant_state: "streaming" as const,
  };
  const rows = shown([block(original, [latest]), block(supplement, [stale])]);
  expect(rows.map((row) => row.seq)).toEqual([1, 2, 3]);
  expect(rows[2]).toMatchObject({ body: "completed answer", assistant_revision: 3 });
});

test("an actual loaded source owns a reply even outside its current output page", () => {
  const original = item(1),
    supplement = item(2),
    reply = item(10, supplement.message_id);
  expect(
    shown([
      block(original, [reply]),
      block(supplement, [item(3, supplement.message_id)], true),
    ]).map((row) => row.seq),
  ).toEqual([1, 2, 3, 10]);
});

test("missing actual sources use the latest visible association and keep pagination", () => {
  const original = item(1),
    supplement = item(2),
    absent = item(3),
    reply = item(4, absent.message_id);
  const blocks = [block(original, [reply], true), block(supplement, [reply])];
  expect(shown(blocks).map((row) => row.seq)).toEqual([1, 2, 4]);
  const continuations = buildTranscriptEntries(blocks).filter(
    (entry) => entry.kind === "block-more",
  );
  expect(continuations).toHaveLength(1);
  expect(continuations[0]?.block).toBe(blocks[0]);
  expect(shown([...blocks, block(absent, [reply])]).map((row) => row.seq)).toEqual([1, 2, 3, 4]);
});
