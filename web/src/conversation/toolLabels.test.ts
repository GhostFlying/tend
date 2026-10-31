import { expect, test } from "vitest";
import type { IssueConversationItem } from "../api/generated/api.ts";
import { groupToolItems, retainToolGroups, toolLabel, toolLabels } from "./toolLabels.ts";

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

test("deferred groups insert a transcript backfill before the newer streamed reply", () => {
  const firstPage = Array.from({ length: 50 }, (_, index) => item(index + 1, "completed"));
  const initial = groupToolItems(firstPage).map((group) => group.map((row) => row.message_id));
  const streamed = retainToolGroups(initial, [...firstPage, item(61)]);
  expect(streamed).toEqual([initial[0], ["61"]]);
  const full = Array.from({ length: 61 }, (_, index) => item(index + 1, "completed"));
  const backfilled = retainToolGroups(streamed, full);
  expect(backfilled.flat()).toEqual(full.map((row) => row.message_id));
  expect(backfilled[0]).toEqual(initial[0]);
  expect(backfilled.at(-1)).toEqual(["61"]);
  expect(retainToolGroups(backfilled, full)).toEqual(backfilled);
});

test("backfill inside an expanded group preserves its wrapper and existing child keys", () => {
  const groups = [["1", "3"], ["5"]];
  const full = [item(1), item(2), item(3), item(4), item(5)];
  const retained = retainToolGroups(groups, full);
  expect(retained).toEqual([["1", "2", "3"], ["4"], ["5"]]);
  expect(retained.map((group) => group[0])).toEqual(["1", "4", "5"]);
  expect(retained.flat()).toEqual(full.map((row) => row.message_id));
  expect(retainToolGroups([], [])).toEqual([]);
});
