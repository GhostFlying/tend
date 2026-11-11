import { IDBFactory } from "fake-indexeddb";
import { expect, test } from "vitest";
import { ApiError, NetworkError } from "../api/client.ts";
import type { APIResponse, Envelope } from "../api/generated/api.ts";
import type { OpSender } from "./ops.ts";
import { DurableOp } from "./durableOp.ts";

const envelope: Envelope = {
  request_id: "req_original",
  operation: "issues.pause",
  target: { type: "issue", id: "issue-a" },
  expected_control_revision: 7,
  payload: { expected_assignment_epoch: 2 },
};
class Sender implements OpSender {
  submitted: Envelope[] = [];
  paths: string[] = [];
  current: unknown = { issue_id: "issue-a", revision: 8 };
  offline = true;
  async submit(e: Envelope): Promise<APIResponse> {
    this.submitted.push(e);
    throw new NetworkError("lost response");
  }
  async get<T>(path: string): Promise<T> {
    this.paths.push(path);
    if (this.offline) throw new NetworkError("offline");
    return this.current as T;
  }
}

test("reload reads the actual object without replay and returns its current version", async () => {
  const factory = new IDBFactory(),
    sender = new Sender();
  const first = new DurableOp("issue:issue-a", factory);
  expect((await first.submit(sender, envelope)).kind).toBe("unknown");
  const recovered = new DurableOp("issue:issue-a", factory);
  expect(await recovered.pending()).toEqual(envelope);
  sender.offline = false;
  expect(await recovered.reconcile(sender)).toMatchObject({
    kind: "stale",
    currentIssue: { issue_id: "issue-a", revision: 8 },
  });
  expect(sender.submitted).toHaveLength(1);
  expect(sender.paths).toEqual(["/api/v1/issues/issue-a", "/api/v1/issues/issue-a"]);
  expect(await recovered.pending()).toBeUndefined();
});

test("unknown external operation and missing business object never authorize replay", async () => {
  const journal = new DurableOp("compact:background-a", new IDBFactory()),
    sender = new Sender();
  const original = {
    ...envelope,
    operation: "conversation.compact",
    target: { type: "conversation" },
  };
  await journal.submit(sender, original);
  sender.offline = false;
  expect((await journal.reconcile(sender)).kind).toBe("unknown");
  expect(sender.submitted).toEqual([original]);
  expect(sender.paths).toEqual([]);
  const missing = new DurableOp("create:missing", new IDBFactory());
  const create = { ...envelope, operation: "issues.create" };
  const absent: OpSender = {
    submit: async () => {
      throw new NetworkError("lost");
    },
    get: async () => {
      throw new ApiError(404, { code: "not_found", message: "missing object" });
    },
  };
  expect((await missing.submit(absent, create)).kind).toBe("unknown");
  expect((await missing.reconcile(absent)).kind).toBe("unknown");
  expect(await missing.pending()).toEqual(create);
});

test("a creation reserves its business ID before transport and restores by that object", async () => {
  const sender = new Sender(),
    journal = new DurableOp("create:new", new IDBFactory());
  await journal.submit(sender, {
    ...envelope,
    operation: "issues.create",
    target: { type: "issue" },
  });
  const original = await journal.pending();
  expect(original?.target.id).toMatch(/^wi_[0-9A-Z]{26}$/);
  expect(sender.submitted[0]).toEqual(original);
  sender.offline = false;
  sender.current = { issue_id: original!.target.id, revision: 1 };
  expect((await journal.reconcile(sender)).kind).toBe("done");
  expect(sender.paths.at(-1)).toBe(`/api/v1/issues/${original!.target.id}`);
  expect(sender.submitted).toHaveLength(1);
});

