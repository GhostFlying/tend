import { IDBFactory } from "fake-indexeddb";
import { expect, test } from "vitest";
import { DraftStorage, type SavedDraft, type Withdrawal } from "./draftStore.ts";

test("withdrawal restoration atomically preserves the old draft and restores only once", async () => {
  const factory = new IDBFactory();
  const a = await DraftStorage.open(factory),
    b = await DraftStorage.open(factory);
  const old: SavedDraft = {
    id: "tab",
    text: "unfinished intent",
    files: [new File(["bytes"], "original.txt")],
    attachments: [],
    updated: 1,
  };
  const w: Withdrawal = {
    id: "withdraw",
    draftId: "tab",
    state: "pending",
    edit: true,
    item: {
      message_id: "m",
      seq: 1,
      author: "owner",
      kind: "message",
      created_at: "2026-01-01T00:00:00Z",
    },
    envelope: {
      request_id: "withdraw",
      operation: "conversation.withdraw_message",
      target: { type: "message", id: "m" },
      payload: {},
    },
  };
  const recovered: SavedDraft = {
    ...old,
    text: "retrieved",
    files: [],
    attachments: [
      {
        file_id: "file",
        name: "already-uploaded.txt",
        size: 5,
        media_type: "text/plain",
        sha256: "digest",
      },
    ],
    updated: 2,
  };
  await a.save("drafts", old);
  await a.save("withdrawals", w);
  const result = await Promise.all([a.restore(w, recovered), b.restore(w, recovered)]);
  expect(result.filter(Boolean)).toHaveLength(1);
  const saved = await b.drafts();
  expect(saved.find((d) => d.id === "tab")?.text).toBe("retrieved");
  expect(saved.find((d) => d.id === "stash:withdraw")?.text).toBe("unfinished intent");
  expect(saved.find((d) => d.id === "stash:withdraw")?.files[0]?.size).toBe(5);
  expect(saved.find((d) => d.id === "tab")?.attachments[0]?.file_id).toBe("file");
  expect((await b.withdrawals())[0]?.state).toBe("done");
});

test("expired draft owners cannot overwrite the successor's draft", async () => {
  const factory = new IDBFactory();
  const a = await DraftStorage.open(factory),
    b = await DraftStorage.open(factory);
  expect(await a.claim("tab", "first", Date.now() - 120000)).toBe(true);
  expect(await b.claim("tab", "second")).toBe(true);
  expect(await a.claim("tab", "first")).toBe(false);
  const value: SavedDraft = {
    id: "tab",
    text: "successor",
    files: [],
    attachments: [],
    updated: 1,
  };
  await b.save("drafts", value);
  await expect(a.save("drafts", { ...value, text: "stale" })).rejects.toThrow("编辑权");
  expect((await b.drafts())[0]?.text).toBe("successor");
});
