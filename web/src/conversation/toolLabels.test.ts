import { expect, test } from "vitest";
import { toolLabel, toolLabels } from "./toolLabels.ts";

test("tool actions have Chinese labels, dotted aliases and readable unknown names", () => {
  for (const [name, label] of Object.entries(toolLabels)) {
    expect(toolLabel(name)).toBe(label);
    expect(/[\p{Script=Han}]/u.test(label)).toBe(true);
  }
  expect(toolLabel("issues.read")).toBe("查看 Issue");
  expect(toolLabel("some_new_tool")).toBe("some new tool");
});