test("concurrent tabs cannot replace a pending target, revision or payload", async () => {
  const factory = new IDBFactory(),
    sender = new Sender();
  const a = new DurableOp("issue:a", factory),
    b = new DurableOp("issue:a", factory);
  const other = { ...envelope, request_id: "req_other", payload: { expected_assignment_epoch: 3 } };
  await Promise.all([a.submit(sender, envelope), b.submit(sender, other)]);
  expect(sender.submitted).toHaveLength(1);
  expect(await a.pending()).toEqual(sender.submitted[0]);
  expect(await b.pending()).toEqual(sender.submitted[0]);
  expect(await new DurableOp("issue:b", factory).pending()).toBeUndefined();
});

test("HTTP 503 checks authoritative state and preserves pending on read failure", async () => {
  const journal = new DurableOp("issue:server-error", new IDBFactory());
  let reachable = false,
    writes = 0;
  const sender: OpSender = {
    submit: async () => {
      writes++;
      throw new ApiError(503, undefined);
    },
    get: async <T>() => {
      if (!reachable) throw new NetworkError("offline");
      return { issue_id: "issue-a", revision: 8 } as T;
    },
  };
  expect((await journal.submit(sender, envelope)).kind).toBe("unknown");
  expect(await journal.pending()).toEqual(envelope);
  reachable = true;
  expect((await journal.reconcile(sender)).kind).toBe("stale");
  expect(writes).toBe(1);
});

test("prefix inspection preserves pending intentions and never completed history", async () => {
  const factory = new IDBFactory(),
    sender = new Sender();
  const a = new DurableOp("delivery:one:facts:1", factory),
    b = new DurableOp("delivery:one:facts:2", factory),
    other = new DurableOp("other-control", factory);
  await a.submit(sender, envelope);
  await b.submit(sender, { ...envelope, request_id: "req_second", expected_control_revision: 8 });
  await other.submit(sender, { ...envelope, request_id: "req_other" });
  const entries = await DurableOp.pendingScopes("delivery:one:", factory);
  expect(entries.map((item) => item.envelope.request_id)).toEqual(["req_original", "req_second"]);
  entries[0]!.envelope.request_id = "mutated";
  expect((await a.pending())?.request_id).toBe("req_original");
  expect((await other.pending())?.request_id).toBe("req_other");
});

test("parent-scoped task creation keeps its actual task identity across a reload", async () => {
  const sender = new Sender(),
    factory = new IDBFactory(),
    first = new DurableOp("issue:task", factory);
  await first.submit(sender, {
    ...envelope,
    operation: "issues.create_task",
    payload: { title: "Same goal" },
  });
  const pending = await first.pending();
  const taskID = (pending!.payload as { task_id: string }).task_id;
  expect(taskID).toMatch(/^task_[0-9A-Z]{26}$/);
  expect(pending!.target.id).toBe("issue-a");
  sender.offline = false;
  sender.current = { task_id: taskID };
  expect((await new DurableOp("issue:task", factory).reconcile(sender)).kind).toBe("done");
  expect(sender.paths.at(-1)).toBe(`/api/v1/tasks/${taskID}`);
  expect(sender.submitted).toHaveLength(1);
});

test("interaction answers preserve their message identity and original target across reload", async () => {
  const sender = new Sender(),
    factory = new IDBFactory(),
    first = new DurableOp("interaction:original", factory);
  expect(
    (
      await first.submit(sender, {
        request_id: "req_interaction",
        operation: "messaging.respond",
        target: { type: "interaction", id: "interaction-original" },
        payload: { version: 7, option: "Continue" },
      })
    ).kind,
  ).toBe("unknown");
  const original = await first.pending();
  expect(original?.target).toEqual({ type: "interaction", id: "interaction-original" });
  expect(original?.payload).toMatchObject({
    version: 7,
    option: "Continue",
    message_id: expect.stringMatching(/^msg_[0-9A-Z]{26}$/),
  });
  const restored = new DurableOp("interaction:original", factory);
  expect(await restored.pending()).toEqual(original);
  expect((await restored.reconcile(sender)).kind).toBe("unknown");
  expect(await restored.pending()).toEqual(original);
  expect(sender.submitted).toEqual([original]);
});

