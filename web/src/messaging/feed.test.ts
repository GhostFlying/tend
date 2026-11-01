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
  let latest = 1;
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
  } as MessagingMessage;
  const feed = new MessagingFeed({
    get: (async (path: string) => {
      if (path.endsWith("/question")) {
        detailReads++;
        if (failRead) throw new Error("temporary read failure");
        return { ...initial, processing };
      }
      return {
        items: latest === 1 ? [initial] : [{ message_id: "new", sequence: latest }],
        next: latest,
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
    latest = 200;
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
      'event: ledger\ndata: {"event_seq":3,"object_type":"task","object_id":"task","kind":"messaging.work_changed"}\n\n',
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

test.each(["work change", "reconnect"])(
  "processed retained inputs refresh after %s",
  async (event) => {
    let latest = 1;
    let emit: (text: string) => void = () => {
      throw new Error("stream not started");
    };
    const original = {
      message_id: "old",
      sequence: 1,
      direction: "input",
      processing: "processed",
    } as MessagingMessage;
    const feed = new MessagingFeed({
      get: (async (path: string) =>
        path.endsWith("/old")
          ? { ...original, processing: "waiting_assignment" }
          : {
              items: latest === 1 ? [original] : [{ message_id: "new", sequence: latest }],
              next: latest,
              more: false,
            }) as ApiClient["get"],
      noteAlive() {},
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
      await vi.waitFor(() => expect(feed.state.get().items).toHaveLength(1));
      latest = 200;
      emit(
        event === "reconnect"
          ? 'event: start\ndata: {"event_seq":3}\n\n'
          : 'event: ledger\ndata: {"event_seq":2,"object_type":"issue","object_id":"issue","kind":"messaging.work_changed"}\n\n',
      );
      await vi.waitFor(() =>
        expect(feed.state.get().items.find((m) => m.message_id === "old")?.processing).toBe(
          "waiting_assignment",
        ),
      );
    } finally {
      await feed.stop();
    }
  },
);
