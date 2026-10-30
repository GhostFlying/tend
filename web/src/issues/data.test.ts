import { expect, test } from "vitest";
import type { Reader } from "../live/data.ts";
import { Issues, issuePagePath } from "./data.ts";
import { AttentionData } from "../attention/data.ts";
import { DirectConversations, conversationReader } from "../conversation/api.ts";

// Deliberately allow a response after abort, as a completed transport can arrive late.
class ReaderFixture implements Reader {
  calls: { path: string; signal?: AbortSignal; answer: (value: unknown) => void }[] = [];
  get<T>(path: string, signal?: AbortSignal): Promise<T> {
    return new Promise<T>((resolve) => {
      this.calls.push({ path, ...(signal ? { signal } : {}), answer: (v) => resolve(v as T) });
    });
  }
}

const settle = async () => {
  await Promise.resolve();
  await Promise.resolve();
};

test("Issue paths preserve root/child pagination and late detail cannot replace selection or history", async () => {
  const api = new ReaderFixture(),
    issues = new Issues(api);
  expect(issuePagePath({ parentID: "" })).toBe("/api/v1/issues?parent_id=");
  expect(issuePagePath()).toBe("/api/v1/issues");
  const children = issues.children("parent/one", "child cursor");
  expect(new URL(api.calls[0]!.path, "https://tend.invalid").searchParams.get("after")).toBe(
    "child cursor",
  );
  api.calls[0]!.answer({ items: [], more: true, next: "page two" });
  await settle();
  expect(children.state.get().value?.next).toBe("page two");
  const old = issues.open("old/issue");
  const oldCall = api.calls[1]!;
  const current = issues.open("current");
  expect(oldCall.signal?.aborted).toBe(true);
  const history = issues.openHistory("current", 2);
  api.calls[2]!.answer({ issue_id: "current", revision: 9 });
  api.calls[3]!.answer({ issue_id: "current", revision: 2 });
  await Promise.all([current, history]);
  oldCall.answer({ issue_id: "old/issue", revision: 100 });
  await old;
  expect(issues.detail.state.get().value).toMatchObject({ issue_id: "current", revision: 9 });
  expect(issues.history.state.get().value?.revision).toBe(2);
  const pending = issues.open("not retained");
  const stopped = api.calls.at(-1)!;
  issues.stop();
  expect(stopped.signal?.aborted).toBe(true);
  stopped.answer({ issue_id: "not retained", revision: 10 });
  await pending;
  expect(issues.detail.state.get().value).toBeNull();
});

test("Inbox and chat share an Attention detail identity without replacing its original question", async () => {
  const api = new ReaderFixture(),
    data = new AttentionData(api);
  const chat = data.attention("attention/one");
  const detail = data.attention("attention/one");
  expect(detail).toBe(chat);
  expect(api.calls).toHaveLength(1);
  api.calls[0]!.answer({
    attention_id: "attention/one",
    revision: 4,
    question_version: 7,
    decision_id: "original-question",
    status: "open",
  });
  await settle();
  const inbox = data.openInbox();
  api.calls[1]!.answer({ items: [{ attention_id: "attention/one", revision: 4 }], more: false });
  await inbox;
  expect(data.inbox.state.get().value?.items?.[0]?.attention_id).toBe(
    chat.state.get().value?.attention_id,
  );
  expect(detail.state.get().value).toMatchObject({
    decision_id: "original-question",
    question_version: 7,
    status: "open",
  });
  data.stop();
});

test("logical-chat readers scope only conversation GETs and exact-run views remain read-only", async () => {
  const api = new ReaderFixture(),
    scoped = conversationReader(api, "consultation/one");
  const controller = new AbortController();
  const page = scoped.get("/api/v1/conversation?limit=50", controller.signal);
  const query = new URL(api.calls[0]!.path, "https://tend.invalid").searchParams;
  expect(query.get("conversation_id")).toBe("consultation/one");
  expect(query.get("limit")).toBe("50");
  expect(api.calls[0]!.signal).toBe(controller.signal);
  api.calls[0]!.answer({ items: [] });
  await page;
  const chats = new DirectConversations(scoped);
  const messages = chats.openRunMessages("run/original", 11);
  const native = chats.openNativeEntry("run/original", "orca");
  expect(api.calls[1]!.path).toBe("/api/v1/runs/run%2Foriginal/messages?after=11");
  expect(api.calls[2]!.path).toBe("/api/v1/runs/run%2Foriginal/native-entry?provider=orca");
  api.calls[1]!.answer({ items: [], more: false });
  api.calls[2]!.answer({ run_id: "run/original", controller: "worker" });
  await Promise.all([messages, native]);
  chats.stop();
});

test("an effect restart reopens the child reader already held by the page subscription", async () => {
  let reads = 0;
  const issues = new Issues({
    get: async <T>() => {
      reads++;
      return { items: [], more: false } as T;
    },
  });
  const subscribed = issues.children("parent", undefined, true);
  await subscribed.refresh();
  issues.stop();
  const before = reads;
  issues.children("parent", undefined, true);
  await subscribed.refresh();
  expect(subscribed.state.get().value).toEqual({ items: [], more: false });
  expect(reads).toBeGreaterThan(before);
  issues.stop();
});
