import type { IssueResult } from "../api/generated/api.ts";

export type ResultCardValue = Pick<
  IssueResult,
  | "artifact"
  | "task_id"
  | "title"
  | "acceptances"
  | "acceptance_policy"
  | "current_completion"
  | "result_complete"
  | "reviewer_artifact_id"
  | "independent_acceptance_id"
>;

export function resultStatus(result: ResultCardValue): string {
  if (result.result_complete && result.reviewer_artifact_id) return "独立审阅通过";
  if (result.result_complete && result.independent_acceptance_id) return "独立验收通过";
  const verdict = result.acceptances?.at(-1)?.verdict;
  if (verdict === "passed") return "独立验收通过";
  if (verdict === "changes_required") return "独立验收需修改";
  if (verdict === "blocked") return "验收受阻";
  if (verdict === "inconclusive") return "验收结论待定";
  if (result.acceptance_policy?.required === false)
    return result.current_completion || result.result_complete
      ? "已完成 · 本轮不要求独立验收"
      : "本轮不要求独立验收 · 非当前完成结果";
  return "待独立验收";
}
