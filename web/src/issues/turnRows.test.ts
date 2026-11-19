import { expect, test } from "vitest";
import type { IssueTranscriptBlock } from "../api/generated/api.ts";
import { buildTranscriptEntries } from "../conversation/transcriptBlocks.ts";
import { conversationStatus } from "../ui/activityStatus.ts";
import { activityTurnRows } from "./turnRows.ts";

const undecidedTrigger = {
  seq: 1,
  message_id: "original-trigger",
  author: "owner",
  kind: "message",
  created_at: "2026-01-01T00:00:00Z",
};
const trigger = { ...undecidedTrigger, state: "processing" as const };
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

test("readable tool metadata shows running or uncertain status without tool details", () => {
  const process = { items: [], more: false, has_tools: true, started_at: 1000 };
  const collect = (value: IssueTranscriptBlock) =>
    activityTurnRows([], value, true).find((row) => row.kind === "agent")!;
  const running = collect({
    ...block,
    trigger: undecidedTrigger,
    process: { ...process, state: "processing", active_tool: "exec_command" },
  });
  expect(running.items).toEqual([]);
  expect(
    conversationStatus(running.items, running.state, running.startedAt, null, {
      activeTool: "exec_command",
    }),
  ).toMatchObject({ active: true, label: "正在执行命令", startedAt: 1000 });
  const unknown = collect({ ...block, trigger: undecidedTrigger, process });
  expect(conversationStatus(unknown.items, unknown.state)).toMatchObject({
    active: false,
    label: "结果待确认",
  });
});

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
  const paged = { ...value, process: { ...value.process!, more: true, next: 7 } };
  expect(buildTranscriptEntries([paged]).some((row) => row.kind === "block-more")).toBe(false);
  expect(activityTurnRows(buildTranscriptEntries([paged]), paged).at(-1)?.kind).toBe("block-more");
  const both = { ...paged, more: true, next_seq: 5 };
  expect(
    activityTurnRows(buildTranscriptEntries([both]), both).filter(
      (row) => row.kind === "block-more",
    ),
  ).toHaveLength(1);
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

test("external trigger stands apart from the Agent and its public process", () => {
  const source = {
    ...block,
    trigger: { ...trigger, author: "external" },
    items: [{ ...first, kind: "tool_use" }],
  };
  const rows = activityTurnRows(buildTranscriptEntries([source]), source);
  expect(rows.filter((row) => row.kind === "source").map((row) => row.item.message_id)).toEqual([
    trigger.message_id,
  ]);
  expect(
    rows
      .filter((row) => row.kind === "agent")
      .flatMap((row) => row.items.map((item) => item.message_id)),
  ).toEqual([first.message_id]);
});

test("an urgent receipt stays separate from the public process and keeps actual replies", () => {
  const value: IssueTranscriptBlock = {
    ...block,
    trigger: {
      ...trigger,
      author: "agent",
      issue_owner_steer: {
        assignment_epoch: 1,
        issue_id: "issue",
        message_id: trigger.message_id,
        run_id: "owner-run",
        sender: { kind: "main_agent", subject: "main" },
        source_message_id: "owner-original",
        source_revision: 1,
        status: "accepted",
        task_id: "owner-task",
        thread_id: "owner-thread",
        turn_id: "owner-turn",
      },
    },
    process: { items: [first], more: false },
  };
  const receipt = activityTurnRows(buildTranscriptEntries([value]), value);
  expect(receipt.map((row) => row.kind)).toEqual(["divider", "urgent"]);

  const reply = {
    seq: 3,
    message_id: "public-reply",
    author: "main_agent",
    kind: "message",
    created_at: trigger.created_at,
    reply_to: trigger.message_id,
    body: "Public reply",
  };
  const replied = { ...value, items: [reply] };
  const rows = activityTurnRows(buildTranscriptEntries([replied]), replied);
  expect(rows.map((row) => row.kind)).toEqual(["divider", "urgent", "agent"]);
  expect(rows.find((row) => row.kind === "agent")?.items).toEqual([reply]);
});