test("deadline and planner creation reserve actual keys while edits retain their target", async () => {
  for (const [operation, prefix] of [
    ["issues.deadlines_put", "deadline"],
    ["planner.entries_put", "next"],
  ]) {
    const sender = new Sender(),
      factory = new IDBFactory(),
      journal = new DurableOp(`create:${prefix}`, factory);
    const create: Envelope = {
      request_id: `req_${prefix}`,
      operation: operation!,
      target: { type: prefix! },
      payload: { issue_id: "issue-a", title: "Original intent" },
    };
    await journal.submit(sender, create);
    const pending = await journal.pending();
    expect(pending?.target.id).toMatch(new RegExp(`^${prefix}_[0-9A-Z]{26}$`));
    expect(await new DurableOp(`create:${prefix}`, factory).pending()).toEqual(pending);
    expect(sender.submitted).toEqual([pending]);
    const edited = {
      ...create,
      target: { type: prefix!, id: "retained-object" },
      expected_control_revision: 2,
    };
    await new DurableOp(`edit:${prefix}`, factory).submit(sender, edited);
    expect(sender.submitted[1]).toEqual(edited);
  }
});

test.each(["issues.update_progress", "issues.set_acceptance_policy"])(
  "%s restores input and only a newer business revision fences its unknown CAS",
  async (operation) => {
    const factory = new IDBFactory(),
      sender = new Sender();
    sender.offline = false;
    sender.current = { issue_id: "issue-a", revision: 7 };
    const original = {
      ...envelope,
      operation,
      payload:
        operation === "issues.update_progress"
          ? {
              summary: "Original summary",
              next_step: "Original next step",
              expected_assignment_epoch: 2,
            }
          : { required: false, reason: "Original policy reason", expected_assignment_epoch: 2 },
    };
    const first = new DurableOp(operation, factory);
    expect((await first.submit(sender, original)).kind).toBe("unknown");
    const restored = new DurableOp(operation, factory);
    expect(await restored.pending()).toEqual(original);
    expect((await restored.reconcile(sender)).kind).toBe("unknown");
    expect(await restored.pending()).toEqual(original);
    sender.current = { issue_id: "issue-a", revision: 8 };
    expect(await restored.reconcile(sender)).toMatchObject({
      kind: "stale",
      message: expect.stringContaining("未确认原操作是否执行"),
      currentIssue: sender.current,
    });
    expect(await restored.pending()).toBeUndefined();
    expect(sender.submitted).toEqual([original]);
    expect(sender.paths).toEqual(Array(3).fill("/api/v1/issues/issue-a"));
  },
);

test.each(["issues.update_progress", "issues.set_acceptance_policy"])(
  "%s preserves unknown intent for missing, foreign, older, or unreadable business state",
  async (operation) => {
    for (const current of [
      undefined,
      { issue_id: "foreign", revision: 8 },
      { issue_id: "issue-a" },
      { issue_id: "issue-a", revision: 6 },
    ]) {
      const sender = new Sender();
      sender.offline = false;
      sender.current = current;
      const journal = new DurableOp(operation, new IDBFactory());
      const original = { ...envelope, operation };
      expect((await journal.submit(sender, original)).kind).toBe("unknown");
      expect((await journal.reconcile(sender)).kind).toBe("unknown");
      expect(await journal.pending()).toEqual(original);
      expect(sender.submitted).toEqual([original]);
    }
    for (const code of ["not_found", "forbidden"] as const) {
      const sender = new Sender();
      sender.get = async <T>(path: string): Promise<T> => {
        sender.paths.push(path);
        throw new ApiError(code === "not_found" ? 404 : 403, { code, message: code });
      };
      const journal = new DurableOp(operation, new IDBFactory());
      const original = { ...envelope, operation };
      expect((await journal.submit(sender, original)).kind).toBe("unknown");
      expect((await journal.reconcile(sender)).kind).toBe("unknown");
      expect(await journal.pending()).toEqual(original);
      expect(sender.submitted).toEqual([original]);
      expect(sender.paths).toEqual(Array(2).fill("/api/v1/issues/issue-a"));
    }
  },
);

