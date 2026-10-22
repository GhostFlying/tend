import { describe, expect, test } from "vitest";
import type { ConversationItem } from "../api/generated/api.ts";
import type { OutgoingMessage } from "../queue/outbox.ts";
import { buildEntries, duration, processing, splitTaskIds, toolSummary } from "./model.ts";

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
  test("names what the call is about", () => {
    expect(toolSummary('{"title":"修 Windows 构建","project":"tend","commit_seq":1}')).toBe(
      "修 Windows 构建 · tend",
    );
    expect(toolSummary("go test ./...\nmore")).toBe("go test ./...");
    expect(toolSummary(undefined)).toBe("");
  });
});

test("duration reads in Chinese", () => {
  expect(duration(6_200)).toBe("6 秒");
  expect(duration(125_000)).toBe("2 分 5 秒");
  expect(duration(3_780_000)).toBe("1 小时 3 分");
});
