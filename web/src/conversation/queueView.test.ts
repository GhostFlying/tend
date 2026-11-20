import { expect, test, vi } from "vitest";
import type {
  IssueConversationQueueEntry,
  IssueConversationQueuePage,
} from "../api/generated/api.ts";
import { ConversationQueue } from "./queueView.ts";

function entry(position: number): IssueConversationQueueEntry {
  return {
    position,
    revision: 1,
    status: "queued",
    can_withdraw: true,
    item: {
      message_id: "msg-" + position,
      kind: "message",
      seq: position,
      author: "owner",
      created_at: "2026-11-20T09:00:00Z",
    },
  };
}

test("queued inputs keep their loaded pages and the latest server stop identity", async () => {
  const paths: string[] = [];
  const empty: IssueConversationQueuePage = { items: [], enabled: true, total: 0, event_seq: 1 };
  let active = "original";
  const queue = new ConversationQueue({
    async get<T>(path: string): Promise<T> {
      paths.push(path);
      const params = new URL(path, "https://tend.invalid").searchParams;
      expect(params.get("projection")).toBe("chat");
      if (params.get("category") === "withdrawn") return empty as T;
      return {
        ...empty,
        total: 2,
        interruptible_message_id: active,
        ...(params.has("after") ? { items: [entry(2)] } : { items: [entry(1)], next: 1 }),
      } as T;
    },
  });
  queue.start();
  await vi.waitFor(() =>
    expect(queue.state.get().pages.owner?.interruptible_message_id).toBe(active),
  );
  await queue.moreQueue("owner");
  expect(queue.state.get().pages.owner?.items?.map((it) => it.position)).toEqual([1, 2]);
  active = "new-original";
  await queue.refresh();
  expect(queue.state.get().pages.owner?.interruptible_message_id).toBe(active);
  expect(queue.state.get().pages.owner?.items?.map((it) => it.position)).toEqual([1, 2]);
  expect(paths.every((path) => path.startsWith("/api/v1/conversation/queue?"))).toBe(true);
  queue.stop();
});

test("the current stop target is available while withdrawn history is still loading", async () => {
  let releaseHistory: () => void = () => {};
  const waitForHistory = new Promise<void>((resolve) => {
    releaseHistory = resolve;
  });
  const page: IssueConversationQueuePage = { items: [], enabled: true, total: 0, event_seq: 1 };
  const queue = new ConversationQueue({
    async get<T>(path: string): Promise<T> {
      if (path.includes("category=withdrawn")) {
        await waitForHistory;
        return page as T;
      }
      return { ...page, interruptible_message_id: "active-original" } as T;
    },
  });
  queue.start();
  try {
    await vi.waitFor(() =>
      expect(queue.state.get().pages.owner?.interruptible_message_id).toBe("active-original"),
    );
    expect(queue.state.get().pages.withdrawn).toBeUndefined();
  } finally {
    releaseHistory();
    queue.stop();
  }
});
