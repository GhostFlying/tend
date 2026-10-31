import { expect, test } from "vitest";
import type { IssueConversationItem } from "../api/generated/api.ts";
import { groupToolItems, toolLabel, toolLabels } from "./toolLabels.ts";

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
