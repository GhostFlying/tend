import { expect, test } from "vitest";
import type { IssueAcceptancePolicy } from "../api/generated/api.ts";
import { resultStatus, type ResultCardValue } from "./resultStatus.ts";

const policy: IssueAcceptancePolicy = {
  required: false,
  reason: "The creator selected a bounded report",
  creator: { kind: "owner", owner_subject: "owner" },
  decided_by: { kind: "owner", owner_subject: "owner" },
  request_id: "req_original_policy",
  issue_revision: 1,
};
const result: ResultCardValue = {
  artifact: { artifact_id: "art_report", run_id: "run_report", ref: {} },
  task_id: "task_report",
  title: "Findings",
  acceptances: [],
};

test("a frozen waived current completion is not a pending independent review", () => {
  expect(resultStatus({ ...result, acceptance_policy: policy, current_completion: true })).toBe(
    "已完成 · 本轮不要求独立验收",
  );
  expect(resultStatus({ ...result, acceptance_policy: policy, current_completion: false })).toBe(
    "本轮不要求独立验收 · 非当前完成结果",
  );
});

test("unspecified or required policy retains pending review and real verdicts retain priority", () => {
  expect(resultStatus(result)).toBe("待独立验收");
  expect(resultStatus({ ...result, acceptance_policy: { ...policy, required: true } })).toBe(
    "待独立验收",
  );
  for (const [verdict, expected] of [
    ["passed", "独立验收通过"],
    ["changes_required", "独立验收需修改"],
    ["blocked", "验收受阻"],
    ["inconclusive", "验收结论待定"],
  ] as const) {
    expect(
      resultStatus({
        ...result,
        acceptance_policy: policy,
        acceptances: [
          {
            acceptance_id: "acc_real",
            artifact_id: "art_report",
            spec_version: 1,
            issues: [],
            verdict,
          },
        ],
      }),
    ).toBe(expected);
  }
});