test("a recorded Run message recovers by its immutable definition and bounded original-Run pages", async () => {
  const original: Envelope = {
    ...envelope,
    operation: "runs.send_message",
    target: { type: "run", id: "run-original" },
    payload: {
      message_id: "msg-original",
      task_id: "task-original",
      spec_version: 2,
      generation: 3,
      text: "Original supplement",
    },
  };
  const message = {
    ...(original.payload as object),
    run_id: "run-original",
    control_revision: 7,
    status: "queued",
  };
  const sender = new Sender();
  sender.get = async <T>(path: string): Promise<T> => {
    sender.paths.push(path);
    return (
      path.endsWith("after=0")
        ? { messages: [], more: true, next: 100 }
        : { messages: [message], more: false }
    ) as T;
  };
  const factory = new IDBFactory(),
    first = new DurableOp("run:message", factory);
  // A successful POST with an unknown answer does not yet observe a business message.
  sender.submit = async (e) => {
    sender.submitted.push(e);
    return { operation: { request_id: e.request_id, status: "unknown" } };
  };
  expect((await first.submit(sender, original)).kind).toBe("unknown");
  const restored = new DurableOp("run:message", factory);
  expect(await restored.reconcile(sender)).toEqual({ kind: "done", result: message });
  expect(sender.paths).toEqual([
    "/api/v1/runs/run-original/messages?limit=100&after=0",
    "/api/v1/runs/run-original/messages?limit=100&after=100",
  ]);
  expect(sender.submitted).toEqual([original]);
  expect(await restored.pending()).toBeUndefined();
});

test("Run message absence, a conflicting immutable definition, or pagination exhaustion stays unknown", async () => {
  const original: Envelope = {
    ...envelope,
    operation: "runs.send_message",
    target: { type: "run", id: "run-original" },
    payload: {
      message_id: "msg-original",
      task_id: "task-original",
      spec_version: 2,
      generation: 3,
      text: "Original supplement",
    },
  };
  const message = {
    ...(original.payload as object),
    run_id: "run-original",
    control_revision: 7,
    status: "unknown",
  };
  for (const change of [
    { run_id: "foreign" },
    { task_id: "foreign" },
    { spec_version: 3 },
    { generation: 4 },
    { text: "Other" },
    { control_revision: 8 },
    { source_message_id: "foreign" },
  ]) {
    const sender = new Sender();
    sender.offline = false;
    sender.current = { messages: [{ ...message, ...change }], more: false };
    const journal = new DurableOp("run:conflict", new IDBFactory());
    expect((await journal.submit(sender, original)).kind).toBe("unknown");
    expect(await journal.pending()).toEqual(original);
    expect(sender.submitted).toEqual([original]);
  }
  for (const kind of ["absent", "stalled", "budget"]) {
    const sender = new Sender();
    sender.get = async <T>(path: string): Promise<T> => {
      sender.paths.push(path);
      const after = Number(new URL(path, "https://fixture.invalid").searchParams.get("after"));
      return {
        messages: [],
        more: kind !== "absent",
        next: kind === "stalled" ? after : after + 100,
      } as T;
    };
    const journal = new DurableOp("run:missing", new IDBFactory());
    expect((await journal.submit(sender, original)).kind).toBe("unknown");
    expect(sender.paths).toHaveLength(kind === "budget" ? 10 : 1);
    expect(await journal.pending()).toEqual(original);
    expect(sender.submitted).toEqual([original]);
  }
});

test("association recovery observes only the original message's newer business revision", async () => {
  const original = {
    ...envelope,
    operation: "conversation.set_issue_associations",
    target: { type: "message", id: "msg-original" },
  };
  const sender = new Sender();
  sender.offline = false;
  sender.current = { message_id: "msg-original", revision: 7 };
  const journal = new DurableOp("associations", new IDBFactory());
  expect((await journal.submit(sender, original)).kind).toBe("unknown");
  sender.current = { message_id: "foreign", revision: 8 };
  expect((await journal.reconcile(sender)).kind).toBe("unknown");
  sender.current = { message_id: "msg-original", revision: 8 };
  expect((await journal.reconcile(sender)).kind).toBe("stale");
  expect(sender.submitted).toEqual([original]);
  expect(sender.paths).toEqual(
    Array(3).fill("/api/v1/messages/msg-original/associations?projection=issues"),
  );
});

