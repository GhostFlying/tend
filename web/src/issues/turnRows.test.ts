import { expect, test } from "vitest";
import type { IssueTranscriptBlock } from "../api/generated/api.ts";
import { buildTranscriptEntries } from "../conversation/transcriptBlocks.ts";
import { activityTurnRows } from "./turnRows.ts";

const trigger = {
  seq: 1,
  message_id: "original-trigger",
  author: "owner",
  kind: "message",
  created_at: "2026-01-01T00:00:00Z",
  state: "processing" as const,
};
const block: IssueTranscriptBlock = { position: 1, trigger, items: [], more: false };
const first = {
  seq: 2,
  message_id: "first-tool",
  author: "main_agent",
  kind: "message",
  created_at: trigger.created_at,
  reply_to: trigger.message_id,
  tool: {
    name: "read",
    status: "running" as const,
    turn_id: "original-turn",
    updated_at: trigger.created_at,
  },
};

test("the original trigger retains its reply identity from empty process to tools and prose", () => {
  const reply = (value: IssueTranscriptBlock) =>
    activityTurnRows(buildTranscriptEntries([value]), value).filter((row) => row.kind === "agent");
  const empty = reply(block);
  const tool = reply({ ...block, items: [first] });
  const prose = reply({
    ...block,
    items: [
      first,
      {
        message_id: "reply",
        seq: 3,
        kind: "message",
        author: "main_agent",
        reply_to: trigger.message_id,
        created_at: trigger.created_at,
        body: "Public reply",
      },
    ],
  });
  expect(empty).toHaveLength(1);
  expect(tool[0]?.key).toBe(empty[0]?.key);
  expect(prose[0]?.key).toBe(empty[0]?.key);
  expect(tool[0]?.items).toEqual([first]);
  expect(activityTurnRows([], { ...block, trigger: { ...trigger, state: "replied" } })).toEqual([]);
});

test("a placeholder precedes continuation and does not consume later groups' identities", () => {
  const more = { kind: "block-more" as const, key: "more", block };
  expect(activityTurnRows([more], block).map((row) => row.kind)).toEqual(["agent", "block-more"]);
  const one = { kind: "agent" as const, key: "first", replyTo: trigger.message_id, items: [first] };
  const two = { ...one, key: "second" };
  const rows = activityTurnRows([one, two, more], block);
  expect(rows[1]).toBe(two);
  expect(rows[2]).toBe(more);
  expect(rows[0]?.key).not.toBe(one.key);
  expect(
    activityTurnRows([], { ...block, trigger: { ...trigger, message_id: "other" } })[0]?.key,
  ).not.toBe(rows[0]?.key);
});

test("public process feeds the live trace without publishing prose or duplicating a settled tool", () => {
  const value: IssueTranscriptBlock = {
    ...block,
    process: { items: [first], more: false, started_at: 1000, ended_at: 9000 },
  };
  const rows = activityTurnRows(buildTranscriptEntries([value]), value);
  const live = rows.find((row) => row.kind === "agent");
  expect(live?.items).toEqual([first]);
  expect(live?.startedAt).toBe(1000);
  expect(live?.endedAt).toBe(9000);
  const settled = { ...value, items: [first], trigger: { ...trigger, state: "replied" as const } };
  expect(
    activityTurnRows(buildTranscriptEntries([settled]), settled).find((row) => row.kind === "agent")
      ?.items,
  ).toEqual([first]);
  expect(
    buildTranscriptEntries([{ ...value, process: { ...value.process!, more: true, next: 7 } }]).at(
      -1,
    )?.kind,
  ).toBe("block-more");
});

test("earlier deferred tools keep arrival order ahead of later callback transcript rows", () => {
  const earlier = { ...first, message_id: "early", seq: 91 };
  const callback = { ...first, message_id: "callback", seq: 50 };
  const value: IssueTranscriptBlock = {
    ...block,
    items: [callback],
    process: { items: [earlier, { ...callback, seq: 92 }], more: false },
  };
  const rows = activityTurnRows(buildTranscriptEntries([value]), value);
  expect(rows.find((row) => row.kind === "agent")?.items.map((item) => item.message_id)).toEqual([
    "early",
    "callback",
  ]);
});
