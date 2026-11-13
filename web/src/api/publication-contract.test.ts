import { expect, expectTypeOf, test } from "vitest";
import type { MessagingPublication, UpdateIssueDeliveryPayload } from "./generated/api";

test("generated publication types accept ordinary text and attachment requests", () => {
  const files: string[] = ["uploaded-original"];
  const requests: MessagingPublication[] = [
    { message_id: "text", body: "Current progress" },
    { message_id: "file", attachments: files },
    { message_id: "result", body: "Result", attachments: files, purpose: "final" },
    { message_id: "progress", body: "Progress", purpose: "progress" },
    { message_id: "unmarked", body: "Ordinary message", purpose: "" },
  ];
  expect(requests.map((request) => request.message_id)).toEqual([
    "text",
    "file",
    "result",
    "progress",
    "unmarked",
  ]);
  expectTypeOf<MessagingPublication["body"]>().toEqualTypeOf<string | undefined>();
  expectTypeOf<MessagingPublication["attachments"]>().toEqualTypeOf<string[] | undefined>();
});

test("generated delivery types accept every action and numeric history selectors", () => {
  const common = { expected_assignment_epoch: 1, reason: "Reconcile the actual result" };
  const requests: UpdateIssueDeliveryPayload[] = [
    { ...common, action: "submit_result", result: { mounts: null } },
    { ...common, action: "prepare", task_id: "original" },
    { ...common, action: "resume", task_id: "original" },
    {
      ...common,
      action: "bind",
      task_id: "original",
      execution_task_id: "original",
      expected_task_control_revision: 1,
    },
    { ...common, action: "report", task_id: "original" },
    { ...common, action: "end", task_id: "original" },
    {
      ...common,
      action: "complete",
      task_id: "original",
      execution_task_id: "original",
      execution_run_id: "retained",
      execution_duty_revision: 2,
    },
  ];
  expect(requests.map((request) => request.action)).toEqual([
    "submit_result",
    "prepare",
    "resume",
    "bind",
    "report",
    "end",
    "complete",
  ]);
  expectTypeOf<UpdateIssueDeliveryPayload["reason"]>().toEqualTypeOf<string>();
  expectTypeOf<UpdateIssueDeliveryPayload["execution_duty_revision"]>().toEqualTypeOf<
    number | undefined
  >();
});
