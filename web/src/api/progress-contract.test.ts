import { expect, expectTypeOf, test } from "vitest";
import type { OwnerTaskBase, UpdateIssueProgressPayload } from "./generated/api";

test("generated progress types accept runtime strings and explicit clearing", () => {
  const summary: string = "界".repeat(500);
  const nextStep: string = "🙂".repeat(240);
  const updates: UpdateIssueProgressPayload[] = [
    { expected_assignment_epoch: 1, summary, next_step: nextStep },
    { expected_assignment_epoch: 1, summary: "", next_step: "" },
  ];
  expect(updates[0]?.summary).toBe(summary);
  expect(updates[1]?.summary).toBe("");
  expectTypeOf<UpdateIssueProgressPayload["summary"]>().toEqualTypeOf<string>();
  expectTypeOf<UpdateIssueProgressPayload["next_step"]>().toEqualTypeOf<string>();
});

test("generated workspace base types preserve optional selectors", () => {
  const choices: OwnerTaskBase[] = [{}, { branch: "main" }, { commit: "a".repeat(40) }];
  expect(choices).toHaveLength(3);
  expectTypeOf<OwnerTaskBase["branch"]>().toEqualTypeOf<string | undefined>();
  expectTypeOf<OwnerTaskBase["commit"]>().toEqualTypeOf<string | undefined>();
});
