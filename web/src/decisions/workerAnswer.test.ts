import { afterEach, expect, test, vi } from "vitest";
import { NetworkError } from "../api/client.ts";
import type { DecisionView, Envelope } from "../api/generated/api.ts";
import { dismissNotice, notice } from "../app/notice.ts";
import type { Services } from "../app/services.tsx";
import { answer } from "./answer.ts";

const decision: DecisionView = {
  decision_id: "dec_question",
  task_id: "task_investigate",
  question: "Should archived items stay visible?",
  options: ["Keep them", "Hide them"],
  version: 1,
  status: "open",
  worker: {
    run_id: "run_investigate",
    role: "implement",
    spec_version: 1,
    question_key: "archived-policy",
    evidence: "Both behaviors exist in the fixtures.",
    question_message_id: "msg_question",
  },
};

function services() {
  const submit = vi.fn(async (_envelope: Envelope) => ({
    operation: { status: "succeeded", result: { decision_id: decision.decision_id } },
  }));
  const lookup = vi.fn();
  const enqueue = vi.fn();
  const refreshSnapshot = vi.fn();
  const refreshDetail = vi.fn();
  const value = {
    client: { submit, lookup },
    queue: { enqueue },
    live: {
      conversation: { get: () => ({ items: [{ seq: 8 }] }) },
      detail: { get: () => ({ taskId: decision.task_id }) },
      refreshSnapshot,
      refreshDetail,
    },
  } as unknown as Services;
  return { value, submit, lookup, enqueue, refreshSnapshot, refreshDetail };
}

afterEach(dismissNotice);

test("worker options and free text answer the original decision directly", async () => {
  for (const [option, text] of [
    ["Keep them", "Only hide deleted items"],
    ["", "  Use the documented behavior.  "],
  ]) {
    const s = services();
    expect(await answer(s.value, decision, option!, "Investigation", text)).toBe(true);
    expect(s.enqueue).not.toHaveBeenCalled();
    expect(s.submit).toHaveBeenCalledOnce();
    const request = s.submit.mock.calls[0]![0];
    expect(request).toMatchObject({
      operation: "decisions.respond",
      target: { type: "decision", id: decision.decision_id },
      payload: {
        version: decision.version,
        ...(option ? { option } : {}),
        text: text!.trim(),
      },
    });
    expect(request.request_id).toMatch(/^req_/);
    expect(s.refreshSnapshot).toHaveBeenCalledOnce();
    expect(s.refreshDetail).toHaveBeenCalledOnce();
    expect(notice.get()?.text).toBe("回答已发送给 worker");
  }
});

test("a lost worker answer receipt is looked up under the same request identity", async () => {
  const s = services();
  s.submit.mockRejectedValueOnce(new NetworkError("lost receipt"));
  s.lookup.mockResolvedValueOnce({ status: "succeeded", result: { status: "answered" } });
  expect(await answer(s.value, decision, "Keep them", undefined)).toBe(true);
  const id = s.submit.mock.calls[0]![0].request_id;
  expect(s.lookup).toHaveBeenCalledWith(id);
  expect(s.submit).toHaveBeenCalledOnce();
});

test("unknown worker answer is not reported as success or blindly resubmitted", async () => {
  const s = services();
  s.submit.mockRejectedValueOnce(new NetworkError("lost receipt"));
  s.lookup.mockRejectedValueOnce(new NetworkError("still offline"));
  expect(await answer(s.value, decision, "", undefined, "Keep this draft")).toBe(false);
  expect(s.submit).toHaveBeenCalledOnce();
  expect(notice.get()?.tone).toBe("warn");
});

test("blank worker answer does not create an operation", async () => {
  const s = services();
  expect(await answer(s.value, decision, "", undefined, " \n ")).toBe(false);
  expect(s.submit).not.toHaveBeenCalled();
});

test("ordinary main-agent decisions still use the chat outbox", async () => {
  const s = services();
  const ordinary = { ...decision };
  delete ordinary.worker;
  expect(await answer(s.value, ordinary, "Keep them", "Investigation")).toBe(true);
  expect(s.submit).not.toHaveBeenCalled();
  expect(s.enqueue).toHaveBeenCalledWith(
    expect.objectContaining({
      decision: { decision_id: decision.decision_id, version: 1, option: "Keep them" },
      lastSeenSeq: 8,
    }),
  );
});
