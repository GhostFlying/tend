import { describe, expect, test } from "vitest";
import type { IssueConversationItem as ConversationItem } from "../api/generated/api.ts";
import type { OutgoingMessage } from "../queue/outbox.ts";
import {
  systemMessageBody,
  buildEntries,
  processing,
  latestReply,
  duration,
  splitTaskIds,
  toolSummary,
} from "./model.ts";

const id = "task_01M5HDDQEKB89928CHASWS8QND";

describe("splitTaskIds", () => {
  test("finds task IDs in text", () => {
    expect(splitTaskIds(`建好了 ${id}，评级 p1`)).toEqual(["建好了 ", { taskId: id }, "，评级 p1"]);
    expect(splitTaskIds(`${id}和${id}`)).toEqual([{ taskId: id }, "和", { taskId: id }]);
  });

  test("leaves what only looks like one", () => {
    for (const text of [
      "task_01M5HDDQEKB89928CHASWS8QN", // 25 digits
      "task_01M5HDDQEKB89928CHASWS8QNDX", // 27 digits
      "task_01m5hddqekb89928chasws8qnd", // lower case
      "task_01M5HDDQEKB89928CHASWS8QNI", // I is not Crockford
      "task_81M5HDDQEKB89928CHASWS8QND", // first digit past 7
      `my${id}`, // part of a longer word
      "run_01M5HDDQEKB89928CHASWS8QND",
    ]) {
      expect(splitTaskIds(text)).toEqual([text]);
    }
  });
});

function item(seq: number, over: Partial<ConversationItem> = {}): ConversationItem {
  return {
    seq,
    kind: "message",
    message_id: `msg_${seq}`,
    author: "owner",
    body: `m${seq}`,
    created_at: new Date(Date.UTC(2026, 9, 22, 10, 0, seq)).toISOString(),
    ...over,
  };
}

function outgoing(requestId: string, over: Partial<OutgoingMessage> = {}): OutgoingMessage {
  return {
    requestId,
    order: 1,
    envelope: {
      request_id: requestId,
      operation: "conversation.submit_message",
      target: { type: "conversation" },
    },
    body: requestId,
    createdAt: 0,
    updatedAt: 0,
    state: "sending",
    ...over,
  };
}

describe("buildEntries", () => {
  test("groups the main agent's items under the message they answer", () => {
    const items = [
      item(1, { state: "replied" }),
      item(2, {
        author: "main_agent",
        kind: "tool_use",
        reply_to: "msg_1",
        tool: { name: "tasks_read", status: "completed", turn_id: "t", updated_at: "" },
      }),
      item(3, { author: "main_agent", reply_to: "msg_1" }),
      item(4, { state: "processing" }),
      item(5, {
        author: "main_agent",
        kind: "tool_use",
        reply_to: "msg_4",
        tool: { name: "tasks_list", status: "running", turn_id: "t2", updated_at: "" },
      }),
    ];
    const entries = buildEntries(items, []);
    expect(entries.map((e) => e.kind)).toEqual(["divider", "owner", "agent", "owner", "agent"]);
    const [, , first, , second] = entries;
    expect(first).toMatchObject({ replyTo: "msg_1", done: { tookMs: 2_000 } });
    expect(first?.kind === "agent" && first.items.map((i) => i.seq)).toEqual([2, 3]);
    // The running turn has no footer yet.
    expect(second).not.toHaveProperty("done");
    expect(processing(items)?.message_id).toBe("msg_4");
  });

  test("shows outbox messages until tend's item for them arrives", () => {
    const items = [item(1, { state: "replied" })];
    const entries = buildEntries(items, [
      outgoing("req_a", { state: "sent", messageId: "msg_1", seq: 1 }),
      outgoing("req_b", { state: "sent", messageId: "msg_2", seq: 2 }),
      outgoing("req_c", { state: "unknown" }),
      outgoing("req_d"),
    ]);
    expect(entries.filter((e) => e.kind === "outgoing").map((e) => e.key)).toEqual([
      "req_b",
      "req_c",
      "req_d",
    ]);
  });

  test("leaves out sent messages from before the loaded page", () => {
    const entries = buildEntries(
      [item(40)],
      [outgoing("req_old", { state: "sent", messageId: "msg_3", seq: 3 })],
    );
    expect(entries.some((e) => e.kind === "outgoing")).toBe(false);
  });
});

describe("toolSummary", () => {
  test("uses resolved titles without exposing input or identifiers", () => {
    expect(toolSummary('{"title":"修Windows构建","task_id":"task_raw"}')).toBe("");
    expect(toolSummary("go test ./...\nmore")).toBe("");
    expect(toolSummary(undefined)).toBe("");
    expect(
      toolSummary('{"task_id":"task_raw"}', (kind, id) =>
        kind === "task" && id === "task_raw" ? "修Windows构建" : undefined,
      ),
    ).toBe("修Windows构建");
    expect(
      toolSummary('{"issue_id":"issue_raw"}', (kind) =>
        kind === "issue" ? "原始Issue标题" : undefined,
      ),
    ).toBe("原始Issue标题");
  });
});

