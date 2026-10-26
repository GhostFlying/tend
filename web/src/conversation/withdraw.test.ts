import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import type { ApiClient } from "../api/client.ts";
import type { ConversationQueueEntry } from "../api/generated/api.ts";
import { ComposerDraft } from "./draft.ts";
import { DraftStorage } from "./draftStore.ts";
import { Withdrawals } from "./withdraw.ts";

beforeEach(() => {
  vi.stubGlobal("indexedDB", new IDBFactory());
  const values = new Map<string, string>();
  vi.stubGlobal("sessionStorage", {
    getItem: (k: string) => values.get(k) ?? null,
    setItem: (k: string, v: string) => values.set(k, v),
  });
  vi.stubGlobal("window", new EventTarget());
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
const entry: ConversationQueueEntry = {
  position: 1,
  revision: 1,
  status: "queued",
  can_withdraw: true,
  item: {
    message_id: "message",
    seq: 1,
    author: "owner",
    kind: "message",
    body: "retrieve me",
    created_at: "2026-01-01T00:00:00Z",
    attachments: [
      { file_id: "document", name: "notes.txt", media_type: "text/plain", sha256: "hash", size: 4 },
    ],
  },
};
test("a new tab explicitly adopts an unfinished retrieval after the old tab closes", async () => {
  const old = new ComposerDraft();
  await old.open();
  old.write("original draft");
  const offline = {
    lookup: async () => {
      throw new Error("offline");
    },
  } as unknown as ApiClient;
  await new Withdrawals(offline, old, () => {}, null).start(entry, true);
  sessionStorage.setItem("tend.draft-id", "new-tab");
  const current = new ComposerDraft();
  await current.open();
  current.write("new draft");
  const client = { lookup: async () => ({ status: "succeeded" }) } as unknown as ApiClient;
  const controller = new Withdrawals(client, current, () => {}, null);
  await controller.resume();
  expect(current.state.get().text).toBe("new draft");
  const w = controller.state.get()[0]!;
  await expect(controller.adopt(w.id)).rejects.toThrow("原标签页");
  await (await old.durableStorage()).release();
  await controller.adopt(w.id);
  expect(current.state.get().text).toBe("retrieve me");
  expect(current.state.get().attachments[0]?.file_id).toBe("document");
  expect(current.state.get().saved.some((d) => d.text === "new draft")).toBe(true);
  expect(current.state.get().saved.some((d) => d.text === "original draft")).toBe(true);
  current.write("edited once");
  await controller.resume();
  expect(current.state.get().text).toBe("edited once");
  expect((await (await DraftStorage.open()).withdrawals())[0]?.state).toBe("done");
});
test("returning from a cached page reacquires the draft before it saves", async () => {
  const draft = new ComposerDraft();
  await draft.open();
  draft.write("retained");
  await draft.flush();
  window.dispatchEvent(new Event("pagehide"));
  await vi.waitFor(async () => {
    await expect(draft.flush()).rejects.toThrow("编辑权");
  });
  window.dispatchEvent(new Event("pageshow"));
  await vi.waitFor(async () => {
    await draft.flush();
  });
  draft.write("after return");
  await draft.flush();
  expect((await (await draft.durableStorage()).drafts()).find((d) => d.id === draft.id)?.text).toBe(
    "after return",
  );
});
