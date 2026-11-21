import { expect, test, vi } from "vitest";
import type { ApiClient } from "../api/client.ts";
import type { MessagingMessage, MessagingPage } from "../api/generated/api.ts";
import { MessagingFeed } from "./feed.ts";

test("reconnection fills more than one missing page without losing visible history", async () => {
  let items = Array.from(
    { length: 100 },
    (_, n) => ({ message_id: `m${n + 1}`, sequence: n + 1 }) as MessagingMessage,
  );
  const get = vi.fn(async (path: string): Promise<MessagingPage> => {
    const url = new URL(path, "http://test.invalid");
    if (url.searchParams.has("after")) {
      const after = Number(url.searchParams.get("after"));
      const rest = items.filter((m) => m.sequence > after);
      return {
        items: rest.slice(0, 100),
        next: rest.slice(0, 100).at(-1)?.sequence ?? after,
        more: rest.length > 100,
      };
    }
    return { items: items.slice(-100), next: items.at(-1)!.sequence, more: items.length > 100 };
  });
  const feed = new MessagingFeed({
    get: get as ApiClient["get"],
    noteAlive() {},
    async openStream(_path, signal) {
      return new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(new TextEncoder().encode('event: start\ndata: {"event_seq":1}\n\n'));
            signal?.addEventListener("abort", () => controller.close(), { once: true });
          },
        }),
      );
    },
  });
  feed.start();
  try {
    await vi.waitFor(() => expect(feed.state.get().items).toHaveLength(100));
    items = Array.from(
      { length: 351 },
      (_, n) => ({ message_id: `m${n + 1}`, sequence: n + 1 }) as MessagingMessage,
    );
    await feed.refresh();
    expect(feed.state.get().items.map((m) => m.sequence)).toEqual(items.map((m) => m.sequence));
    expect(feed.state.get().more).toBe(false);
    expect(get).toHaveBeenCalledWith(
      "/api/v1/messaging/messages?after=200",
      expect.any(AbortSignal),
    );
  } finally {
    await feed.stop();
  }
});

test("failed old-message invalidations survive until a successful refresh", async () => {
  let failRead = true;
  let detailReads = 0;
  let processing = "processing";
  let emit: (message: string) => void = () => {
    throw new Error("stream not started");
  };
  const initial = {
    message_id: "question",
    sequence: 1,
    direction: "input",
    processing: "waiting_execution",
    issue_ids: ["issue"],
  } as MessagingMessage;
  const feed = new MessagingFeed({
    get: (async (path: string) => {
      if (path.endsWith("/question")) {
        detailReads++;
        if (failRead) throw new Error("temporary read failure");
        return { ...initial, processing };
      }
      return {
        items: path.includes("latest") ? [initial] : [],
        next: 1,
        more: false,
      };
    }) as ApiClient["get"],
    noteAlive() {},
    async openStream(_path, signal) {
      return new Response(
        new ReadableStream({
          start(controller) {
            emit = (message) => controller.enqueue(new TextEncoder().encode(message));
            emit('event: start\ndata: {"event_seq":1}\n\n');
            signal?.addEventListener("abort", () => controller.close(), { once: true });
          },
        }),
      );
    },
  });
  feed.start();
  try {
    await vi.waitFor(() => expect(feed.state.get().items).toHaveLength(1));
    emit(
      'event: ledger\ndata: {"event_seq":2,"object_type":"message","object_id":"question","kind":"messaging.interaction_changed"}\n\n',
    );
    await vi.waitFor(() => expect(feed.state.get().error).toContain("temporary read failure"));
    failRead = false;
    await feed.refresh();
    expect(detailReads).toBe(2);
    expect(feed.state.get().items.find((m) => m.message_id === "question")?.processing).toBe(
      "processing",
    );
    processing = "processed";
    emit(
      'event: ledger\ndata: {"event_seq":3,"object_type":"task","object_id":"task","kind":"messaging.work_changed","issue_id":"issue"}\n\n',
    );
    await vi.waitFor(() =>
      expect(feed.state.get().items.find((m) => m.message_id === "question")?.processing).toBe(
        "processed",
      ),
    );
  } finally {
    await feed.stop();
  }
});

