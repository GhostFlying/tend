import { expect, test } from "vitest";
import type { IssueConversationItem } from "../api/generated/api.ts";
import {
  groupToolItems,
  flatTurnRows,
  settleToolGroups,
  toolGroups,
  toolLabel,
  toolLabels,
} from "./toolLabels.ts";

const item = (id: number, status?: "completed" | "running" | "failed"): IssueConversationItem => ({
  seq: id,
  message_id: String(id),
  kind: "message",
  author: "main_agent",
  created_at: "2026-10-31T00:00:00Z",
  ...(status
    ? {
        tool: {
          name: "issues_read",
          status,
          input: "{}",
          turn_id: "turn",
          updated_at: "2026-10-31T00:00:00Z",
        },
      }
    : { body: "reply" }),
});

test("tool actions have Chinese labels, dotted aliases and readable unknown names", () => {
  for (const [name, label] of Object.entries(toolLabels)) {
    expect(toolLabel(name)).toBe(label);
    expect(/[\p{Script=Han}]/u.test(label)).toBe(true);
  }
  expect(toolLabel("issues.read")).toBe("查看 Issue");
  expect(toolLabel("some_new_tool")).toBe("some new tool");
});

test("consecutive completed calls group while messages and unfinished calls remain boundaries", () => {
  const items = [
    item(1, "completed"),
    item(2, "completed"),
    item(3, "running"),
    item(4, "failed"),
    item(5),
    item(6, "completed"),
  ];
  expect(groupToolItems(items).map((g) => g.map((i) => i.seq))).toEqual([
    [1, 2],
    [3],
    [4],
    [5],
    [6],
  ]);
  expect(groupToolItems([])).toEqual([]);
  const otherTurn = {
    ...item(2, "completed"),
    tool: { ...item(2, "completed").tool!, turn_id: "other" },
  };
  expect(groupToolItems([item(1, "completed"), otherTurn])).toHaveLength(2);
  const external = { ...item(7, "completed"), author: "external" };
  expect(groupToolItems([item(1, "completed"), external, item(2, "completed")])).toHaveLength(3);
  const choice = {
    ...item(1, "completed"),
    choices: [{ choice_id: "c" }],
  } as IssueConversationItem;
  expect(groupToolItems([choice, item(2, "completed")])).toHaveLength(2);
});

test("flat content keys never change when grouping forms, splits or backfills", () => {
  const choice = { ...item(2), choices: [{ choice_id: "c" }] } as IssueConversationItem;
  const stages = [
    [item(1, "completed"), item(3, "running"), choice],
    [item(1, "completed"), item(3, "completed"), choice],
    [item(1, "completed"), choice, item(3, "completed")],
    [item(0), item(1, "completed"), choice, item(3, "completed"), item(4, "completed")],
  ];
  for (const items of stages) {
    const states = settleToolGroups(toolGroups(items), new Map(), new Set());
    const rows = flatTurnRows(items, states, new Set());
    expect(rows.filter((row) => row.kind === "item").map((row) => row.key)).toEqual(
      items.map((row) => `item:${row.message_id}`),
    );
    expect(rows.find((row) => row.key === "item:2")).toMatchObject({ kind: "item", hidden: false });
    expect(new Set(rows.map((row) => row.key)).size).toBe(rows.length);
  }
  expect(toolGroups(stages[2]!)).toEqual([]);
});

test("expanded and focused tool rows remain visible as their run forms", () => {
  const initial = [item(1, "completed"), item(2, "running")];
  const previous = settleToolGroups(toolGroups(initial), new Map(), new Set());
  const completed = [item(1, "completed"), item(2, "completed")];
  const expanded = new Set(["1"]);
  expect(
    flatTurnRows(completed, previous, expanded)
      .filter((row) => row.kind === "item")
      .every((row) => !row.hidden),
  ).toBe(true);
  const focused = settleToolGroups(toolGroups(completed), previous, new Set(), "2");
  expect(focused.get("1")?.open).toBe(true);
  expect(
    flatTurnRows(completed, focused, new Set())
      .filter((row) => row.kind === "item")
      .every((row) => !row.hidden),
  ).toBe(true);
});

test("backfills split closed groups without hiding replies or choices and carry open membership", () => {
  const initial = [
    item(1, "completed"),
    item(4, "completed"),
    item(5),
    item(6, "completed"),
    item(7, "completed"),
  ];
  const previous = settleToolGroups(toolGroups(initial), new Map(), new Set(["7"]));
  const choice = { ...item(3), choices: [{ choice_id: "c" }] } as IssueConversationItem;
  const full = [
    item(1, "completed"),
    item(2),
    choice,
    item(4, "completed"),
    item(5),
    item(6, "completed"),
    item(7, "completed"),
  ];
  const states = settleToolGroups(toolGroups(full), previous, new Set());
  const rows = flatTurnRows(full, states, new Set());
  expect(rows.filter((row) => row.kind === "item").map((row) => row.item.message_id)).toEqual(
    full.map((row) => row.message_id),
  );
  expect(rows.every((row) => row.kind !== "item" || !!row.item.tool || !row.hidden)).toBe(true);
  const prepend = [item(0, "completed"), item(1, "completed"), item(4, "completed")];
  const open = settleToolGroups(toolGroups(initial), new Map(), new Set(["1"]));
  const carried = settleToolGroups(toolGroups(prepend), open, new Set());
  expect(carried.get("0")?.open).toBe(true);
});

test("successive pagination backfills preserve flat item order and unique keys", () => {
  const stages = [
    [1, 50, 61],
    [1, ...Array.from({ length: 12 }, (_, index) => index + 50)],
    Array.from({ length: 61 }, (_, index) => index + 1),
  ];
  for (const seqs of stages) {
    const items = seqs.map((seq) => item(seq, "completed"));
    const rows = flatTurnRows(items, new Map(), new Set(["61"]));
    expect(rows.filter((row) => row.kind === "item").map((row) => row.item.seq)).toEqual(seqs);
    expect(new Set(rows.map((row) => row.key)).size).toBe(rows.length);
  }
});