test.each([
  ["issues.pause", { issue_id: "issue-a", revision: 7 }],
  ["attentions.respond", { attention_id: "issue-a", revision: 7 }],
  ["work_item_choices.respond", { choice_id: "issue-a", version: 7 }],
  ["control.set_mode", { task_id: "issue-a", control_revision: 7, control_mode: "managed" }],
])(
  "%s does not resolve an unknown CAS from unchanged business state",
  async (operation, current) => {
    const sender = new Sender();
    sender.offline = false;
    sender.current = current;
    const original = {
      ...envelope,
      operation: operation as string,
      payload: { mode: "human_control" },
    };
    const journal = new DurableOp(String(operation), new IDBFactory());
    expect((await journal.submit(sender, original)).kind).toBe("unknown");
    expect((await journal.reconcile(sender)).kind).toBe("unknown");
    expect(await journal.pending()).toEqual(original);
    expect(sender.submitted).toEqual([original]);
  },
);

test.each(["update", "pause", "resume", "cancel", "trigger"])(
  "followup %s recovery reads the original Issue/rule history and never claims the trigger succeeded",
  async (action) => {
    const original: Envelope = {
      ...envelope,
      operation: `issues.followups_${action}`,
      target: { type: "followup", id: "rule-original" },
      payload: { issue_id: "issue-original", expected_assignment_epoch: 2 },
    };
    const sender = new Sender();
    sender.offline = false;
    const rule = {
      rule_id: "rule-original",
      issue_id: "issue-original",
      revision: 7,
      trigger_count: 1,
    };
    sender.current = { rule };
    const journal = new DurableOp(action, new IDBFactory());
    expect((await journal.submit(sender, original)).kind).toBe("unknown");
    sender.current = { rule: { ...rule, issue_id: "foreign", revision: 8 } };
    expect((await journal.reconcile(sender)).kind).toBe("unknown");
    sender.current = { rule: { ...rule, rule_id: "foreign", revision: 8 } };
    expect((await journal.reconcile(sender)).kind).toBe("unknown");
    sender.current = { rule: { ...rule, revision: 8 } };
    expect(await journal.reconcile(sender)).toMatchObject({
      kind: "stale",
      message: expect.stringContaining("未确认原操作是否执行"),
    });
    expect(sender.paths).toEqual(
      Array(4).fill("/api/v1/issues/issue-original/followups/rule-original/history"),
    );
    expect(sender.submitted).toEqual([original]);
    expect(await journal.pending()).toBeUndefined();
    const legacy = { ...original, payload: { expected_assignment_epoch: 2 } };
    sender.paths.length = 0;
    const retained = new DurableOp("legacy", new IDBFactory());
    expect((await retained.submit(sender, legacy)).kind).toBe("unknown");
    expect((await retained.reconcile(sender)).kind).toBe("unknown");
    expect(sender.paths).toEqual([]);
    expect(await retained.pending()).toEqual(legacy);
  },
);

test("merely reading current delivery facts retains the unknown refresh without replay", async () => {
  const sender = new Sender();
  sender.offline = false;
  sender.current = { items: [{ status: "completed" }] };
  const original = { ...envelope, operation: "issues.refresh_delivery" };
  const journal = new DurableOp("refresh", new IDBFactory());
  expect((await journal.submit(sender, original)).kind).toBe("unknown");
  expect(await journal.reconcile(sender)).toMatchObject({
    kind: "unknown",
    message: expect.stringContaining("原刷新动作是否执行仍未确认"),
  });
  expect(sender.paths).toEqual(Array(2).fill("/api/v1/issues/issue-a/deliveries"));
  expect(sender.submitted).toEqual([original]);
  expect(await journal.pending()).toEqual(original);
});