test("unrelated work adds no reads and a real publication only reads forward", async () => {
  const original = { message_id: "m1", sequence: 1, direction: "input", issue_ids: ["related"] };
  const fresh = { message_id: "m2", sequence: 2, direction: "publication" };
  const get = vi.fn(async (path: string) => ({
    items: path.includes("latest") ? [original] : [fresh],
    next: path.includes("latest") ? 1 : 2,
    more: false,
  }));
  let emit!: (value: string) => void;
  const streams: string[] = [];
  const feed = new MessagingFeed({
    get: get as ApiClient["get"],
    noteAlive() {},
    async openStream(path, signal) {
      streams.push(path);
      return new Response(
        new ReadableStream({
          start(controller) {
            emit = (text) => controller.enqueue(new TextEncoder().encode(text));
            emit(`event: start\ndata: {"event_seq":${path.includes("after=") ? 41 : 1}}\n\n`);
            signal?.addEventListener("abort", () => controller.close(), { once: true });
          },
        }),
      );
    },
  });
  const ledger = (event_seq: number, event: Record<string, unknown>) =>
    `event: ledger\ndata: ${JSON.stringify({ event_seq, ...event })}\n\n`;
  feed.start();
  try {
    await vi.waitFor(() => expect(feed.state.get().items).toHaveLength(1));
    const unrelated = Array.from({ length: 40 }, (_, n) =>
      ledger(n + 2, {
        kind: "messaging.work_changed",
        object_type: "issue",
        object_id: "other",
        issue_id: "other",
      }),
    );
    emit(unrelated.join(""));
    emit(ledger(42, { kind: "messaging.published", object_type: "message", object_id: "m2" }));
    await vi.waitFor(() => expect(feed.state.get().items).toHaveLength(2));
    expect(get.mock.calls.map(([path]) => path)).toEqual([
      "/api/v1/messaging/messages?latest=1",
      "/api/v1/messaging/messages?after=1",
    ]);
    await feed.stop();
    feed.start();
    await vi.waitFor(() => expect(streams.at(-1)).toBe("/api/v1/messaging/events?after=42"));
  } finally {
    await feed.stop();
  }
});

test("a read receipt received during the initial page survives its stale snapshot", async () => {
  const message = { message_id: "first", sequence: 1, direction: "input", agent_read_at: 0 };
  let emit!: (value: string) => void;
  let completePage!: () => void;
  const snapshot = new Promise((resolve) => {
    completePage = () => resolve({ items: [message], next: 1, more: false });
  });
  const get = vi.fn(async (path: string) =>
    path.endsWith("/first") ? { ...message, agent_read_at: 1000 } : snapshot,
  );
  const noteAlive = vi.fn();
  const feed = new MessagingFeed({
    get: get as ApiClient["get"],
    noteAlive,
    async openStream(_path, signal) {
      return new Response(
        new ReadableStream({
          start(controller) {
            emit = (text) => controller.enqueue(new TextEncoder().encode(text));
            emit('event: start\ndata: {"event_seq":1}\n\n');
            signal?.addEventListener("abort", () => controller.close(), { once: true });
          },
        }),
      );
    },
  });
  feed.start();
  try {
    await vi.waitFor(() => expect(get).toHaveBeenCalledTimes(1));
    emit(
      'event: ledger\ndata: {"event_seq":2,"kind":"messaging.read","object_type":"message","object_id":"first"}\n\n',
    );
    await vi.waitFor(() => expect(noteAlive).toHaveBeenCalledTimes(2));
    completePage();
    await vi.waitFor(() => expect(feed.state.get().items[0]?.agent_read_at).toBe(1000));
  } finally {
    completePage();
    await feed.stop();
  }
});
