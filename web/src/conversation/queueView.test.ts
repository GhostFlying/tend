import { expect, test } from "vitest";
import type {
  ConversationItem,
  ConversationQueueEntry,
  LedgerEvent,
  TranscriptBlock,
} from "../api/generated/api.ts";
import { ConversationQueue } from "./queueView.ts";
function item(id: string, seq: number, author = "owner"): ConversationItem {
  return { message_id: id, seq, author, kind: "message", created_at: "2026-01-01T00:00:00Z" };
}
const event = (it: ConversationItem): LedgerEvent => ({
  event_seq: 2,
  kind: "conversation.message",
  object_type: "message",
  object_id: it.message_id,
  created_at: "2026-01-01T00:00:00Z",
  item: it,
});
test("queue counts and pagination do not depend on the transcript page", async () => {
  const all: ConversationQueueEntry[] = Array.from({ length: 120 }, (_, n) => ({
    item: item(`m${n}`, n + 1),
    position: n + 1,
    revision: 1,
    status: "queued",
    can_withdraw: true,
  }));
  const q = new ConversationQueue({
    async get<T>(path: string): Promise<T> {
      const url = new URL(path, "http://test");
      if (path.includes("/transcript")) return { blocks: [], more: false, event_seq: 1 } as T;
      const owner = url.searchParams.get("category") === "owner",
        after = Number(url.searchParams.get("after") || 0);
      return {
        enabled: true,
        total: owner ? 120 : 0,
        items: owner ? all.slice(after, after + 50) : [],
        next: owner && after + 50 < 120 ? after + 50 : 0,
        event_seq: 1,
      } as T;
    },
  });
  q.start();
  await q.refresh();
  expect(q.state.get().pages.owner?.total).toBe(120);
  expect(q.state.get().pages.owner?.items).toHaveLength(50);
  expect(q.state.get().blocks).toEqual([]);
  await q.moreQueue("owner");
  expect(q.state.get().pages.owner?.items).toHaveLength(100);
  await q.refresh();
  expect(q.state.get().pages.owner?.items).toHaveLength(100);
  q.stop();
});
test("live output fetches its absent parent and survives an older page revision", async () => {
  const trigger = item("old-parent", 1);
  trigger.state = "processing";
  const output = {
    ...item("reply", 80, "main_agent"),
    reply_to: trigger.message_id,
    body: "new stream",
    assistant_revision: 3,
  };
  const paths: string[] = [];
  const q = new ConversationQueue({
    async get<T>(path: string): Promise<T> {
      paths.push(path);
      if (path.includes("/blocks/"))
        return {
          position: 1,
          trigger,
          items: [{ ...output, body: "old", assistant_revision: 1 }],
          more: false,
        } as T;
      if (path.includes("/transcript")) return { blocks: [], more: false, event_seq: 1 } as T;
      return { enabled: true, items: [], total: 0, event_seq: 1 } as T;
    },
  });
  q.start();
  await q.refresh();
  q.changed(event(output));
  await q.refresh();
  expect(paths.some((p) => p.includes("/blocks/old-parent"))).toBe(true);
  expect(q.state.get().blocks[0]?.items?.[0]?.body).toBe("new stream");
  q.stop();
});
test("refresh refills loaded continuation pages after reconnect", async () => {
  const trigger = item("a", 1);
  trigger.state = "processing";
  const outputs = Array.from({ length: 80 }, (_, n) => ({
    ...item(`reply${n}`, n + 2, "main_agent"),
    reply_to: "a",
    body: "before",
    assistant_revision: 1,
  }));
  let revision = 1;
  const block = (after: number): TranscriptBlock => {
    const entries = outputs
      .filter((it) => it.seq > after)
      .slice(0, 50)
      .map((it) => ({
        ...it,
        body: revision === 2 ? "after" : "before",
        assistant_revision: revision,
      }));
    return {
      position: 1,
      trigger,
      items: entries,
      more: after === 0,
      next_seq: entries.at(-1)?.seq ?? after,
    };
  };
  const q = new ConversationQueue({
    async get<T>(path: string): Promise<T> {
      if (path.includes("/blocks/"))
        return block(Number(new URL(path, "http://test").searchParams.get("after"))) as T;
      if (path.includes("/transcript"))
        return { blocks: [block(0)], more: false, event_seq: revision } as T;
      return { enabled: true, items: [], total: 0, event_seq: revision } as T;
    },
  });
  q.start();
  await q.refresh();
  await q.moreBlock(q.state.get().blocks[0]!);
  expect(q.state.get().blocks[0]?.items).toHaveLength(80);
  revision = 2;
  await q.refresh();
  expect(q.state.get().blocks[0]?.items?.at(-1)?.body).toBe("after");
  expect(q.state.get().blocks[0]?.more).toBe(false);
  q.stop();
});

test("reconnect bridges missing pages before retaining older loaded history", async () => {
  let newer = false;
  const q = new ConversationQueue({
    async get<T>(path: string): Promise<T> {
      if (!path.includes("transcript"))
        return { enabled: true, items: [], total: 0, event_seq: newer ? 2 : 1 } as T;
      const start = !newer ? 1 : path.includes("before=") ? 11 : 31;
      return {
        blocks: Array.from({ length: 20 }, (_, i) => ({
          position: start + i,
          notice_only: true,
          trigger: {
            message_id: `message-${start + i}`,
            seq: start + i,
            author: "system",
            kind: "message",
            created_at: "2026-01-01T00:00:00Z",
          },
          items: [],
          more: false,
        })),
        more: start > 1,
        next_before: start > 1 ? start : undefined,
        event_seq: newer ? 2 : 1,
      } as T;
    },
  });
  q.start();
  await q.refresh();
  newer = true;
  await q.refresh();
  expect(q.state.get().blocks.map((b) => b.position)).toEqual(
    Array.from({ length: 50 }, (_, i) => i + 1),
  );
  expect(q.state.get().more).toBe(false);
  q.stop();
});
