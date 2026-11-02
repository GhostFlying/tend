import { expect, test } from "vitest";
import { readFileSync } from "node:fs";
import { layoutGroups } from "./layout-groups.ts";

test("CI groups cover every mandatory browser regression exactly once", () => {
  const { scripts } = JSON.parse(
    readFileSync(new URL("../package.json", import.meta.url), "utf8"),
  ) as { scripts: Record<string, string> };
  const expected = Object.keys(scripts).filter(
    (name) => name.startsWith("test:") && name !== "test:copy-layout",
  );
  const actual = Object.values(layoutGroups).flat();
  expect(actual).toHaveLength(13);
  expect(new Set(actual).size).toBe(actual.length);
  expect(actual.toSorted()).toEqual(expected.toSorted());
});
