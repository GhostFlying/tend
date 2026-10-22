import { expect, test } from "vitest";
import type { ConversationItem } from "../api/generated/api.ts";
import type { OutgoingMessage } from "../queue/outbox.ts";
import { messageAnswer } from "./answer.ts";

const d = { decision_id: "dec_1", version: 2 };
const choice = { decision_id: "dec_1", version: 2, option: "revise the spec" };

function out(
  state: OutgoingMessage["state"],
  extra: Partial<OutgoingMessage> = {},
): OutgoingMessage {
  return {
    requestId: "req_1",
    order: 1,
    envelope: {
      request_id: "req_1",
      operation: "conversation.submit_message",
      target: { type: "conversation" },
      payload: { body: "x", decision: choice },
    },
    body: "x",
    createdAt: 0,
    updatedAt: 0,
    state,
    ...extra,
  };
}

function owner(state: NonNullable<ConversationItem["state"]>, decision = choice): ConversationItem {
  return {
    seq: 5,
    kind: "message",
    message_id: "msg_5",
    author: "owner",
    created_at: "2026-10-22T10:00:00Z",
    state,
    decision,
  };
}

test("an answer on its way or with the main agent is pending", () => {
  expect(messageAnswer(d, [], [out("sending")])).toEqual({
    option: "revise the spec",
    state: "pending",
  });
  expect(messageAnswer(d, [owner("processing")], [])?.state).toBe("pending");
  expect(
    messageAnswer(d, [owner("queued")], [out("sent", { messageId: "msg_5", seq: 5 })])?.state,
  ).toBe("pending");
});

test("a refused answer is failed, so the decision is read again", () => {
  expect(messageAnswer(d, [], [out("failed")])?.state).toBe("failed");
});

test("once the turn ends, or for another version, the buttons are back", () => {
  expect(
    messageAnswer(d, [owner("replied")], [out("sent", { messageId: "msg_5", seq: 5 })]),
  ).toBeUndefined();
  expect(
    messageAnswer({ ...d, version: 3 }, [owner("processing")], [out("sending")]),
  ).toBeUndefined();
});