test("duration reads in Chinese", () => {
  expect(duration(6_200)).toBe("6 秒");
  expect(duration(125_000)).toBe("2 分 5 秒");
  expect(duration(3_780_000)).toBe("1 小时 3 分");
});

test("a streamed reply timestamp does not claim to measure completion duration", () => {
  const owner: ConversationItem = {
    seq: 1,
    message_id: "msg_owner",
    kind: "message",
    author: "owner",
    body: "hello",
    state: "replied",
    created_at: "2026-10-22T10:00:00Z",
  };
  const reply: ConversationItem = {
    seq: 2,
    message_id: "msg_reply",
    kind: "message",
    author: "main_agent",
    reply_to: owner.message_id,
    body: "complete",
    assistant_state: "completed",
    created_at: "2026-10-22T10:00:01Z",
  };
  const entry = buildEntries([owner, reply], []).find((e) => e.kind === "agent");
  expect(entry?.done).toEqual({ at: Date.parse(reply.created_at) });
});

test("system completion cards stay independent of owners and agent turns", () => {
  const owner: ConversationItem = {
    seq: 1,
    message_id: "owner",
    kind: "message",
    author: "owner",
    body: "work",
    state: "processing",
    created_at: "2026-10-23T10:00:00Z",
  };
  const first: ConversationItem = {
    seq: 2,
    message_id: "first",
    kind: "message",
    author: "main_agent",
    reply_to: "owner",
    body: "first",
    assistant_state: "streaming",
    created_at: "2026-10-23T10:00:01Z",
  };
  const system: ConversationItem = {
    seq: 3,
    message_id: "notice",
    kind: "message",
    author: "system",
    body: `任务 ${id} 完成`,
    created_at: "2026-10-23T10:00:02Z",
  };
  const next = { ...first, seq: 4, message_id: "next", body: "continued" };
  const entries = buildEntries([owner, first, system, next], []).filter(
    (e) => e.kind !== "divider",
  );
  expect(entries.map((e) => e.kind)).toEqual(["owner", "agent", "system", "agent"]);
  expect(entries[2]).toMatchObject({ key: "notice", item: system });
  expect(processing([owner, first, system])).toEqual(owner);
  expect(latestReply([owner, first, system])).toEqual(first);
});

test("background event replies retain their provenance and can be stopped", () => {
  const event: ConversationItem = {
    seq: 1,
    message_id: "event",
    kind: "message",
    author: "system",
    body: "Investigation completed",
    state: "processing",
    created_at: "2026-10-24T10:00:00Z",
  };
  const reply: ConversationItem = {
    seq: 2,
    message_id: "reply",
    kind: "message",
    author: "main_agent",
    reply_to: "event",
    body: "Reading findings",
    assistant_state: "streaming",
    created_at: "2026-10-24T10:00:01Z",
  };
  const ordinaryNotice: ConversationItem = { ...event, seq: 3, message_id: "upgrade" };
  delete ordinaryNotice.state;
  const items = [event, reply, ordinaryNotice];
  expect(processing(items)).toEqual(event);
  const entries = buildEntries(items, []).filter((entry) => entry.kind !== "divider");
  expect(entries.map((entry) => entry.kind)).toEqual(["system", "agent", "system"]);
  expect(entries[1]).toMatchObject({ replyTo: "event", items: [reply] });
  const stopped = [
    { ...event, state: "interrupted" as const },
    { ...reply, assistant_state: "interrupted" as const },
    ordinaryNotice,
  ];
  expect(processing(stopped)).toBeUndefined();
  expect(buildEntries(stopped, []).find((entry) => entry.kind === "agent")?.done).toEqual({
    at: Date.parse(reply.created_at),
  });
});

test.each(["open", "answered", "withdrawn", "superseded"])(
  "system notice omits only a repeated card question (%s)",
  (status) => {
    const choice = {
      choice_id: "choice-one",
      work_item_id: "wi-one",
      message_id: "msg-one",
      source_id: "event-one",
      version: 1,
      status,
      question: "是否继续上线？",
      options: [],
      created_at: 1,
      updated_at: 1,
    };
    expect(systemMessageBody({ body: choice.question, choices: [choice] })).toBe("");
    expect(systemMessageBody({ body: "已通过验收，是否继续上线？", choices: [choice] })).toBe(
      "已通过验收，是否继续上线？",
    );
    expect(systemMessageBody({ body: choice.question })).toBe(choice.question);
  },
);
