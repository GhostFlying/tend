import type { RunView } from "../api/generated/api.ts";

/** Runs are ordered by creation; role/spec generations are not task-wide ordinals. */
export function runLabel(runs: readonly RunView[], run: RunView): string {
  const ordinal = runs.findIndex((candidate) => candidate.run_id === run.run_id) + 1;
  const role = run.role === "issue_manager" ? "协调" : run.role === "accept" ? "独立验收" : "实施";
  return `第 ${ordinal} 次执行 · ${role}${run.spec_version ? ` · 第 ${run.spec_version} 版要求` : ""}`;
}
