import { expect, test } from "vitest";
import type { IssueConversationItem } from "../api/generated/api.ts";
import {
  groupToolItems,
  retainToolGroups,
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

test("deferred groups insert a transcript backfill before the newer streamed reply", () => {
  const firstPage = Array.from({ length: 50 }, (_, index) => item(index + 1, "completed"));
  const initial = toolGroups(firstPage);
  const streamed = retainToolGroups(initial, [...firstPage, item(61)]);
  const full = Array.from({ length: 61 }, (_, index) => item(index + 1, "completed"));
  const backfilled = retainToolGroups(streamed, full);
  expect(backfilled.flatMap((group) => group.ids)).toEqual(full.map((row) => row.message_id));
  expect(backfilled[0]).toEqual(initial[0]);
  expect(backfilled.at(-1)?.ids).toEqual(["61"]);
  expect(retainToolGroups(backfilled, full)).toEqual(backfilled);
});

test("non-tool backfills split closed groups and remain visible while another group is open", () => {
  const initial = toolGroups([
    item(1, "completed"),
    item(4, "completed"),
    item(5),
    item(6, "completed"),
    item(7, "completed"),
  ]);
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
  const retained = retainToolGroups(initial, full, new Map([[initial[2]!.key, "7"]]));
  expect(retained.flatMap((group) => group.ids)).toEqual(full.map((row) => row.message_id));
  expect(retained.filter((group) => group.grouped).map((group) => group.ids)).toEqual([["6", "7"]]);
  expect(retained.find((group) => group.ids.includes("2"))?.grouped).toBe(false);
  expect(retained.find((group) => group.ids.includes("3"))?.grouped).toBe(false);
});

test("the tool segment containing the reading row retains the original disclosure key", () => {
  const initial = toolGroups([item(1, "completed"), item(3, "completed")]);
  const full = [item(1, "completed"), item(2), item(3, "completed")];
  const retained = retainToolGroups(initial, full, new Map([[initial[0]!.key, "3"]]));
  expect(retained.map((group) => group.ids)).toEqual([["1"], ["2"], ["3"]]);
  expect(retained[2]!.key).toBe(initial[0]!.key);
  expect(retained[2]!.grouped).toBe(true);
  expect(retained[0]!.grouped).toBe(false);
  expect(new Set(retained.map((group) => group.key)).size).toBe(retained.length);
});
