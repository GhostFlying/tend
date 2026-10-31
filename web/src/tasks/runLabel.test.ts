import { expect, test } from "vitest";
import type { RunView } from "../api/generated/api.ts";
import { runLabel } from "./runLabel.ts";

test("execution ordinals distinguish reset generations across roles and requirements", () => {
  const runs = [
    { run_id: "a", role: "implement", spec_version: 1, generation: 1 },
    { run_id: "b", role: "accept", spec_version: 1, generation: 1 },
    { run_id: "c", role: "accept", spec_version: 1, generation: 1 },
    { run_id: "d", role: "implement", spec_version: 2, generation: 1 },
  ] as RunView[];
  expect(runs.map((run) => runLabel(runs, run))).toEqual([
    "第 1 次执行 · 实施 · 第 1 版要求",
    "第 2 次执行 · 独立验收 · 第 1 版要求",
    "第 3 次执行 · 独立验收 · 第 1 版要求",
    "第 4 次执行 · 实施 · 第 2 版要求",
  ]);
  expect(runLabel([...runs, { ...runs[0]!, run_id: "e" }], runs[2]!)).toBe(
    runLabel(runs, runs[2]!),
  );
});
