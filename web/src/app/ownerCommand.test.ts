import { IDBFactory } from "fake-indexeddb";
import { expect, test } from "vitest";
import { NetworkError } from "../api/client.ts";
import type {
  APIResponse,
  AttentionView,
  Envelope,
  IssueView,
  RequestView,
} from "../api/generated/api.ts";
import { issueCommand } from "../issues/actions.ts";
import { attentionCommand, withdrawAttentionCommand } from "../attention/actions.ts";
import { runSupplementCommand } from "../conversation/actions.ts";
import { DurableOp } from "./durableOp.ts";
import { performOwnerCommand } from "./ownerCommand.ts";
import { runOp, type OpSender } from "./ops.ts";

test("unknown Issue control retains original request, revision and epoch across reload", async () => {
  const factory = new IDBFactory();
  const submitted: Envelope[] = [];
  let online = false;
  const sender: OpSender = {
    submit: async (e) => {
      submitted.push(e);
      if (!online) throw new NetworkError("lost response");
      return { operation: { request_id: e.request_id, status: "succeeded", result: {} } };
    },
    lookup: async () => null,
  };
  const original = issueCommand(
    { issue_id: "issue-one", revision: 4, assignment_epoch: 2 },
    "pause",
    { reason: "Wait for owner" },
  );
  let refreshed = 0;
  const journal = new DurableOp("issue:one", factory);
  expect((await performOwnerCommand(sender, journal, original, () => refreshed++)).kind).toBe(
    "unknown",
  );
  const pending = await journal.pending();
  expect(pending).toMatchObject({ ...original, request_id: submitted[0]!.request_id });
  expect(refreshed).toBe(0);
  // A later read observes a new definition/assignee; it must not rebase the pending command.
  const changed = issueCommand(
    { issue_id: "issue-one", revision: 5, assignment_epoch: 3 },
    "pause",
    {},
  );
  expect((await performOwnerCommand(sender, journal, changed)).kind).toBe("unknown");
  expect(submitted).toHaveLength(1);
  online = true;
  expect(
    (
      await performOwnerCommand(
        sender,
        new DurableOp("issue:one", factory),
        undefined,
        () => refreshed++,
      )
    ).kind,
  ).toBe("done");
  expect(submitted).toEqual([pending, pending]);
  expect(refreshed).toBe(1);
});

test("stale owner requests retain current Issue and Attention projections without re-submitting", async () => {
  const issue = { issue_id: "issue-one", revision: 5 } as IssueView;
  const attention = { attention_id: "attention-one", revision: 8 } as AttentionView;
  let submissions = 0;
  const sender: OpSender = {
    submit: async (e): Promise<APIResponse> => {
      submissions++;
      return {
        operation: {
          request_id: e.request_id,
          status: "failed",
          error: { code: "stale_revision", message: "Changed" },
        },
        current_issue: issue,
        current_attention: attention,
      };
    },
    lookup: async (): Promise<RequestView | null> => null,
  };
  const outcome = await runOp(
    sender,
    issueCommand({ issue_id: "issue-one", revision: 4, assignment_epoch: 2 }, "pause", {}),
  );
  expect(outcome).toMatchObject({
    kind: "stale",
    currentIssue: issue,
    currentAttention: attention,
  });
  expect(submissions).toBe(1);
});

test("original question answer and exact-run supplement use durable identities; withdrawal is not an answer", async () => {
  const factory = new IDBFactory();
  const sender: OpSender = {
    submit: async () => {
      throw new NetworkError("offline");
    },
    lookup: async () => null,
  };
  const answer = attentionCommand({ attention_id: "attention-one", revision: 5 }, "respond", {
    version: 3,
    option: "approved-original-option",
  });
  const question = new DurableOp("attention:one", factory);
  await performOwnerCommand(sender, question, answer);
  expect(await question.pending()).toMatchObject({
    operation: "attentions.respond",
    target: { type: "attention", id: "attention-one" },
    expected_control_revision: 5,
    payload: { version: 3, option: "approved-original-option" },
  });
  const run = new DurableOp("run:original", factory);
  await performOwnerCommand(
    sender,
    run,
    runSupplementCommand("run-original", 11, {
      task_id: "task-one",
      spec_version: 4,
      generation: 2,
      text: "Supplement this frozen run",
    }),
  );
  expect(await run.pending()).toMatchObject({
    target: { type: "run", id: "run-original" },
    expected_control_revision: 11,
    payload: { task_id: "task-one", spec_version: 4, generation: 2 },
  });
  const withdrawn = withdrawAttentionCommand(
    { attention_id: "attention-one", revision: 5 },
    "No longer needed",
  );
  expect(withdrawn).toMatchObject({
    operation: "attentions.withdraw",
    reason: "No longer needed",
    payload: {},
  });
});
