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
